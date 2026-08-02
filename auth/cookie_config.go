package auth

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/log"
	"github.com/spf13/viper"
)

// ConfigSessionCookieSecure controls the Secure attribute on the session cookie.
const ConfigSessionCookieSecure = "auth.session.cookie.secure"

// ConfigSessionCookieDomain widens the session cookie to a parent domain.
const ConfigSessionCookieDomain = "auth.session.cookie.domain"

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

// SessionCookieDomain returns the domain the session cookie is scoped to, or "" for a
// host-only cookie. Empty is the default and the safe answer: a cookie with a Domain is sent
// to every subdomain, and every subdomain can send one back.
func SessionCookieDomain() string {
	return strings.TrimSpace(viper.GetString(ConfigSessionCookieDomain))
}

// SessionCookieName is the name the session cookie is written and read under. Every call
// site, on both sides, has to go through here.
//
// The name used to be a bare compile-time constant, which is what made cookie tossing
// unfixable by an app: see core.HostPrefixedSessionCookieName for the mechanism. The prefixed
// form is therefore the default, and it is used whenever a browser would actually accept it —
// which is when the cookie is Secure and carries no Domain, because those are two of the
// three conditions the prefix imposes (Path=/ is the third and is unconditional here).
//
// The two fallbacks are explicit opt-outs, not silent downgrades:
//
//   - auth.session.cookie.secure: false, the documented local-development setting. A
//     `__Host-` cookie that is not Secure is rejected outright.
//   - auth.session.cookie.domain, set by an app that shares one session across subdomains.
//     A `__Host-` cookie may not carry a Domain, and that is precisely the property being
//     asked for.
//
// Emitting a prefixed name with either condition unmet would be worse than not prefixing at
// all: the browser discards the cookie without a word, so every request looks logged-out and
// nothing anywhere reports an error — the same silent failure the Secure default was written
// to prevent.
func SessionCookieName() string {
	name := resolveSessionCookieName()
	announceSessionCookieForm(name)
	return name
}

func resolveSessionCookieName() string {
	if SessionCookieDomain() != "" {
		return core.SessionCookieBaseName
	}

	if !SessionCookieSecure() {
		return core.SessionCookieBaseName
	}

	return core.HostPrefixedSessionCookieName
}

// announcedSessionCookieForm keeps the decision to one log line.
//
// It is announced on the first resolution rather than from a provider's Boot, because the name
// is derived from configuration and has no owner to announce it — but every request resolves
// it, so a server logs the line while serving its first one and an operator sees it at
// startup. The line exists because the choice is invisible otherwise: falling back to the
// unprefixed name is a real change in exposure, and an operator has to be able to tell which
// form is on the wire. The reason is composed inside the Once so that resolving the name on
// every request stays an atomic load and two viper lookups.
var announcedSessionCookieForm sync.Once

func announceSessionCookieForm(name string) {
	announcedSessionCookieForm.Do(func() {
		log.Log().Infof("auth: session cookie is named %q — %s", name, sessionCookieFormReason())
	})
}

func sessionCookieFormReason() string {
	if domain := SessionCookieDomain(); domain != "" {
		return fmt.Sprintf(
			"%s is %q, and a __Host- cookie may not carry a Domain; this app has opted into "+
				"a cookie its sibling subdomains can also set",
			ConfigSessionCookieDomain, domain)
	}

	if !SessionCookieSecure() {
		return fmt.Sprintf("%s is false, and a __Host- cookie is only accepted when Secure",
			ConfigSessionCookieSecure)
	}

	return "host-locked: no other host can set this cookie for yours"
}

// NewSessionCookie builds the session cookie. It is the only correct way to construct one:
// the name, the Secure flag and the Domain are not three independent decisions, and pairing
// a `__Host-` name with a Domain produces a cookie no browser keeps.
//
// maxAge is the cookie's lifetime in seconds; a negative value expires it. sameSite is Lax on
// issue and Strict on invalidation, which is what the two call sites have always used.
func NewSessionCookie(value string, maxAge int, sameSite http.SameSite) *http.Cookie {
	name := SessionCookieName()

	cookie := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   SessionCookieSecure(),
		HttpOnly: true,
		SameSite: sameSite,
	}

	// Only the unprefixed form may carry a Domain, and only then is there one configured —
	// the resolver above already refuses the prefix when a Domain is set, so this is the
	// structural half of the same guarantee rather than a second decision.
	if name != core.HostPrefixedSessionCookieName {
		cookie.Domain = SessionCookieDomain()
	}

	return cookie
}
