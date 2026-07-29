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
