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
// Now: an absent variable leaves its key alone, so defaults and IsSet behave;
// untouched keys are never rewritten; and a security-relevant key whose placeholder
// cannot be resolved stops the boot.
//
// An explicitly-empty variable (`FOO=` in the environment) is a real value of "",
// because an empty string is legitimate for some keys — a blank cookie domain, for
// instance. Only an *absent* variable falls through to the default.
func Parse(files ...string) error {
	for _, file := range files {
		dir, fileName := parsePath(file)
		viper.AddConfigPath(dir)
		viper.SetConfigName(fileName)
		err := viper.MergeInConfig()
		if err != nil {
			return err
		}
	}

	return ResolveEnvPlaceholders()
}

// ResolveEnvPlaceholders substitutes `${VAR}` values in the loaded config.
//
// Exported so a test, or an app that loads config its own way, can apply the same
// semantics.
func ResolveEnvPlaceholders() error {
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
			// Leave the key untouched so any default still applies and IsSet stays
			// false. Writing "" here is what made the secure-cookie guard fail open.
			unresolved[key] = name
			if isSecurityRelevant(key) {
				unresolvedSecurityKeys = append(unresolvedSecurityKeys,
					fmt.Sprintf("%s (${%s})", key, name))
			}
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

	for _, key := range sortedKeys(unresolved) {
		log.Log().Warnf(
			"config: %s references ${%s}, which is not set; leaving the key unset so its "+
				"default applies", key, unresolved[key])
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
