package auth

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func withCookieSecureConfig(t *testing.T, set bool, value bool) {
	t.Helper()

	previous := viper.Get(ConfigSessionCookieSecure)
	wasSet := viper.IsSet(ConfigSessionCookieSecure)

	if set {
		viper.Set(ConfigSessionCookieSecure, value)
	} else {
		viper.Set(ConfigSessionCookieSecure, nil)
	}

	t.Cleanup(func() {
		if wasSet {
			viper.Set(ConfigSessionCookieSecure, previous)
		} else {
			viper.Set(ConfigSessionCookieSecure, nil)
		}
	})
}

// TestSessionCookieSecureDefaultsToTrue keeps the pre-v2 posture for every app that
// configures nothing: the secure default must not become insecure by omission.
func TestSessionCookieSecureDefaultsToTrue(t *testing.T) {
	withCookieSecureConfig(t, false, false)

	assert.True(t, SessionCookieSecure(),
		"an unset key must default to Secure:true, not to viper.GetBool's false")
}

// TestSessionCookieSecureCanBeDisabledForLocalDev is the T3.6 fix. Secure was
// hard-coded true, so plain-HTTP local development silently failed to
// authenticate: the browser took the Set-Cookie and then refused to send it back
// over http://, making every post-login request look logged-out.
func TestSessionCookieSecureCanBeDisabledForLocalDev(t *testing.T) {
	withCookieSecureConfig(t, true, false)

	assert.False(t, SessionCookieSecure())
}

func TestSessionCookieSecureCanBeSetExplicitlyTrue(t *testing.T) {
	withCookieSecureConfig(t, true, true)

	assert.True(t, SessionCookieSecure())
}

// TestSessionCookieSecureIsTrueWhenThePlaceholderIsUnresolved is the A5 security
// assertion, and it is the real safety net.
//
// The documented convention is `secure: ${SESSION_COOKIE_SECURE}` in config.yaml.
// With that variable unset, the key still exists in viper's config layer holding the
// literal "${SESSION_COOKIE_SECURE}" — the parser cannot remove it, and viper's
// config layer outranks any default. So IsSet is true and GetBool on that string is
// false, which is exactly how the v2 secure-by-default guard came to ship a session
// cookie WITHOUT Secure on a fresh checkout, a CI runner, or a container missing one
// env line.
//
// This function therefore refuses to trust the config layer: anything that is not a
// parseable boolean means "unset", and unset means Secure.
func TestSessionCookieSecureIsTrueWhenThePlaceholderIsUnresolved(t *testing.T) {
	withCookieSecureConfig(t, true, false) // establishes the key...
	viper.Set(ConfigSessionCookieSecure, "${SESSION_COOKIE_SECURE}")

	assert.True(t, SessionCookieSecure(),
		"an unresolved ${VAR} must not disable Secure")
}

// TestSessionCookieSecureIgnoresUnparseableValues covers the rest of the shapes a
// misconfigured layer can produce. Every one must fall back to Secure, never to Go's
// zero value for bool.
func TestSessionCookieSecureIgnoresUnparseableValues(t *testing.T) {
	for _, value := range []any{
		"",                // blanked by a substitution
		"   ",             // whitespace
		"${ANYTHING}",     // unresolved placeholder
		"yes-please",      // not a boolean
		42,                // wrong type entirely
		[]string{"false"}, // wrong type entirely
	} {
		withCookieSecureConfig(t, true, false)
		viper.Set(ConfigSessionCookieSecure, value)

		assert.Truef(t, SessionCookieSecure(),
			"value %#v must be treated as unset and default to Secure", value)
	}
}

// TestSessionCookieSecureStillHonoursRealBooleans makes sure the hardening did not
// break the legitimate local-dev opt-out, in both bool and string form.
func TestSessionCookieSecureStillHonoursRealBooleans(t *testing.T) {
	for _, value := range []any{false, "false", "FALSE", "0", " false "} {
		withCookieSecureConfig(t, true, false)
		viper.Set(ConfigSessionCookieSecure, value)

		assert.Falsef(t, SessionCookieSecure(),
			"value %#v is an explicit opt-out and must be honoured", value)
	}

	for _, value := range []any{true, "true", "TRUE", "1"} {
		withCookieSecureConfig(t, true, false)
		viper.Set(ConfigSessionCookieSecure, value)

		assert.Truef(t, SessionCookieSecure(), "value %#v means Secure", value)
	}
}
