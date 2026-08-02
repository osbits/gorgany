package auth

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	gorganyauth "github.com/osbits/gorgany/v2/auth"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/http/middleware"
	"github.com/osbits/gorgany/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEC-H12. The built-in browser login accepted an unprotected form POST, so an attacker could
// submit *their own* valid credentials from a page the victim visits: the victim's browser
// sends the request, the login succeeds, and the victim spends the visit inside the attacker's
// account while the attacker holds the other half of the session.
//
// Two things had to change together. The origin check, because the token kind of protection
// was not reachable — the framework ships no login template, there was no view helper that
// could emit the field, and CSRFMiddleware requires a session that SessionMiddleware does not
// guarantee on this route. And the shared form seam, because mounting *any* middleware that
// reads the form in front of a handler that reads the raw body broke the handler:
// net/http's ParseForm consumes the body and does not put it back.

// loginBody is the form a browser submits.
func loginBody(username, password string) string {
	return url.Values{"username": {username}, "password": {password}}.Encode()
}

// postLogin runs one login request through the origin check and the controller, the way the
// route config wires them.
func postLogin(
	t *testing.T, pipeline *loginPipeline, headers map[string]string,
	body string, cookies ...*http.Cookie) (*grghttp.Message, int) {
	t.Helper()

	message, writer := pipeline.newRequestMessage(t, http.MethodPost, "/login", body, cookies...)
	for key, value := range headers {
		message.Request().RawRequest().Header.Set(key, value)
	}

	handler := middleware.NewSameOriginMiddleware().Handle(pipeline.controller.Login)
	handler(message)

	return message, writer.Result().StatusCode
}

// TestACrossOriginLoginPostIsRejectedAndChangesNothing is the headline.
func TestACrossOriginLoginPostIsRejectedAndChangesNothing(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			attacker := &e2eUser{id: "attacker-1", username: "attacker", password: password}
			pipeline := newLoginPipeline(t, backend, attacker)

			presented := pipeline.startSession(t)

			_, status := postLogin(t, pipeline,
				map[string]string{"Origin": "https://evil.example"},
				loginBody("attacker", "correct-horse"), presented)

			assert.Equal(t, http.StatusForbidden, status,
				"a login posted from another origin must be refused")

			// And nothing about the authentication state moved.
			session, err := pipeline.storage.GetSessionById(presented.Value)
			require.NoError(t, err)
			require.NotNil(t, session, "the presented session must still be the current one")
			assert.Empty(t, session.GetUserId(), "and must not have been authenticated")
		})
	}
}

// TestASameOriginFormLoginStillSucceeds is the over-blocking fence — the check has to refuse
// the attack without refusing the ordinary case.
func TestASameOriginFormLoginStillSucceeds(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			user := &e2eUser{id: "user-1", username: "alice", password: password}
			pipeline := newLoginPipeline(t, backend, user)

			presented := pipeline.startSession(t)

			message, _ := postLogin(t, pipeline,
				map[string]string{"Origin": "http://example.com"},
				loginBody("alice", "correct-horse"), presented)

			// The harness's requests are addressed to example.com, so that Origin is this one.
			current := message.Session().Get()
			require.NotNil(t, current, "the login must have published a session")
			assert.Equal(t, "user-1", current.GetUserId())
			assert.NotEqual(t, presented.Value, current.GetId(), "and rotated the identifier")
		})
	}
}

// TestSecFetchSiteDecidesBeforeOriginDoes. Sec-Fetch-Site is a forbidden header name — script
// cannot set it — so it is the most trustworthy of the three signals and is consulted first.
func TestSecFetchSiteDecidesBeforeOriginDoes(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	cases := map[string]struct {
		fetchSite string
		refused   bool
	}{
		"same-origin is allowed":        {"same-origin", false},
		"none is allowed":               {"none", false},
		"cross-site is refused":         {"cross-site", true},
		"same-site is refused by default": {"same-site", true},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			user := &e2eUser{id: "user-1", username: "alice", password: password}
			pipeline := newLoginPipeline(t, e2eBackends()[0], user)
			presented := pipeline.startSession(t)

			_, status := postLogin(t, pipeline,
				map[string]string{"Sec-Fetch-Site": testCase.fetchSite},
				loginBody("alice", "correct-horse"), presented)

			if testCase.refused {
				assert.Equal(t, http.StatusForbidden, status)
			} else {
				assert.NotEqual(t, http.StatusForbidden, status)
			}
		})
	}
}

// TestALoginPostWithNoOriginOrRefererFollowsThePolicy.
//
// Allowed by default: a browser cannot be made to omit all three signals on a cross-site form
// POST, so the all-absent case is a non-browser client and refusing it by default would break
// those for no gain. RequireOriginHeader is for a browser-only endpoint.
func TestALoginPostWithNoOriginOrRefererFollowsThePolicy(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	t.Run("allowed by default", func(t *testing.T) {
		user := &e2eUser{id: "user-1", username: "alice", password: password}
		pipeline := newLoginPipeline(t, e2eBackends()[0], user)
		presented := pipeline.startSession(t)

		_, status := postLogin(t, pipeline, nil, loginBody("alice", "correct-horse"), presented)
		assert.NotEqual(t, http.StatusForbidden, status)
	})

	t.Run("refused when required", func(t *testing.T) {
		user := &e2eUser{id: "user-1", username: "alice", password: password}
		pipeline := newLoginPipeline(t, e2eBackends()[0], user)
		presented := pipeline.startSession(t)

		message, writer := pipeline.newRequestMessage(
			t, http.MethodPost, "/login", loginBody("alice", "correct-horse"), presented)

		strict := middleware.NewSameOriginMiddlewareWith(
			middleware.SameOriginOptions{RequireOriginHeader: true})
		strict.Handle(pipeline.controller.Login)(message)

		assert.Equal(t, http.StatusForbidden, writer.Result().StatusCode)
	})
}

// TestCredentialsSurviveTheCsrfMiddlewaresFormParse is the trap test, and the reason "just
// mount the middleware" was not the fix.
//
// CSRFMiddleware called req.ParseForm to find the token; ParseForm reads the body and does not
// put it back; the login handler then read the raw body and found nothing, so every login
// failed with "we were unable to find a user". Both go through the shared seam now.
func TestCredentialsSurviveTheCsrfMiddlewaresFormParse(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			user := &e2eUser{id: "user-1", username: "alice", password: password}
			pipeline := newLoginPipeline(t, backend, user)

			presented := pipeline.startSession(t)

			// The token the session actually holds, which is what a rendered form would carry.
			session, err := pipeline.storage.GetSessionById(presented.Value)
			require.NoError(t, err)
			require.NotNil(t, session)
			token := session.GetItem(core.CSRFSessionKey)
			require.NotEmpty(t, token)

			body := url.Values{
				core.CSRFFormFieldName: {token},
				"username":             {"alice"},
				"password":             {"correct-horse"},
			}.Encode()

			message, _ := pipeline.newRequestMessage(
				t, http.MethodPost, "/login", body, presented)
			message.Request().RawRequest().Header.Set("Origin", "http://example.com")

			csrf := middleware.NewCSRFMiddleware()
			require.NoError(t, pipeline.container.Make(csrf))

			handler := csrf.Handle(
				middleware.NewSameOriginMiddleware().Handle(pipeline.controller.Login))
			handler(message)

			current := message.Session().Get()
			require.NotNil(t, current,
				"the CSRF middleware's form parse must not have consumed the credentials")
			assert.Equal(t, "user-1", current.GetUserId())
		})
	}
}

// TestTheFormIsReadableTwice states the seam's property directly.
func TestTheFormIsReadableTwice(t *testing.T) {
	pipeline := newLoginPipeline(t, e2eBackends()[0],
		&e2eUser{id: "user-1", username: "alice", password: "x"})

	body := loginBody("alice", "correct-horse")
	message, _ := pipeline.newRequestMessage(t, http.MethodPost, "/login", body)

	first, err := grghttp.PostFormValues(message)
	require.NoError(t, err)
	assert.Equal(t, "alice", first.Get("username"))

	// Somebody else drains the body the way net/http does.
	require.NoError(t, message.Request().RawRequest().ParseForm())

	second, err := grghttp.PostFormValues(message)
	require.NoError(t, err)
	assert.Equal(t, "alice", second.Get("username"))

	raw, err := message.Request().Body()
	require.NoError(t, err)
	assert.Contains(t, string(raw), "alice",
		"and the raw body is still there for a handler that wants it")
}

// TestLogoutIsProtectedAndStillRevokesThePresentedSession covers the fourth acceptance
// criterion: a forced logout is a real, if smaller, forgery.
func TestLogoutIsProtectedAndStillRevokesThePresentedSession(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			user := &e2eUser{id: "user-1", username: "alice", password: password}
			pipeline := newLoginPipeline(t, backend, user)
			presented := pipeline.startSession(t)

			handler := middleware.NewSameOriginMiddleware().Handle(pipeline.controller.Logout)

			// Cross-origin: refused, and the session survives.
			crossOrigin, writer := pipeline.newRequestMessage(
				t, http.MethodPost, "/logout", "", presented)
			crossOrigin.Request().RawRequest().Header.Set("Origin", "https://evil.example")
			handler(crossOrigin)

			assert.Equal(t, http.StatusForbidden, writer.Result().StatusCode)
			session, err := pipeline.storage.GetSessionById(presented.Value)
			require.NoError(t, err)
			assert.NotNil(t, session, "a refused logout must leave the session alone")

			// Same-origin: allowed, and the session is gone.
			sameOrigin, _ := pipeline.newRequestMessage(
				t, http.MethodPost, "/logout", "", presented)
			sameOrigin.Request().RawRequest().Header.Set("Origin", "http://example.com")
			handler(sameOrigin)

			session, err = pipeline.storage.GetSessionById(presented.Value)
			require.NoError(t, err)
			assert.Nil(t, session, "the intended session must actually be revoked")
		})
	}
}

// TestTheBuiltInLoginRoutesCarryTheOriginCheck. The protection has to be on the route config,
// not only reachable — an app mounts this controller and gets it.
func TestTheBuiltInLoginRoutesCarryTheOriginCheck(t *testing.T) {
	controller := LoginController{}

	protected := map[string]bool{}
	for _, route := range controller.GetRoutes() {
		if route.GetMethod() != core.POST {
			continue
		}
		for _, mw := range route.GetMiddlewares() {
			if _, ok := mw.(*middleware.SameOriginMiddleware); ok {
				protected[route.GetPath()] = true
			}
		}
	}

	assert.True(t, protected["/login"], "POST /login must carry the origin check")
	assert.True(t, protected["/logout"], "POST /logout must carry the origin check")
}

// e2eUser is a principal with a real hashed password.
type e2eUser struct {
	id       string
	username string
	password string
}

func (u *e2eUser) GetId() string          { return u.id }
func (u *e2eUser) GetUsername() string    { return u.username }
func (u *e2eUser) GetPassword() string    { return u.password }
func (u *e2eUser) GetRole() core.UserRole { return core.UserRole("user") }

var _ core.Authenticable = (*e2eUser)(nil)
var _ = gorganyauth.SessionCookieName
