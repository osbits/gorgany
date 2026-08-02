package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests here are about the *other* rotation, the one nobody asked for. CurrentSession
// rotates a session on its own once it is idle past the activity timeout or older than the
// rotation interval, and it does that from inside whatever first resolved the session — which
// on a normal request is the session middleware. The two caches a request keeps (the session
// scope's s.current and the message context the strategy consults before the cookie) were
// filled in by that first resolution, so they name the identifier the rotation has just
// deleted, and nothing tells them otherwise.

// idleBeyondTheActivityTimeout backdates a session's activity stamp far enough that the next
// request rotates it, and puts it back in the store.
func idleBeyondTheActivityTimeout(t *testing.T, pipeline *loginPipeline, id string) {
	t.Helper()

	stored, err := pipeline.storage.GetSessionById(id)
	require.NoError(t, err)
	require.NotNil(t, stored)

	stored.SetLastActivity(
		time.Now().Add(-pipeline.storage.GetSessionActivityTimeout() - time.Minute))
	require.NoError(t, pipeline.storage.AddSession(stored))
}

// loggedInCookie runs a full login and returns the cookie the browser is left holding.
func loggedInCookie(t *testing.T, pipeline *loginPipeline, presented *http.Cookie) *http.Cookie {
	t.Helper()

	message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login",
		"username=victim@example.com&password=correct-horse", presented)
	pipeline.middleware.Handle(pipeline.controller.Login)(message)

	live := lastSessionCookie(t, writer.Result().Cookies())
	require.NotNil(t, live, "a login has to leave the client with a session cookie")

	return live
}

// lastSessionCookie is what a cookie jar keeps: the last Set-Cookie for the name wins.
func lastSessionCookie(t *testing.T, cookies []*http.Cookie) *http.Cookie {
	t.Helper()

	var found *http.Cookie
	for _, cookie := range cookies {
		if cookie.MaxAge >= 0 && cookie.Value != "" {
			found = cookie
		}
	}
	return found
}

// TestAnIdleRotationLeavesTheRequestAuthenticated.
//
// The user comes back to the tab after lunch. Their session is past the activity timeout, so
// the first request rotates it — and that request must still be their request. It used to be
// anonymous: the session middleware resolved the session (which rotated it), left both of the
// request's caches naming the deleted identifier, and every handler after it asked about a
// session the store no longer had. The visible result is one spurious trip through the login
// page per idle period, on a request that carried a perfectly good cookie.
func TestAnIdleRotationLeavesTheRequestAuthenticated(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			user := &stubUser{id: "victim-42", password: password}
			pipeline := newLoginPipeline(t, backend, user)

			live := loggedInCookie(t, pipeline, pipeline.startSession(t))
			idleBeyondTheActivityTimeout(t, pipeline, live.Value)

			request, _ := pipeline.newRequestMessage(t, http.MethodGet, "/home", "", live)

			var seen core.ISession
			var authenticated bool
			var currentUser core.Authenticable
			pipeline.middleware.Handle(func(m core.HttpMessage) {
				seen = m.Session().Get()
				authenticated = pipeline.strategy.IsLoggedIn(m.Context())
				currentUser, _ = pipeline.strategy.CurrentUser(m.Context())
			})(request)

			require.NotNil(t, seen, "the request must still have a session")

			held, err := pipeline.storage.GetSessionById(seen.GetId())
			require.NoError(t, err)
			assert.NotNil(t, held,
				"the session the request is handed has to be one the store actually holds")

			assert.True(t, authenticated,
				"the request the rotation happened on must still be authenticated")
			require.NotNil(t, currentUser,
				"and CurrentUser must resolve through the rotated identifier")
			assert.Equal(t, "victim-42", currentUser.GetId())
		})
	}
}

// TestARotatingRequestLeavesNoSessionBehind.
//
// Every identifier a rotation abandons is deleted by the rotation that abandons it, so a
// response that wrote several session cookies must leave exactly one of them resolvable. It
// did not: because the request's caches still named the first rotation's output, the next
// resolution inside the same request looked up an identifier that had been deleted, found
// nothing, and started a *fresh* session instead of rotating the live one — so the live one
// was never revoked and stayed in the store, unreferenced, until it expired.
func TestARotatingRequestLeavesNoSessionBehind(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			user := &stubUser{id: "victim-42", password: password}
			pipeline := newLoginPipeline(t, backend, user)

			// The visitor opened the login form, walked away past the activity timeout, and
			// then submitted it — so this one request both rotates and authenticates.
			presented := pipeline.startSession(t)
			idleBeyondTheActivityTimeout(t, pipeline, presented.Value)

			message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login",
				"username=victim@example.com&password=correct-horse", presented)
			pipeline.middleware.Handle(pipeline.controller.Login)(message)

			cookies := writer.Result().Cookies()
			require.NotEmpty(t, cookies)

			live := lastSessionCookie(t, cookies)
			require.NotNil(t, live)

			for _, cookie := range cookies {
				held, err := pipeline.storage.GetSessionById(cookie.Value)
				require.NoError(t, err)
				if cookie.Value == live.Value {
					assert.NotNil(t, held,
						"the identifier the client is left with must be in the store")
					continue
				}
				assert.Nil(t, held,
					"an identifier the client no longer holds must not be left in the store: "+
						"%s", cookie.Value)
			}
		})
	}
}

// TestTheCsrfTokenHeaderNamesTheSessionTheClientLeavesWith.
//
// X-CSRF-Token is published by the session middleware before the handler runs, so on a
// request whose handler replaces the session the header is written against the wrong one
// unless somebody corrects it. A client that reads the token off the response that logged it
// in — which is the documented contract — then has a token its session does not hold, and
// every mutating request it makes is rejected.
func TestTheCsrfTokenHeaderNamesTheSessionTheClientLeavesWith(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		for _, idle := range []bool{false, true} {
			name := backend.name
			if idle {
				name += "/after the activity timeout"
			}

			t.Run(name, func(t *testing.T) {
				user := &stubUser{id: "victim-42", password: password}
				pipeline := newLoginPipeline(t, backend, user)

				presented := pipeline.startSession(t)
				if idle {
					idleBeyondTheActivityTimeout(t, pipeline, presented.Value)
				}

				message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login",
					"username=victim@example.com&password=correct-horse", presented)
				pipeline.middleware.Handle(pipeline.controller.Login)(message)

				final := message.Session().Get()
				require.NotNil(t, final)

				published := writer.Result().Header.Get(core.CSRFTokenHeader)
				require.NotEmpty(t, published, "a session response carries the token header")

				stored, err := pipeline.storage.GetSessionById(final.GetId())
				require.NoError(t, err)
				require.NotNil(t, stored)

				assert.Equal(t, stored.GetItem(core.CSRFSessionKey), published,
					"the token on the response must be the one the client's session holds")
			})
		}
	}
}
