package auth

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The names are spelled out here rather than taken from the resolver. Asserting that the
// emitted cookie is named whatever the resolver returns would pass for any resolver at all,
// including one that hands out an unprefixed name; these tests exist to pin the actual
// wire-level names.
//
// The configuration key is spelled out for the same reason: it is what an operator writes
// in config.yaml, so the test has to fail if the code starts reading a different one.
const (
	bareCookieName         = "GRG_SESSION_ID"
	hostPrefixedCookieName = "__Host-" + bareCookieName
	someParentCookieDomain = "example.com"
	cookieDomainConfigKey  = "auth.session.cookie.domain"
)

func withCookieDomainConfig(t *testing.T, domain string) {
	t.Helper()

	previous := viper.Get(cookieDomainConfigKey)
	wasSet := viper.IsSet(cookieDomainConfigKey)

	viper.Set(cookieDomainConfigKey, domain)

	t.Cleanup(func() {
		if wasSet {
			viper.Set(cookieDomainConfigKey, previous)
		} else {
			viper.Set(cookieDomainConfigKey, nil)
		}
	})
}

// issuedSessionCookie drives the real session-creation path and returns the cookie the
// client would receive. Both storages are covered, because the name has to be the same
// regardless of where the session is kept — an app that switches backends and finds itself
// reading a cookie under a different name than it writes would simply stop authenticating.
func issuedSessionCookie(t *testing.T, backend sessionBackend) *http.Cookie {
	t.Helper()

	strategy := strategyFor(backend)

	cookies := newStubCookieManager()
	_, err := strategy.NewSessionWithoutUser(contextWith(&stubMessageContext{cookies: cookies}))
	require.NoError(t, err)

	cookies.mu.Lock()
	defer cookies.mu.Unlock()
	require.Len(t, cookies.set, 1, "session creation writes exactly one cookie")

	return cookies.set[0]
}

// expiredSessionCookie drives Logout and returns the cookie that expires the session.
func expiredSessionCookie(t *testing.T, backend sessionBackend) *http.Cookie {
	t.Helper()

	strategy := strategyFor(backend)
	session := plantedSession(t, strategy)

	msgCtx, cookies := requestCarrying(session.GetId())
	require.NoError(t, strategy.Logout(contextWith(msgCtx)))

	cookies.mu.Lock()
	defer cookies.mu.Unlock()
	require.Len(t, cookies.set, 1, "logout writes exactly one cookie")

	return cookies.set[0]
}

// TestTheSessionCookieIsHostPrefixedByDefault.
//
// The name used to be a compile-time constant with no prefix and no configuration hook, and
// that is what made the finding unfixable by an app. A bare name is a token any host in the
// registrable domain can also write: `sibling.example.com` sets
// `GRG_SESSION_ID=chosen; Domain=example.com; Path=/login`, RFC 6265 orders the more
// specific path first, http.Request.Cookie returns the first match, and the framework reads
// the sibling's value — even though its own cookie is host-only. The `__Host-` prefix is the
// only thing that stops it: a browser refuses to let any other host set such a cookie.
func TestTheSessionCookieIsHostPrefixedByDefault(t *testing.T) {
	withCookieSecureConfig(t, false, false)
	withCookieDomainConfig(t, "")

	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			cookie := issuedSessionCookie(t, backend)

			assert.Equal(t, hostPrefixedCookieName, cookie.Name,
				"an app that configures nothing must get the prefix-locked cookie")
			assert.True(t, cookie.Secure, "a __Host- cookie is only accepted when Secure")
			assert.Equal(t, "/", cookie.Path, "a __Host- cookie is only accepted with Path=/")
			assert.Empty(t, cookie.Domain, "a __Host- cookie is only accepted without a Domain")
		})
	}
}

// TestTheSessionCookieFallsBackToTheBareNameWhenSecureIsDisabled. `secure: false` is the
// documented local-development opt-out, and a browser rejects a `__Host-` cookie that is
// not Secure outright — silently, with no error anywhere and no session, which is exactly
// the failure mode the cookie configuration comments were written to prevent.
func TestTheSessionCookieFallsBackToTheBareNameWhenSecureIsDisabled(t *testing.T) {
	withCookieSecureConfig(t, true, false)
	withCookieDomainConfig(t, "")

	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			cookie := issuedSessionCookie(t, backend)

			assert.Equal(t, bareCookieName, cookie.Name)
			assert.False(t, cookie.Secure)
			assert.Empty(t, cookie.Domain)
		})
	}
}

// TestTheSessionCookieFallsBackToTheBareNameWhenADomainIsConfigured. An app that shares its
// session across subdomains has asked for the thing `__Host-` forbids, so the prefix has to
// go — and it is the app's explicit opt-out, not a silent downgrade.
func TestTheSessionCookieFallsBackToTheBareNameWhenADomainIsConfigured(t *testing.T) {
	withCookieSecureConfig(t, false, false)
	withCookieDomainConfig(t, someParentCookieDomain)

	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			cookie := issuedSessionCookie(t, backend)

			assert.Equal(t, bareCookieName, cookie.Name)
			assert.Equal(t, someParentCookieDomain, cookie.Domain)
			assert.True(t, cookie.Secure, "the Secure default is unaffected by the Domain")
		})
	}
}

// TestNoSessionCookieEverPairsTheHostPrefixWithADomain is the invariant that has to hold for
// every configuration, on every cookie the framework writes. A `__Host-` cookie carrying a
// Domain is discarded by the browser without a word, so login fails with nothing to see in
// any log — the worst possible way to get this wrong.
func TestNoSessionCookieEverPairsTheHostPrefixWithADomain(t *testing.T) {
	for _, secure := range []bool{true, false} {
		for _, domain := range []string{"", someParentCookieDomain} {
			for name, cookieUnderTest := range map[string]func(*testing.T, sessionBackend) *http.Cookie{
				"issued":  issuedSessionCookie,
				"expired": expiredSessionCookie,
			} {
				withCookieSecureConfig(t, true, secure)
				withCookieDomainConfig(t, domain)

				for _, backend := range sessionBackends(t) {
					label := fmt.Sprintf("%s %s secure=%v domain=%q",
						name, backend.name, secure, domain)

					t.Run(label, func(t *testing.T) {
						cookie := cookieUnderTest(t, backend)

						if cookie.Name != hostPrefixedCookieName {
							return
						}

						assert.Empty(t, cookie.Domain,
							"a __Host- cookie must never carry a Domain (%s)", label)
						assert.True(t, cookie.Secure,
							"a __Host- cookie must always be Secure (%s)", label)
						assert.Equal(t, "/", cookie.Path)
					})
				}
			}
		}
	}
}

// TestTheNameWrittenIsTheNameRead. The name is resolved separately at every read and every
// write, so a disagreement between two of those call sites is not a degradation: the store
// never sees the identifier the browser holds, so nobody can log in and nothing reports an
// error. This drives a real second request with the cookie the first one emitted.
func TestTheNameWrittenIsTheNameRead(t *testing.T) {
	configurations := []struct {
		name   string
		secure bool
		domain string
	}{
		{name: "default", secure: true, domain: ""},
		{name: "insecure local dev", secure: false, domain: ""},
		{name: "shared parent domain", secure: true, domain: someParentCookieDomain},
	}

	for _, configuration := range configurations {
		t.Run(configuration.name, func(t *testing.T) {
			withCookieSecureConfig(t, true, configuration.secure)
			withCookieDomainConfig(t, configuration.domain)

			strategy := strategyFor(sessionBackends(t)[0])

			writer := newStubCookieManager()
			created, err := strategy.NewSessionWithoutUser(
				contextWith(&stubMessageContext{cookies: writer}))
			require.NoError(t, err)

			writer.mu.Lock()
			emitted := writer.set[0]
			writer.mu.Unlock()

			// The browser now sends back exactly what it was given.
			jar := newStubCookieManager()
			jar.present[emitted.Name] = emitted.Value

			resolved := strategy.CurrentSession(contextWith(&stubMessageContext{cookies: jar}))
			require.NotNil(t, resolved,
				"the session must be found under the name it was written with")
			assert.Equal(t, created.GetId(), resolved.GetId())
		})
	}
}
