package auth

import (
	"net/http"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests below drive the real storages rather than a mock, because the property under
// test is what the *store* holds after a login: an assertion that Login called some method
// would pass against a rotation that never took effect.

// sessionBackend is one of the two shipped session storages, wired the way the container
// wires it: the storage, and the factory that mints sessions for it.
type sessionBackend struct {
	name    string
	storage core.ISessionStorage
	factory ISessionFactory
}

func sessionBackends(t *testing.T) []sessionBackend {
	t.Helper()

	database := newDbStorage(newFakeSessionRepo())
	dbFactory := NewDbSessionFactory()
	dbFactory.SetMediator(database.mediator)

	return []sessionBackend{
		{name: "memory", storage: NewMemorySession(time.Hour), factory: NewMemorySessionFactory()},
		{name: "database", storage: database, factory: dbFactory},
	}
}

func strategyFor(backend sessionBackend) *StandardAuthStrategy {
	return &StandardAuthStrategy{
		sessionManager: backend.storage,
		csrfService:    &CsrfService{},
		sessionFactory: backend.factory,
		userService:    &stubUserService{},
	}
}

// stubUserService answers CurrentUser with one user, whoever asks.
type stubUserService struct {
	user core.Authenticable
}

func (s *stubUserService) Get(any) (core.Authenticable, error) { return s.user, nil }

func (s *stubUserService) GetByUsername(string) (core.Authenticable, error) { return s.user, nil }

func (s *stubUserService) Save(core.Authenticable) error { return nil }

// plantedSession is a session the client presented — the attacker's contribution to a
// fixation attack. It is created through the strategy so that it is indistinguishable from
// one the session middleware would have started: stored, cookie written, CSRF token issued.
func plantedSession(t *testing.T, strategy *StandardAuthStrategy) core.ISession {
	t.Helper()

	session, err := strategy.NewSessionWithoutUser(
		contextWith(&stubMessageContext{cookies: newStubCookieManager()}))
	require.NoError(t, err)
	require.NotNil(t, session)

	return session
}

// requestCarrying is a fresh request presenting one session id and nothing else. The
// message context deliberately holds no session, so ResolveSessionId has to go to the
// cookie — which is how a request from another browser reaches the store.
func requestCarrying(id string) (*stubMessageContext, *stubCookieManager) {
	cookies := newStubCookieManager()
	cookies.present[SessionCookieName()] = id
	return &stubMessageContext{cookies: cookies}, cookies
}

// cookiesNamed returns every cookie the manager was asked to write under this name.
func cookiesNamed(cookies *stubCookieManager, name string) []*http.Cookie {
	cookies.mu.Lock()
	defer cookies.mu.Unlock()

	var matched []*http.Cookie
	for _, cookie := range cookies.set {
		if cookie.Name == name {
			matched = append(matched, cookie)
		}
	}
	return matched
}

// TestLoginRotatesASessionTheClientPresented is the headline.
//
// Login used to take whatever CurrentSession returned and assign the authenticated user id
// to it, so the identifier that authenticates after a login was one the client chose. An
// attacker who can get a session id into the victim's browser — a cookie written by a
// sibling subdomain, an XSS, a shared machine before login — keeps that id authenticating
// as the victim from the moment the victim signs in.
func TestLoginRotatesASessionTheClientPresented(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)

			plantedId := plantedSession(t, strategy).GetId()

			msgCtx, cookies := requestCarrying(plantedId)
			session, err := strategy.Login(&stubUser{id: "victim-42"}, contextWith(msgCtx))
			require.NoError(t, err)
			require.NotNil(t, session)

			assert.NotEqual(t, plantedId, session.GetId(),
				"the identifier that authenticates must not be the one the client presented")
			assert.Equal(t, "victim-42", session.GetUserId())

			stored := sessionById(t, backend.storage, session.GetId())
			require.NotNil(t, stored, "the rotated session has to be in the store")
			assert.Equal(t, "victim-42", stored.GetUserId())

			// The new identifier is of no use unless the browser is told about it.
			var values []string
			for _, cookie := range cookiesNamed(cookies, SessionCookieName()) {
				values = append(values, cookie.Value)
			}
			assert.Contains(t, values, session.GetId(),
				"login must write the rotated identifier to the client")
		})
	}
}

// TestThePreLoginSessionIdStopsAuthenticating covers the other half of fixation: rotating
// is pointless if the identifier the attacker still holds keeps working.
func TestThePreLoginSessionIdStopsAuthenticating(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)
			strategy.userService = &stubUserService{user: &stubUser{id: "victim-42"}}

			plantedId := plantedSession(t, strategy).GetId()

			msgCtx, _ := requestCarrying(plantedId)
			_, err := strategy.Login(&stubUser{id: "victim-42"}, contextWith(msgCtx))
			require.NoError(t, err)

			// A request from the attacker's browser, which still holds the planted id.
			attackerCtx, _ := requestCarrying(plantedId)

			assert.Nil(t, sessionById(t, backend.storage, plantedId),
				"the presented identifier must be gone from the store")
			assert.False(t, strategy.IsLoggedIn(contextWith(attackerCtx)),
				"the presented identifier must not authenticate after the login")

			user, err := strategy.CurrentUser(contextWith(attackerCtx))
			require.NoError(t, err)
			assert.Nil(t, user, "and it must not resolve to the user who logged in")
		})
	}
}

// TestAConcurrentRequestCannotRestoreThePreLoginSessionId.
//
// The attacker's own request is the concurrent one: it resolved the planted session before
// the victim logged in and holds the object, and every request writes the session back —
// SessionMiddleware slides the expiry and the activity stamp on all of them. If that write
// can recreate the session, rotation buys nothing, because the identifier the attacker
// holds comes back carrying the user id the login just assigned.
func TestAConcurrentRequestCannotRestoreThePreLoginSessionId(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)

			plantedId := plantedSession(t, strategy).GetId()

			// The attacker's in-flight request holds the session object.
			inFlight := sessionById(t, backend.storage, plantedId)
			require.NotNil(t, inFlight)

			msgCtx, _ := requestCarrying(plantedId)
			_, err := strategy.Login(&stubUser{id: "victim-42"}, contextWith(msgCtx))
			require.NoError(t, err)

			// ... and finishes afterwards, writing back what it holds.
			inFlight.SetLastActivity(time.Now())
			assert.Error(t, backend.storage.AddSession(inFlight),
				"a session the login revoked must not be storable again")

			assert.Nil(t, sessionById(t, backend.storage, plantedId),
				"the pre-login identifier must stay revoked")
		})
	}
}

// TestAPostLoginRotationDoesNotHandTheOldCookieAnAuthenticatedSession.
//
// This is the reason the rotation could not be left to the idle threshold. RotateSession
// carries the user id onto the new identifier, so on a session that is already authenticated
// whichever party crosses the threshold first keeps the identity and evicts the other. If
// that party is the attacker — and it is whoever's next request lands first — they receive a
// brand new identifier still authenticated as the victim, in their own cookie jar, while the
// victim's session is deleted: hijack plus a logout for the victim. Rotating at the
// authentication boundary is what removes the shared identifier that makes it possible.
func TestAPostLoginRotationDoesNotHandTheOldCookieAnAuthenticatedSession(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)

			planted := plantedSession(t, strategy)
			plantedId := planted.GetId()

			msgCtx, _ := requestCarrying(plantedId)
			_, err := strategy.Login(&stubUser{id: "victim-42"}, contextWith(msgCtx))
			require.NoError(t, err)

			// The attacker's browser goes quiet for longer than the activity timeout and then
			// comes back, still presenting the identifier it planted.
			if stale := sessionById(t, backend.storage, plantedId); stale != nil {
				stale.SetLastActivity(
					time.Now().Add(-backend.storage.GetSessionActivityTimeout() - time.Minute))
			}

			attackerCtx, attackerCookies := requestCarrying(plantedId)
			resolved := strategy.CurrentSession(contextWith(attackerCtx))

			if resolved != nil {
				assert.Empty(t, resolved.GetUserId(),
					"a rotation must not carry the victim's identity onto an identifier "+
						"presented by somebody who was never authenticated")
			}
			assert.Empty(t, cookiesNamed(attackerCookies, SessionCookieName()),
				"and nothing may be written to that client's jar off the back of it")
		})
	}
}

// TestTheCsrfTokenDoesNotSurviveTheAuthenticationBoundary.
//
// A rotation carries the CSRF token onto the new identifier so that an already-rendered form
// keeps working, and that is right for the rotations that happen on their own — the activity
// timeout and the 24-hour age limit — because no privilege boundary is crossed there and the
// client has no way to learn that anything moved. A login is neither of those things. Every
// secret the presented session held was chosen by whoever presented it, and the token is the
// one that keeps its value after the identifier has been replaced.
//
// Concretely, for an app that opted out of the __Host- cookie name by configuring
// auth.session.cookie.domain: a sibling of the registrable domain plants its own session in
// the victim's browser, whose token it knows because it asked /csrf for it. The login moves the
// victim onto an identifier the sibling cannot guess — but a sibling is same-site, so
// SameSite=Lax sends the victim's cookie on a POST the sibling makes, and if the token had
// crossed the login it would be the only thing between that POST and an authenticated state
// change.
//
// The client is not left holding a dead token: http.PublishSession rewrites X-CSRF-Token from
// the session the login installed, which the end-to-end tests in http/controller/auth pin.
func TestTheCsrfTokenDoesNotSurviveTheAuthenticationBoundary(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)

			planted := plantedSession(t, strategy)
			plantedToken := planted.GetItem(core.CSRFSessionKey)
			require.NotEmpty(t, plantedToken, "the pre-login session carries a token")

			msgCtx, _ := requestCarrying(planted.GetId())
			session, err := strategy.Login(&stubUser{id: "victim-42"}, contextWith(msgCtx))
			require.NoError(t, err)
			require.NotNil(t, session)

			assert.False(t,
				strategy.csrfService.ValidateCSRFToken(contextWith(msgCtx), session, plantedToken),
				"a token that existed before the login must not validate a request made after it")

			stored := sessionById(t, backend.storage, session.GetId())
			require.NotNil(t, stored)
			assert.NotEqual(t, plantedToken, stored.GetItem(core.CSRFSessionKey),
				"and the store must not be holding the pre-login token either")
			assert.NotEmpty(t, stored.GetItem(core.CSRFSessionKey),
				"the authenticated session still needs a token of its own")
		})
	}
}

// TestTheCsrfTokenSurvivesAnIdleRotation is the counterpart, on the rotation path that has
// always existed: a session idle past the activity timeout is rotated on its owner's next
// request, and the client holding the token it was issued minutes earlier has no way to know.
// Every mutating request it makes would be rejected until it fetched a new one. Nothing about
// the login boundary above licenses dropping it here.
func TestTheCsrfTokenSurvivesAnIdleRotation(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)

			session := plantedSession(t, strategy)
			issuedToken := session.GetItem(core.CSRFSessionKey)
			require.NotEmpty(t, issuedToken)

			session.SetLastActivity(
				time.Now().Add(-backend.storage.GetSessionActivityTimeout() - time.Minute))

			msgCtx, _ := requestCarrying(session.GetId())
			rotated := strategy.CurrentSession(contextWith(msgCtx))
			require.NotNil(t, rotated, "an idle session is rotated, not dropped")
			require.NotEqual(t, session.GetId(), rotated.GetId(), "this is the rotation path")

			assert.Equal(t, issuedToken, rotated.GetItem(core.CSRFSessionKey),
				"a rotation must not invalidate the token an already-rendered form carries")
		})
	}
}

// TestLoginCarriesNoOtherPreAuthenticationState. The CSRF token crosses the rotation
// deliberately and by name; nothing else a pre-authentication session accumulated does,
// because an attacker who planted the session also chose its contents.
func TestLoginCarriesNoOtherPreAuthenticationState(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			strategy := strategyFor(backend)

			planted := plantedSession(t, strategy)
			planted.SetItem("attacker_chosen", "value")

			msgCtx, _ := requestCarrying(planted.GetId())
			session, err := strategy.Login(&stubUser{id: "victim-42"}, contextWith(msgCtx))
			require.NoError(t, err)

			assert.Empty(t, session.GetItem("attacker_chosen"),
				"pre-authentication attributes must not survive the authentication boundary")
		})
	}
}

// TestLoginFailsClosedWhenTheOldSessionCannotBeRevoked. A login that could not get rid of
// the identifier the client presented must not report success: both identifiers would then
// authenticate as the user, which is the fixation the rotation exists to prevent.
func TestLoginFailsClosedWhenTheOldSessionCannotBeRevoked(t *testing.T) {
	repo := newFakeSessionRepo()
	storage := newDbStorage(repo)
	factory := NewDbSessionFactory()
	factory.SetMediator(storage.mediator)

	strategy := strategyFor(sessionBackend{storage: storage, factory: factory})

	plantedId := plantedSession(t, strategy).GetId()

	repo.deleteErr = assert.AnError

	msgCtx, _ := requestCarrying(plantedId)
	session, err := strategy.Login(&stubUser{id: "victim-42"}, contextWith(msgCtx))

	require.Error(t, err, "a login that could not revoke the presented identifier is not a login")
	assert.Nil(t, session)
}
