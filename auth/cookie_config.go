package auth

import (
	"strconv"
	"strings"

	"github.com/osbits/gorgany/v2/log"
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
// It is deliberately robust against the config layer rather than trusting it.
// Secure-by-default must not depend on `${VAR}` substitution, viper precedence or an
// operator's env file being right — all three of which have already gone wrong once:
// an unset `secure: ${SESSION_COOKIE_SECURE}` used to make IsSet report true with an
// empty value, so the guard below was skipped and GetBool("") returned false. Any
// value that is empty or does not parse as a boolean is therefore treated as unset.
func SessionCookieSecure() bool {
	if !viper.IsSet(ConfigSessionCookieSecure) {
		return true
	}

	switch value := viper.Get(ConfigSessionCookieSecure).(type) {
	case bool:
		return value
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			// Empty or unparseable: fall back to secure rather than to Go's zero
			// value for bool, which is exactly the wrong direction.
			if strings.TrimSpace(value) != "" {
				log.Log().Warnf(
					"auth: %s is %q, which is not a boolean; defaulting to Secure:true",
					ConfigSessionCookieSecure, value)
			}
			return true
		}
		return parsed
	case nil:
		return true
	default:
		log.Log().Warnf(
			"auth: %s is %T, which is not a boolean; defaulting to Secure:true",
			ConfigSessionCookieSecure, value)
		return true
	}
}
