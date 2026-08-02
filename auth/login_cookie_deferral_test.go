package auth

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Login used to hand the client its new identifier before it had finished. The cookie was
// written inside NewSessionWithoutUser — CookieManager writes straight into the live
// ResponseWriter, so once that ran the header was on the wire and there was no taking it back
// — and only then did Login set the user id, mint a CSRF token and persist. A failure in any
// of those returned an error with no rollback, so the store kept an authenticated row whose
// cookie the browser already had while the handler rendered "we could not sign you in". The
// next request was signed in.
//
// These are database-backed only, deliberately. StandardAuthStrategy holds a concrete
// *CsrfService rather than an interface, so the only failure injectable after the session
// exists is the repository's, and the in-memory store has no repository. The success-path
// fences below do run on both.

// failingLoginBackend is the database backend with a switchable repository failure.
func failingLoginBackend(t *testing.T) (*StandardAuthStrategy, *fakeSessionRepo) {
	t.Helper()

	repo := newFakeSessionRepo()
	storage := newDbStorage(repo)
	factory := NewDbSessionFactory()
	factory.SetMediator(storage.mediator)

	return &StandardAuthStrategy{
		sessionManager: storage,
		csrfService:    &CsrfService{},
		sessionFactory: factory,
		userService:    &stubUserService{},
	}, repo
}

// TestAFailedLoginWritesNoSessionCookie is the headline for SEC-H03.
func TestAFailedLoginWritesNoSessionCookie(t *testing.T) {
	strategy, repo := failingLoginBackend(t)

	cookies := newStubCookieManager()
	ctx := contextWith(&stubMessageContext{cookies: cookies})

	// The session is created, and then the store goes away before the login can finish.
	repo.failSaveAfter(1, errors.New("connection reset"))

	session, err := strategy.Login(&stubUser{id: "victim-42"}, ctx)

	require.Error(t, err, "the login did not complete and must not report success")
	assert.Nil(t, session)
	assert.Empty(t, cookiesNamed(cookies, SessionCookieName()),
		"a login that failed must not leave the browser holding the identifier it created")
}

// TestAFailedLoginLeavesNothingAuthenticatedInTheStore — the other half. Even if the client
// never sees the cookie, an authenticated row is a row somebody else's copy of the identifier
// could reach.
func TestAFailedLoginLeavesNothingAuthenticatedInTheStore(t *testing.T) {
	strategy, repo := failingLoginBackend(t)

	ctx := contextWith(&stubMessageContext{cookies: newStubCookieManager()})
	repo.failSaveAfter(1, errors.New("connection reset"))

	_, err := strategy.Login(&stubUser{id: "victim-42"}, ctx)
	require.Error(t, err)

	repo.mu.Lock()
	defer repo.mu.Unlock()
	for id, row := range repo.rows {
		assert.Empty(t, row.UserID,
			"row %s was left authenticated by a login that reported failure", id)
	}
}

// TestAFailedLoginRevokesTheSessionItHadAlreadyCreated states the rollback directly, at each
// point in the sequence where the row already exists.
//
// The two cases are different code paths, which is why both are here: an early failure is
// abandoned inside newSessionWithoutUser, which created the row; a later one is abandoned by
// Login, which by then has authenticated it.
func TestAFailedLoginRevokesTheSessionItHadAlreadyCreated(t *testing.T) {
	for _, failAfter := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("store fails after %d writes", failAfter), func(t *testing.T) {
			strategy, repo := failingLoginBackend(t)

			cookies := newStubCookieManager()
			ctx := contextWith(&stubMessageContext{cookies: cookies})
			repo.failSaveAfter(failAfter, errors.New("connection reset"))

			_, err := strategy.Login(&stubUser{id: "victim-42"}, ctx)
			require.Error(t, err)

			repo.mu.Lock()
			remaining := len(repo.rows)
			repo.mu.Unlock()
			assert.Zero(t, remaining,
				"the session the login created must not outlive the failure")
			assert.Empty(t, cookiesNamed(cookies, SessionCookieName()),
				"and no cookie may name it")
		})
	}
}

// TestASuccessfulLoginWritesExactlyOneCookieAndItNamesTheNewSession is the over-blocking
// fence, on both backends: deferring the write must not lose it.
func TestASuccessfulLoginWritesExactlyOneCookieAndItNamesTheNewSession(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)
			cookies := newStubCookieManager()
			ctx := contextWith(&stubMessageContext{cookies: cookies})

			session, err := strategy.Login(&stubUser{id: "user-7"}, ctx)
			require.NoError(t, err)
			require.NotNil(t, session)

			written := cookiesNamed(cookies, SessionCookieName())
			require.Len(t, written, 1, "exactly one session cookie should reach the client")
			assert.Equal(t, session.GetId(), written[0].Value,
				"and it must name the session the login actually authenticated")
		})
	}
}

// TestNewSessionWithoutUserStillWritesTheCookieItsCallersDependOn is the fence for the split.
// SessionMiddleware and CsrfController hand the session straight to the client and have
// nothing left that can fail, so for them the cookie still has to be written here.
func TestNewSessionWithoutUserStillWritesTheCookieItsCallersDependOn(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			cookies := newStubCookieManager()
			session, err := strategyFor(backend).NewSessionWithoutUser(
				contextWith(&stubMessageContext{cookies: cookies}))
			require.NoError(t, err)
			require.NotNil(t, session)

			written := cookiesNamed(cookies, SessionCookieName())
			require.Len(t, written, 1)
			assert.Equal(t, session.GetId(), written[0].Value)
		})
	}
}

// TestRotateSessionStillWritesItsOwnCookie — same fence for the other exported wrapper, which
// CurrentSession calls on the idle and age thresholds.
func TestRotateSessionStillWritesItsOwnCookie(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)
			old := plantedSession(t, strategy)

			cookies := newStubCookieManager()
			rotated, err := strategy.RotateSession(
				contextWith(&stubMessageContext{cookies: cookies}), old)
			require.NoError(t, err)
			require.NotNil(t, rotated)

			written := cookiesNamed(cookies, SessionCookieName())
			require.Len(t, written, 1)
			assert.Equal(t, rotated.GetId(), written[0].Value)
		})
	}
}

// TestAFailedLoginDoesNotAuthenticateASubsequentRequest is the acceptance criterion stated as
// the thing a user would actually notice.
func TestAFailedLoginDoesNotAuthenticateASubsequentRequest(t *testing.T) {
	strategy, repo := failingLoginBackend(t)

	cookies := newStubCookieManager()
	_, err := func() (any, error) {
		repo.failSaveAfter(1, errors.New("connection reset"))
		return strategy.Login(&stubUser{id: "victim-42"},
			contextWith(&stubMessageContext{cookies: cookies}))
	}()
	require.Error(t, err)
	repo.saveErr = nil

	// Whatever the browser is holding, the next request must not be signed in.
	for _, cookie := range cookies.set {
		msgCtx, _ := requestCarrying(cookie.Value)
		assert.False(t, strategy.IsLoggedIn(contextWith(msgCtx)),
			"a failed login authenticated the next request through cookie %q", cookie.Value)
	}
}
