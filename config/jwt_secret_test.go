package config

import (
	"os"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withoutJwtSecretEnv removes JWT_SECRET for the duration of a test.
//
// A test that asserts "an app with no secret must not boot" is measuring the process
// environment as much as the code, and anyone working on a JWT-using project has JWT_SECRET
// exported — so does the CI job that builds one. Without this the assertion inverts and
// reports "an error is expected but got nil", which reads as the guard being broken and
// invites someone to relax the assertion rather than the environment. t.Setenv cannot
// express "unset", hence the manual restore.
func withoutJwtSecretEnv(t *testing.T) {
	t.Helper()

	previous, wasSet := os.LookupEnv("JWT_SECRET")
	require.NoError(t, os.Unsetenv("JWT_SECRET"))
	t.Cleanup(func() {
		if wasSet {
			require.NoError(t, os.Setenv("JWT_SECRET", previous))
			return
		}
		require.NoError(t, os.Unsetenv("JWT_SECRET"))
	})
}

// The absent-key hole. ResolveEnvPlaceholders can only examine keys viper.AllKeys()
// returns, so a config that never mentions auth.jwt.secret at all never reached the
// security-relevant guard: the app booted with an empty HMAC key and signed and accepted
// tokens with it. AllKeys() does include keys registered with SetDefault, which is what
// the fix leans on.

// TestAJwtSectionWithNoSecretStopsTheBoot: the section is there, so this app means to use
// JWT, and the one line that matters is missing.
func TestAJwtSectionWithNoSecretStopsTheBoot(t *testing.T) {
	withoutJwtSecretEnv(t)

	loadYAML(t, `
auth:
  jwt:
    lifeTime: 3600
`)

	err := ResolveEnvPlaceholders()

	require.Error(t, err, "an app that configures JWT with no secret must not boot")
	assert.Contains(t, err.Error(), "auth.jwt.secret")
	assert.Contains(t, err.Error(), "JWT_SECRET",
		"the operator needs to be told which variable to set")
}

// TestAJwtSectionWithNoSecretResolvesFromTheEnvironment is the other half of the same
// default: the key is absent from the file, JWT_SECRET is set, so the app works. That is
// the documented idiom without the config line.
func TestAJwtSectionWithNoSecretResolvesFromTheEnvironment(t *testing.T) {
	const secret = "PyD8yhvAyBFC0Qs4Q9k1TfKp7cJmVn2xLr6WdZbGtHs"
	t.Setenv("JWT_SECRET", secret)

	loadYAML(t, `
auth:
  session:
    storage: database
  jwt:
    lifeTime: 3600
`)

	require.NoError(t, ResolveEnvPlaceholders())
	assert.Equal(t, secret, viper.GetString("auth.jwt.secret"))

	// The substituted value is merged into the config layer, and this is the one key whose
	// substitution originates in the defaults layer, so it is worth pinning that the merge
	// leaves its siblings alone.
	assert.Equal(t, 3600, viper.GetInt("auth.jwt.lifeTime"))
	assert.Equal(t, "database", viper.GetString("auth.session.storage"))
}

// TestAnAppWithNoJwtSectionIsNotForcedToConfigureASecret. A session-only app has no
// business being told to set JWT_SECRET, so the default is registered only for an app
// whose config declares the auth.jwt section.
func TestAnAppWithNoJwtSectionIsNotForcedToConfigureASecret(t *testing.T) {
	loadYAML(t, `
auth:
  session:
    storage: memory
`)

	require.NoError(t, ResolveEnvPlaceholders(),
		"an app that never uses JWT must boot without a JWT secret")
	assert.False(t, viper.IsSet("auth.jwt.secret"),
		"and no auth.jwt.secret should be invented for it")
}

// TestTheJwtSecretErrorDoesNotAdviseRemovingThePlaceholder.
//
// The message used to end with "remove the placeholder so the framework's secure default
// applies". There is no secure default for a signing key, so an operator who followed the
// framework's own advice turned a caught boot failure into an empty HMAC key that accepts
// forged tokens.
func TestTheJwtSecretErrorDoesNotAdviseRemovingThePlaceholder(t *testing.T) {
	loadYAML(t, buildNestedYAML("auth.jwt.secret", "${DEFINITELY_NOT_SET_ANYWHERE}"))

	err := ResolveEnvPlaceholders()

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "remove the placeholder",
		"removing it for a key with no safe fallback is the bypass, not the fix")
	assert.Contains(t, err.Error(), "no secure fallback")
}

// TestTheSecureCookieErrorStillOffersTheDefault: auth.session.cookie.secure does have a
// secure fallback — it defaults to true — so for that key the advice was correct and must
// survive.
func TestTheSecureCookieErrorStillOffersTheDefault(t *testing.T) {
	loadYAML(t, buildNestedYAML("auth.session.cookie.secure", "${DEFINITELY_NOT_SET_ANYWHERE}"))

	err := ResolveEnvPlaceholders()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "remove the placeholder")
}
