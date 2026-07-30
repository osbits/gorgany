package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/osbits/gorgany/log"
	"github.com/spf13/viper"
)

// SecurityRelevantKeys are config keys whose unresolved placeholder must stop the
// boot rather than quietly degrade.
//
// A missing JWT secret means tokens signed with an empty key; a missing
// secure-cookie flag used to mean a session cookie shipped without Secure. Neither
// should first be discovered in production.
var SecurityRelevantKeys = []string{
	"auth.jwt.secret",
	"auth.session.cookie.secure",
}

// envPlaceholder reports the variable named by a `${VAR}` value.
func envPlaceholder(value string) (string, bool) {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	if name == "" {
		return "", false
	}
	return name, true
}

// Parse merges the given config files and resolves `${VAR}` placeholders from the
// environment.
//
// Two defects were fixed here, one security-relevant.
//
// It used os.Getenv, which cannot tell "unset" from "set to empty", and wrote the
// result with viper.Set — viper's *override* layer, the highest precedence there is.
// A placeholder for a missing variable therefore produced a key that was present,
// empty, and unbeatable by any default. That defeated the secure-cookie guard added
// in T3.6: with the documented `secure: ${SESSION_COOKIE_SECURE}` and the variable
// unset — a fresh checkout, a CI runner, a container missing one env line —
// viper.IsSet returned true, the secure-by-default branch was skipped, GetBool("")
// returned false, and the session cookie shipped without Secure. The one guard added
// to keep cookies safe by default failed open, along the exact path the config sample
// tells people to use.
//
// The else branch also rewrote every untouched key through the override layer, so
// afterwards viper.IsSet was true for every key in config.yaml and viper.SetDefault
// was inert for all of them — a trap for every default added from then on.
//
// Now: untouched keys are never rewritten, a security-relevant key whose placeholder
// cannot be resolved stops the boot, and any other unresolved placeholder is blanked.
//
// Blanking, rather than leaving the literal in place, is a correction to the first fix
// (F5). "Leave the key alone so its default applies" cannot work — every key
// viper.AllKeys() reaches is already in viper's *config* layer, which outranks SetDefault,
// so the key stays set whatever this function does. That left the realistic choice between
// an empty string and a literal `${VAR}`, and only one of those is a value a consumer
// accepts: an unresolved `auth.session.cookie.domain` became `Domain: "${COOKIE_DOMAIN}"`,
// an invalid cookie attribute, so the browser dropped Set-Cookie and login failed silently.
// See KeepUnresolvedLiterals for the opt-out.
//
// An explicitly-empty variable (`FOO=` in the environment) is a real value of "",
// because an empty string is legitimate for some keys — a blank cookie domain, for
// instance. It is indistinguishable from the blanked case in the config, but not in the
// log: only an absent variable warns.
func Parse(files ...string) error {
	return ParseWithOptions(files, nil)
}

// ParseWithOptions is Parse with control over unresolved placeholders, for an app that
// loads its own config and wants KeepUnresolvedLiterals.
func ParseWithOptions(files []string, opts []ResolveOption) error {
	for _, file := range files {
		dir, fileName := parsePath(file)
		viper.AddConfigPath(dir)
		viper.SetConfigName(fileName)
		err := viper.MergeInConfig()
		if err != nil {
			return err
		}
	}

	return ResolveEnvPlaceholders(opts...)
}

// ResolveOption adjusts what ResolveEnvPlaceholders does with an unresolved placeholder.
type ResolveOption func(*resolveOptions)

type resolveOptions struct {
	keepUnresolvedLiterals bool
}

// KeepUnresolvedLiterals leaves an unresolved `${VAR}` in place as the literal string
// instead of blanking it.
//
// The default is to blank, because the literal is a value nothing accepts: an unresolved
// `auth.session.cookie.domain` becomes `Domain: "${COOKIE_DOMAIN}"`, which is an invalid
// cookie attribute, so the browser drops Set-Cookie and login fails with no error anywhere
// — the same class of silent failure the secure-cookie work set out to eliminate. An app
// carrying ten optional placeholders had ten of these.
//
// Use this when the literal genuinely aids diagnosis and an empty value would not — a
// database host is the case, since a connection error naming ${DB_HOST} beats one naming
// the empty string. Note that the warning already names every unresolved key either way,
// so the diagnostic argument is weaker than it looks.
func KeepUnresolvedLiterals() ResolveOption {
	return func(o *resolveOptions) { o.keepUnresolvedLiterals = true }
}

// ResolveEnvPlaceholders substitutes `${VAR}` values in the loaded config.
//
// An unresolved placeholder is blanked by default; see KeepUnresolvedLiterals. A
// security-relevant key whose placeholder cannot be resolved stops the boot either way.
//
// Exported so a test, or an app that loads config its own way, can apply the same
// semantics. An app that substitutes placeholders itself should call this instead: a local
// `viper.Set` loop reintroduces the sibling-wipe described below, and the symptom is a boot
// panic naming an unrelated key.
func ResolveEnvPlaceholders(opts ...ResolveOption) error {
	options := resolveOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	unresolved := make(map[string]string)
	var unresolvedSecurityKeys []string

	// Substitutions are collected and applied together through MergeConfigMap rather
	// than written one at a time with viper.Set. viper.Set writes the *override* layer,
	// and a map fetch resolves against the highest layer that holds the key without
	// deep-merging the ones below: after Set("databases.default.host", ...),
	// GetStringMap("databases") returns {"default": {"host": ...}} and every sibling —
	// driver, log, properties — is gone.
	//
	// That is not hypothetical. It is exactly how the e2e fixture app failed to boot
	// with "datasource config: 'driver' is required" once this function stopped
	// promoting *every* key: the old else branch rewrote untouched keys too, which
	// happened to keep the override subtree complete and hid the hazard.
	//
	// MergeConfigMap deep-merges into the *config* layer, so siblings survive, defaults
	// still apply to keys absent from the file, and IsSet keeps its file-based meaning.
	substitutions := make(map[string]any)

	for _, key := range viper.AllKeys() {
		value, isString := viper.Get(key).(string)
		if !isString {
			// Not a string, so not a placeholder. Leave it exactly where it is:
			// rewriting would promote it into the override layer.
			continue
		}

		name, isPlaceholder := envPlaceholder(value)
		if !isPlaceholder {
			continue
		}

		resolved, present := os.LookupEnv(name)
		if !present {
			unresolved[key] = name
			if isSecurityRelevant(key) {
				unresolvedSecurityKeys = append(unresolvedSecurityKeys,
					fmt.Sprintf("%s (${%s})", key, name))
				// Do not substitute a security-relevant key at all: the boot is about to
				// fail, and writing anything would matter only if a caller ignored the
				// error.
				continue
			}

			if options.keepUnresolvedLiterals {
				continue
			}

			// Blank it. The key cannot be *unset* — every key AllKeys() returns is
			// already in viper's config layer, which outranks SetDefault — so the
			// realistic choice is between an empty string and a literal `${VAR}`, and
			// only one of those is a value any consumer accepts.
			setNested(substitutions, key, "")
			continue
		}

		setNested(substitutions, key, resolved)
	}

	if len(substitutions) > 0 {
		if err := viper.MergeConfigMap(substitutions); err != nil {
			return fmt.Errorf("config: could not apply environment substitutions: %w", err)
		}
	}

	if len(unresolvedSecurityKeys) > 0 {
		sort.Strings(unresolvedSecurityKeys)
		return fmt.Errorf(
			"config: security-relevant key(s) reference environment variables that are not "+
				"set: %s. Set them, or remove the placeholder so the framework's secure "+
				"default applies — an unset placeholder must never silently weaken security",
			strings.Join(unresolvedSecurityKeys, ", "))
	}

	// The message used to say "leaving the key unset so its default applies". Neither
	// clause was true, and its own test conceded as much: every key AllKeys() reaches is
	// in viper's config layer, which outranks SetDefault, so the key stays set and no
	// default ever applies. Saying what actually happens matters because the reader is
	// deciding whether they still need to set the variable.
	outcome := "using an empty value instead"
	if options.keepUnresolvedLiterals {
		outcome = "leaving the literal in place, so the key reads back as the placeholder " +
			"text rather than a usable value"
	}

	for _, key := range sortedKeys(unresolved) {
		log.Log().Warnf(
			"config: %s references ${%s}, which is not set; %s. The key stays present, so a "+
				"SetDefault for it will not apply — set the variable, or remove the "+
				"placeholder to fall back to the framework default.",
			key, unresolved[key], outcome)
	}

	return nil
}

// setNested writes value at a dotted key path, creating the intervening maps.
//
// The path segments come from viper.AllKeys(), which lowercases them, so they line up
// with what MergeConfigMap expects.
func setNested(root map[string]any, dottedKey string, value any) {
	segments := strings.Split(dottedKey, ".")

	current := root
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[segment] = next
		}
		current = next
	}

	current[segments[len(segments)-1]] = value
}

// isSecurityRelevant reports whether an unresolved placeholder for this key should
// stop the boot.
func isSecurityRelevant(key string) bool {
	for _, secure := range SecurityRelevantKeys {
		if strings.EqualFold(key, secure) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func parsePath(fullPath string) (string, string) {
	splitPath := strings.Split(fullPath, "/")
	return strings.Join(splitPath[:len(splitPath)-1], "/"), splitPath[len(splitPath)-1]
}
