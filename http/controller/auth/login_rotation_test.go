package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	gorganyauth "github.com/osbits/gorgany/v2/auth"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/http/middleware"
	"github.com/osbits/gorgany/v2/service"
	"github.com/osbits/gorgany/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file drives a login the way a browser does: one request, through the real session
// middleware, the real message and its session scope, the real standard strategy and a real
// session store. Nothing here is stubbed except the user lookup, the view engine and the two
// bits of routing the handler asks for.
//
// The reason it is worth the setup is that rotation is not finished when the strategy returns
// a new session. Two caches hold the session for the life of a request — the message's
// session scope, whose Get answers from s.current once it has resolved, and the message
// context, which ResolveSessionId consults *before* the cookie — and both were populated by
// the middleware before the handler ran. Left alone, everything the rest of the request asks
// is answered about the session that has just been deleted: message.Session().Get() hands
// back the old object and IsLoggedIn looks the old identifier up in a store that no longer
// has it, so a login that fully succeeded is indistinguishable from one that failed.

// e2eBackend is one of the two shipped session storages. It registers itself into the
// container rather than being handed over ready-made, because that is how an app gets one:
// the storage's own dependencies are container-injected, and a storage assembled outside the
// container has them overwritten the moment the container resolves it.
type e2eBackend struct {
	name string
	wire func(t *testing.T, container *service.Container) core.ISessionStorage
}

func e2eBackends() []e2eBackend {
	return []e2eBackend{
		{
			name: "memory",
			wire: func(t *testing.T, container *service.Container) core.ISessionStorage {
				storage := gorganyauth.NewMemorySession(time.Hour)
				require.NoError(t, container.Singleton(
					func() core.ISessionStorage { return storage }))
				require.NoError(t, container.Singleton(
					func() gorganyauth.ISessionFactory { return gorganyauth.NewMemorySessionFactory() }))
				return storage
			},
		},
		{
			name: "database",
			wire: func(t *testing.T, container *service.Container) core.ISessionStorage {
				mediator := gorganyauth.NewDbSessionMediator(newSessionRowSet())
				require.NoError(t, container.Singleton(
					func() *gorganyauth.DbSessionMediator { return mediator }))

				storage := gorganyauth.NewDbSessionStorage(time.Hour)
				require.NoError(t, container.Singleton(
					func() core.ISessionStorage { return storage }))

				factory := gorganyauth.NewDbSessionFactory()
				factory.SetMediator(mediator)
				require.NoError(t, container.Singleton(
					func() gorganyauth.ISessionFactory { return factory }))

				return storage
			},
		},
	}
}

// sessionRowSet stands in for the `sessions` table: FindById hands out a fresh struct every
// time, an UPDATE that matches no row reports success — that is what SQL does — and deleting
// an absent row is a no-op that reports it found nothing.
type sessionRowSet struct {
	rows map[string]*gorganyauth.DbSessionEntity
}

func newSessionRowSet() *sessionRowSet {
	return &sessionRowSet{rows: map[string]*gorganyauth.DbSessionEntity{}}
}

func (r *sessionRowSet) clone(row *gorganyauth.DbSessionEntity) *gorganyauth.DbSessionEntity {
	copied := &gorganyauth.DbSessionEntity{
		ID:           row.ID,
		UserID:       row.UserID,
		Expiry:       row.Expiry,
		CreatedAt:    row.CreatedAt,
		LastActivity: row.LastActivity,
	}
	copied.Meta = *row.GetMeta()
	copied.Meta.IsLoaded = true
	if row.Attributes != nil {
		attributes := make(gorganyauth.AttributesMap, len(row.Attributes))
		for key, value := range row.Attributes {
			attributes[key] = value
		}
		copied.Attributes = attributes
	}
	return copied
}

func (r *sessionRowSet) FindById(id string) (*gorganyauth.DbSessionEntity, error) {
	row, ok := r.rows[id]
	if !ok {
		return nil, nil
	}
	return r.clone(row), nil
}

func (r *sessionRowSet) Save(session *gorganyauth.DbSessionEntity) error {
	if _, exists := r.rows[session.GetId()]; !exists && session.GetMeta().IsLoaded {
		return nil
	}
	r.rows[session.GetId()] = r.clone(session)
	session.GetMeta().IsLoaded = true
	return nil
}

func (r *sessionRowSet) Delete(session *gorganyauth.DbSessionEntity) error {
	_, err := r.DeleteById(session.GetId())
	return err
}

func (r *sessionRowSet) DeleteById(id string) (bool, error) {
	_, existed := r.rows[id]
	delete(r.rows, id)
	return existed, nil
}

func (r *sessionRowSet) DeleteExpired() error {
	for id, row := range r.rows {
		if row.Expiry.Before(time.Now()) {
			delete(r.rows, id)
		}
	}
	return nil
}

var _ gorganyauth.ISessionRepository = (*sessionRowSet)(nil)

// stubRenderer satisfies the Message's view dependency. Login never renders.
type stubRenderer struct {
	core.IEngineRenderer
}

// e2eUserService answers both lookups a login makes: the credential check by username, and
// CurrentUser's by id.
type e2eUserService struct {
	core.IUserService
	user core.Authenticable
}

func (s *e2eUserService) Get(any) (core.Authenticable, error) { return s.user, nil }

func (s *e2eUserService) GetByUsername(string) (core.Authenticable, error) { return s.user, nil }

// loginPipeline is everything a login request needs, wired by the container — the same code
// path that builds these objects in a running app, including the unexported injected fields
// no test could otherwise reach.
type loginPipeline struct {
	strategy   *gorganyauth.StandardAuthStrategy
	storage    core.ISessionStorage
	middleware *middleware.SessionMiddleware
	controller LoginController
	container  *service.Container
}

func newLoginPipeline(t *testing.T, backend e2eBackend, user core.Authenticable) *loginPipeline {
	t.Helper()

	return newLoginPipelineWithUserService(t, backend, &e2eUserService{user: user})
}

// newLoginPipelineWithUserService is the same wiring for a directory holding more than one
// principal, which is what a donation scenario needs: the attacker and the victim have to be
// separate users with separate credentials, or a login "as the poster" cannot be told apart
// from a login as whoever was there before.
func newLoginPipelineWithUserService(
	t *testing.T, backend e2eBackend, userService core.IUserService) *loginPipeline {
	t.Helper()

	container := service.NewContainer()
	require.NoError(t, container.Singleton(func() *gorganyauth.CsrfService { return &gorganyauth.CsrfService{} }))
	require.NoError(t, container.Singleton(func() core.IUserService { return userService }))
	require.NoError(t, container.Singleton(func() core.IEngineRenderer { return &stubRenderer{} }))

	storage := backend.wire(t, container)

	strategy := &gorganyauth.StandardAuthStrategy{}
	require.NoError(t, container.Make(strategy))

	// Bound before the strategy is registered on it, because the container calls Init on
	// whatever it resolves and AuthContext.Init replaces the strategy map.
	authContext := &gorganyauth.AuthContext{}
	require.NoError(t, container.Singleton(func() core.IAuthContext { return authContext }))
	authContext.RegisterAuthStrategy(core.DefaultKeyInRegistrar, strategy)

	sessionMiddleware := middleware.NewSessionMiddleware()
	require.NoError(t, container.Make(sessionMiddleware))

	return &loginPipeline{
		strategy:   strategy,
		storage:    storage,
		middleware: sessionMiddleware,
		controller: LoginController{
			webContext:  &stubWebContext{home: "/home"},
			authContext: authContext,
			userService: userService,
			router:      &stubRouter{},
		},
		container: container,
	}
}

// newRequestMessage builds the real Message the router would build for this request.
func (p *loginPipeline) newRequestMessage(
	t *testing.T, method, target, body string, cookies ...*http.Cookie) (*grghttp.Message, *httptest.ResponseRecorder) {
	t.Helper()

	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	writer := httptest.NewRecorder()
	message := &grghttp.Message{}
	require.NoError(t, p.container.Make(message,
		map[string]interface{}{"writer": writer, "request": request}))

	return message, writer
}

// startSession runs one ordinary GET through the middleware, which is how a visitor gets a
// session and a cookie before ever seeing the login form.
func (p *loginPipeline) startSession(t *testing.T) *http.Cookie {
	t.Helper()

	message, writer := p.newRequestMessage(t, http.MethodGet, "/login", "")
	p.middleware.Handle(func(core.HttpMessage) {})(message)

	cookies := writer.Result().Cookies()
	require.Len(t, cookies, 1, "the middleware starts a session and writes its cookie")

	return cookies[0]
}

// TestLoginRotatesTheSessionEndToEnd.
//
// The victim arrives carrying a session identifier — in a fixation attack, one the attacker
// planted — and posts valid credentials. Afterwards the identifier must have changed, and
// the whole of the rest of that request has to agree about which session is current.
func TestLoginRotatesTheSessionEndToEnd(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			user := &stubUser{id: "victim-42", password: password}
			pipeline := newLoginPipeline(t, backend, user)

			presented := pipeline.startSession(t)

			message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login",
				"username=victim@example.com&password=correct-horse", presented)

			pipeline.middleware.Handle(pipeline.controller.Login)(message)

			require.Equal(t, "/home", writer.Result().Header.Get("Location"),
				"a successful login redirects home")

			current := message.Session().Get()
			require.NotNil(t, current, "the request must still have a session")
			assert.NotEqual(t, presented.Value, current.GetId(),
				"the identifier the client presented must not be the one it leaves with")
			assert.Equal(t, "victim-42", current.GetUserId(),
				"and the session the rest of the request sees must be the authenticated one")

			assert.True(t, pipeline.strategy.IsLoggedIn(message.Context()),
				"IsLoggedIn must see the rotated session in the same request that logged in")

			currentUser, err := pipeline.strategy.CurrentUser(message.Context())
			require.NoError(t, err)
			require.NotNil(t, currentUser, "CurrentUser must resolve through the rotated session")
			assert.Equal(t, "victim-42", currentUser.GetId())

			// What the browser is left holding.
			var issued *http.Cookie
			for _, cookie := range writer.Result().Cookies() {
				if cookie.Value == current.GetId() {
					issued = cookie
				}
			}
			require.NotNil(t, issued, "the rotated identifier has to be sent to the client")
			assert.Equal(t, presented.Name, issued.Name,
				"and under the same name, or the client's next request carries nothing")

			// And the identifier that arrived is dead.
			revoked, err := pipeline.storage.GetSessionById(presented.Value)
			require.NoError(t, err)
			assert.Nil(t, revoked, "the presented identifier must no longer resolve")
		})
	}
}

// TestASecondRequestOnTheRotatedCookieIsStillAuthenticated is the other half of the same
// property: rotation is only useful if the cookie the client was handed actually works on
// the next request. A republish that fixed the current request while writing a cookie for a
// session the store does not hold would pass the test above and log everybody out.
func TestASecondRequestOnTheRotatedCookieIsStillAuthenticated(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			user := &stubUser{id: "victim-42", password: password}
			pipeline := newLoginPipeline(t, backend, user)

			presented := pipeline.startSession(t)

			message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login",
				"username=victim@example.com&password=correct-horse", presented)
			pipeline.middleware.Handle(pipeline.controller.Login)(message)

			var issued *http.Cookie
			for _, cookie := range writer.Result().Cookies() {
				if cookie.MaxAge >= 0 && cookie.Value != "" {
					issued = cookie
				}
			}
			require.NotNil(t, issued)

			// The next page load, carrying only what the browser now has.
			next, _ := pipeline.newRequestMessage(t, http.MethodGet, "/home", "", issued)

			authenticated := false
			pipeline.middleware.Handle(func(m core.HttpMessage) {
				authenticated = pipeline.strategy.IsLoggedIn(m.Context())
			})(next)

			assert.True(t, authenticated,
				"the cookie the login handed out must authenticate the following request")
		})
	}
}

// TestTheOldCookieDoesNotAuthenticateAfterLogin. The attacker's browser keeps the identifier
// it planted, and keeps making requests with it. Those requests must be anonymous — and,
// because the session middleware starts a session for a request that has none, they must not
// be able to bring the planted identifier back either.
func TestTheOldCookieDoesNotAuthenticateAfterLogin(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			user := &stubUser{id: "victim-42", password: password}
			pipeline := newLoginPipeline(t, backend, user)

			planted := pipeline.startSession(t)

			message, _ := pipeline.newRequestMessage(t, http.MethodPost, "/login",
				"username=victim@example.com&password=correct-horse", planted)
			pipeline.middleware.Handle(pipeline.controller.Login)(message)

			attackerRequest, _ := pipeline.newRequestMessage(t, http.MethodGet, "/home", "", planted)

			authenticated := true
			var served core.ISession
			pipeline.middleware.Handle(func(m core.HttpMessage) {
				authenticated = pipeline.strategy.IsLoggedIn(m.Context())
				served = m.Session().Get()
			})(attackerRequest)

			assert.False(t, authenticated,
				"the identifier the victim arrived with must not authenticate as the victim")
			if served != nil {
				assert.NotEqual(t, planted.Value, served.GetId(),
					"and it must not be revived as a session either")
				assert.Empty(t, served.GetUserId())
			}
		})
	}
}
