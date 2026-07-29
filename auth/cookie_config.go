package auth

import (
	"github.com/spf13/viper"
)

// ConfigSessionCookieSecure controls the Secure attribute on the session cookie.
const ConfigSessionCookieSecure = "auth.session.cookie.secure"

// SessionCookieSecure reports whether the session cookie should carry the Secure
// attribute. It defaults to true.
//
// The attribute used to be hard-coded to true, which meant plain-HTTP local
// development silently failed to authenticate: the browser accepted the
// Set-Cookie header and then declined to send the cookie back over http://, so
// every request after login looked logged-out. Safari is the strictest about it,
// but no browser sends a Secure cookie over plain HTTP to a non-localhost host.
//
// Set it to false only for local development over http://:
//
//	auth:
//	  session:
//	    cookie:
//	      secure: false   # local dev over http:// only — never in production
//
// Because the default is true, an app that configures nothing keeps the pre-v2
// behaviour. Note that viper.GetBool returns false for an unset key, so the
// presence of the key is checked explicitly rather than reading the bool directly.
func SessionCookieSecure() bool {
	if !viper.IsSet(ConfigSessionCookieSecure) {
		return true
	}
	return viper.GetBool(ConfigSessionCookieSecure)
}
