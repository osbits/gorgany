package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise `${VAR}` substitution through viper, because the bug was in
// how substitution interacted with viper's precedence layers — not in the string
// handling.

// loadYAML resets viper and reads yaml into it, mimicking Parse's merge step.
func loadYAML(t *testing.T, yaml string) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader(yaml)))
}

// TestUnsetPlaceholderIsLeftVisibleNotBlanked is the A5 headline, and it documents a
// correction to the brief.
//
// The brief prescribed: "If the variable is absent, leave the key alone so defaults
// and IsSet behave." Leaving the key alone is right, but the stated reason does not
// hold: a key written in config.yaml lives in viper's *config* layer, which outranks
// SetDefault. IsSet is therefore true and GetString returns the literal "${VAR}" no
// matter what this parser does — verified, not assumed.
//
// So the value is deliberately left as the literal placeholder rather than blanked to
// "". An app that misconfigures DB_HOST gets a connection error naming
// "${DB_HOST}", which is instantly diagnosable; the old behaviour gave it an empty
// host and no clue. Security-relevant keys do not rely on this at all — they stop the
// boot (see TestAnUnsetSecurityRelevantPlaceholderStopsTheBoot), and their consumers
// are independently hardened.
func TestUnsetPlaceholderIsLeftVisibleNotBlanked(t *testing.T) {
	loadYAML(t, `
databases:
  default:
    host: ${DEFINITELY_NOT_SET_ANYWHERE}
`)

	require.NoError(t, ResolveEnvPlaceholders())

	assert.Equal(t, "${DEFINITELY_NOT_SET_ANYWHERE}",
		viper.GetString("databases.default.host"),
		"an unresolved placeholder stays visible instead of becoming an empty string")
}

// TestADefaultAppliesToAKeyAbsentFromTheFile is the part of the override-layer fix
// that is actually observable. The old else branch promoted *every* config key into
// the override layer, which made SetDefault inert for all of them; keys absent from
// the file were collateral damage of that promotion.
func TestADefaultAppliesToAKeyAbsentFromTheFile(t *testing.T) {
	loadYAML(t, `
some:
  present: in-file
`)
	viper.SetDefault("some.absent", "the-default")

	require.NoError(t, ResolveEnvPlaceholders())

	assert.Equal(t, "the-default", viper.GetString("some.absent"),
		"a default for a key not in the file must still apply after substitution")
	assert.Equal(t, "in-file", viper.GetString("some.present"))
}

// TestASetVariableIsSubstituted is the happy path.
func TestASetVariableIsSubstituted(t *testing.T) {
	t.Setenv("GORGANY_TEST_HOST", "db.internal")

	loadYAML(t, `
databases:
  default:
    host: ${GORGANY_TEST_HOST}
`)

	require.NoError(t, ResolveEnvPlaceholders())
	assert.Equal(t, "db.internal", viper.GetString("databases.default.host"))
}

// TestAnExplicitlyEmptyVariableIsARealValue documents the decision the brief asked
// for: `FOO=` in the environment means an empty string, which is legitimate for keys
// like a blank cookie domain. Only an *absent* variable falls through to the default.
func TestAnExplicitlyEmptyVariableIsARealValue(t *testing.T) {
	t.Setenv("GORGANY_TEST_EMPTY", "")

	loadYAML(t, `
some:
  key: ${GORGANY_TEST_EMPTY}
`)
	viper.SetDefault("some.key", "the-default")

	require.NoError(t, ResolveEnvPlaceholders())

	assert.Equal(t, "", viper.GetString("some.key"),
		"an explicitly empty variable is a value, not an absence")
}

// TestUntouchedKeysAreNotPromotedIntoTheOverrideLayer covers the second defect. The
// old else branch rewrote every key through viper.Set, so afterwards IsSet was true
// for every key in config.yaml and SetDefault was inert for all of them — a trap for
// every future IsSet-guarded default, of which the cookie guard was the first victim.
func TestUntouchedKeysAreNotPromotedIntoTheOverrideLayer(t *testing.T) {
	loadYAML(t, `
plain:
  value: hello
`)

	require.NoError(t, ResolveEnvPlaceholders())

	// A default for a key that is NOT in the file must still work afterwards.
	viper.SetDefault("plain.other", "default-wins")
	assert.Equal(t, "default-wins", viper.GetString("plain.other"))

	// And the file value is untouched.
	assert.Equal(t, "hello", viper.GetString("plain.value"))
}

// TestAnUnsetSecurityRelevantPlaceholderStopsTheBoot: a missing JWT secret or
// secure-cookie flag must be loud, not a silent downgrade.
func TestAnUnsetSecurityRelevantPlaceholderStopsTheBoot(t *testing.T) {
	for _, key := range []string{"auth.jwt.secret", "auth.session.cookie.secure"} {
		t.Run(key, func(t *testing.T) {
			loadYAML(t, buildNestedYAML(key, "${DEFINITELY_NOT_SET_ANYWHERE}"))

			err := ResolveEnvPlaceholders()

			require.Error(t, err, "%s must not resolve silently", key)
			assert.Contains(t, err.Error(), key)
			assert.Contains(t, err.Error(), "DEFINITELY_NOT_SET_ANYWHERE")
			assert.Contains(t, err.Error(), "security-relevant")
		})
	}
}

// TestASetSecurityRelevantPlaceholderIsFine
func TestASetSecurityRelevantPlaceholderIsFine(t *testing.T) {
	t.Setenv("GORGANY_TEST_SECRET", "s3cret")

	loadYAML(t, `
auth:
  jwt:
    secret: ${GORGANY_TEST_SECRET}
`)

	require.NoError(t, ResolveEnvPlaceholders())
	assert.Equal(t, "s3cret", viper.GetString("auth.jwt.secret"))
}

// TestASecurityRelevantKeyWithNoPlaceholderIsFine — a literal value needs no
// environment variable.
func TestASecurityRelevantKeyWithNoPlaceholderIsFine(t *testing.T) {
	loadYAML(t, `
auth:
  jwt:
    secret: literal-secret
`)

	require.NoError(t, ResolveEnvPlaceholders())
	assert.Equal(t, "literal-secret", viper.GetString("auth.jwt.secret"))
}

// TestNonStringValuesAreLeftAlone: numbers and booleans cannot be placeholders and
// must not be rewritten.
func TestNonStringValuesAreLeftAlone(t *testing.T) {
	loadYAML(t, `
databases:
  default:
    port: 5432
    log: false
`)

	require.NoError(t, ResolveEnvPlaceholders())

	assert.Equal(t, 5432, viper.GetInt("databases.default.port"))
	assert.False(t, viper.GetBool("databases.default.log"))
}

func TestEnvPlaceholder(t *testing.T) {
	tests := map[string]struct {
		name string
		ok   bool
	}{
		"${FOO}":    {name: "FOO", ok: true},
		"${A_B_C1}": {name: "A_B_C1", ok: true},
		"plain":     {ok: false},
		"${}":       {ok: false},
		"${FOO":     {ok: false},
		"FOO}":      {ok: false},
		"pre${FOO}": {ok: false},
		"":          {ok: false},
	}

	for value, want := range tests {
		name, ok := envPlaceholder(value)
		assert.Equalf(t, want.ok, ok, "envPlaceholder(%q) ok", value)
		assert.Equalf(t, want.name, name, "envPlaceholder(%q) name", value)
	}
}

func TestIsSecurityRelevant(t *testing.T) {
	assert.True(t, isSecurityRelevant("auth.jwt.secret"))
	assert.True(t, isSecurityRelevant("AUTH.JWT.SECRET"), "matching is case-insensitive")
	assert.True(t, isSecurityRelevant("auth.session.cookie.secure"))
	assert.False(t, isSecurityRelevant("databases.default.host"))
}

func TestParsePath(t *testing.T) {
	dir, name := parsePath("config/config")
	assert.Equal(t, "config", dir)
	assert.Equal(t, "config", name)
}

// buildNestedYAML turns a dotted key into nested YAML so a test can target any key.
func buildNestedYAML(dottedKey, value string) string {
	parts := strings.Split(dottedKey, ".")

	var b strings.Builder
	for i, part := range parts {
		b.WriteString(strings.Repeat("  ", i))
		b.WriteString(part)
		if i == len(parts)-1 {
			b.WriteString(": " + value + "\n")
		} else {
			b.WriteString(":\n")
		}
	}
	return b.String()
}

// TestSubstitutionDoesNotWipeSiblingKeys is the test whose absence let a boot-breaking
// regression ship past the whole unit suite.
//
// Substituting a leaf with viper.Set writes the *override* layer, and a map fetch
// resolves against the highest layer holding the key without deep-merging the ones
// below. So after Set("databases.default.host", ...), GetStringMap("databases") returned
// {"default": {"host": ...}} and driver, log and properties were gone — which is how the
// e2e fixture app came to die with "datasource config: 'driver' is required".
//
// Every unit test passed throughout, because they all read scalars through GetString.
// This one reads the map, which is what provider.configuredConnections does.
func TestSubstitutionDoesNotWipeSiblingKeys(t *testing.T) {
	t.Setenv("GORGANY_TEST_DB_HOST", "db.internal")
	t.Setenv("GORGANY_TEST_DB_PORT", "5432")

	loadYAML(t, `
databases:
  default:
    driver: postgres_gorm
    host: ${GORGANY_TEST_DB_HOST}
    port: ${GORGANY_TEST_DB_PORT}
    prefer_simple_protocol: true
    log: false
    properties:
      maxOpenConnections: 5
`)

	require.NoError(t, ResolveEnvPlaceholders())

	// This is the read that broke: the db provider fetches the whole subtree.
	databases := viper.GetStringMap("databases")
	defaults, ok := databases["default"].(map[string]any)
	require.True(t, ok, "the databases subtree must still be a map")

	assert.Equal(t, "postgres_gorm", defaults["driver"],
		"a sibling of a substituted key must survive substitution")
	assert.Equal(t, "db.internal", defaults["host"], "and the substitution must have applied")
	assert.Equal(t, "5432", defaults["port"])
	assert.Equal(t, true, defaults["prefer_simple_protocol"])
	assert.Equal(t, false, defaults["log"])
	assert.NotNil(t, defaults["properties"], "a nested sibling map must survive too")

	// And the scalar reads keep working.
	assert.Equal(t, "db.internal", viper.GetString("databases.default.host"))
	assert.Equal(t, "postgres_gorm", viper.GetString("databases.default.driver"))
}

// TestAnUnresolvedPlaceholderAlsoLeavesSiblingsAlone: the untouched path must not
// disturb the subtree either.
func TestAnUnresolvedPlaceholderAlsoLeavesSiblingsAlone(t *testing.T) {
	loadYAML(t, `
databases:
  default:
    driver: postgres_gorm
    host: ${DEFINITELY_NOT_SET_ANYWHERE}
`)

	require.NoError(t, ResolveEnvPlaceholders())

	defaults, ok := viper.GetStringMap("databases")["default"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "postgres_gorm", defaults["driver"])
	assert.Equal(t, "${DEFINITELY_NOT_SET_ANYWHERE}", defaults["host"])
}

// TestSubstitutionAcrossSeveralSubtrees: one MergeConfigMap call carries every
// substitution, so a nested map built for one subtree must not clobber another.
func TestSubstitutionAcrossSeveralSubtrees(t *testing.T) {
	t.Setenv("GORGANY_TEST_A", "value-a")
	t.Setenv("GORGANY_TEST_B", "value-b")
	t.Setenv("GORGANY_TEST_C", "value-c")

	loadYAML(t, `
one:
  deep:
    nested: ${GORGANY_TEST_A}
    kept: keep-one
  sibling: keep-two
two:
  value: ${GORGANY_TEST_B}
  kept: keep-three
top: ${GORGANY_TEST_C}
`)

	require.NoError(t, ResolveEnvPlaceholders())

	assert.Equal(t, "value-a", viper.GetString("one.deep.nested"))
	assert.Equal(t, "keep-one", viper.GetString("one.deep.kept"))
	assert.Equal(t, "keep-two", viper.GetString("one.sibling"))
	assert.Equal(t, "value-b", viper.GetString("two.value"))
	assert.Equal(t, "keep-three", viper.GetString("two.kept"))
	assert.Equal(t, "value-c", viper.GetString("top"))

	one := viper.GetStringMap("one")
	deep, ok := one["deep"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "value-a", deep["nested"])
	assert.Equal(t, "keep-one", deep["kept"])
	assert.Equal(t, "keep-two", one["sibling"])
}

func TestSetNested(t *testing.T) {
	root := map[string]any{}

	setNested(root, "a", 1)
	setNested(root, "b.c", 2)
	setNested(root, "b.d.e", 3)
	setNested(root, "b.d.f", 4)

	assert.Equal(t, map[string]any{
		"a": 1,
		"b": map[string]any{
			"c": 2,
			"d": map[string]any{"e": 3, "f": 4},
		},
	}, root)
}

// TestSetNestedOverwritesAScalarInThePath: a config declaring both `a: 1` and `a.b: 2`
// is contradictory, and setNested must not panic on it.
func TestSetNestedOverwritesAScalarInThePath(t *testing.T) {
	root := map[string]any{"a": "scalar"}

	require.NotPanics(t, func() { setNested(root, "a.b", 2) })
	assert.Equal(t, map[string]any{"a": map[string]any{"b": 2}}, root)
}
