package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The doubles embed the interface they stand in for, so a handler reaching for
// anything these tests do not model fails with a nil-method panic rather than being
// silently accommodated.

type stubStrategy struct {
	core.IAuthStrategy

	loggedIn   bool
	loginCalls int
	loginUsers []string

	// session is what Login hands back — after rotation, never the one that came in.
	session core.ISession
}

func (s *stubStrategy) IsLoggedIn(context.Context) bool { return s.loggedIn }

func (s *stubStrategy) Login(user core.Authenticable, _ context.Context) (core.ISession, error) {
	s.loginCalls++
	s.loginUsers = append(s.loginUsers, user.GetId())
	if s.session != nil {
		return s.session, nil
	}
	return &stubSession{}, nil
}

type stubSession struct {
	core.ISession

	// token is the session's CSRF token. Publishing a session rewrites the response's
	// X-CSRF-Token from it, so the double has to answer for it; the empty default is a
	// session that has none yet, which the publish leaves alone.
	token string
}

func (s *stubSession) GetItem(string) string { return s.token }

// stubSessionScope is the request's session scope. The handler writes to it, because a login
// replaces the session and the rest of the request has to be told which one is current.
type stubSessionScope struct {
	core.IEditableSessionScope

	current core.ISession
}

func (s *stubSessionScope) Get() core.ISession  { return s.current }
func (s *stubSessionScope) Set(v core.ISession) { s.current = v }

type stubAuthContext struct {
	core.IAuthContext
	strategy core.IAuthStrategy
}

func (c *stubAuthContext) ResolveAuthStrategyByContext(context.Context) core.IAuthStrategy {
	return c.strategy
}

type stubWebContext struct {
	core.IWebContext
	home string
}

func (c *stubWebContext) GetHomeUrl() string { return c.home }

type stubRouter struct {
	core.Router
}

func (r *stubRouter) UrlByNameSequence(name string, _ ...any) string { return "/" + name }

type stubUser struct {
	id       string
	password string
}

func (u *stubUser) GetId() string          { return u.id }
func (u *stubUser) GetUsername() string    { return "victim@example.com" }
func (u *stubUser) GetPassword() string    { return u.password }
func (u *stubUser) GetRole() core.UserRole { return core.UserRole("user") }

type stubUserService struct {
	core.IUserService
	user core.Authenticable
}

func (s *stubUserService) GetByUsername(string) (core.Authenticable, error) {
	return s.user, nil
}

type recorded struct {
	redirects []string
	rendered  []string
	flashes   []map[string]any
}

type stubResponse struct {
	core.IResponseScope
	rec *recorded
}

func (r *stubResponse) Redirect(url string, _ int) {
	r.rec.redirects = append(r.rec.redirects, url)
}

func (r *stubResponse) Header() http.Header { return http.Header{} }

type stubRequest struct {
	core.IRequestScope
	body []byte
}

func (r *stubRequest) Body() ([]byte, error) { return r.body, nil }

type stubView struct {
	core.IViewScope
	rec *recorded
}

func (v *stubView) Render(tpl string, _ map[string]any) {
	v.rec.rendered = append(v.rec.rendered, tpl)
}

type stubMessage struct {
	core.HttpMessage
	req *stubRequest
	res *stubResponse
	vw  *stubView
	ses *stubSessionScope
	rec *recorded
}

// RedirectWithFlash records both halves, because a failed login is only useful if the caller
// is told why.
func (m *stubMessage) RedirectWithFlash(url string, _ int, data map[string]any) {
	m.rec.redirects = append(m.rec.redirects, url)
	m.rec.flashes = append(m.rec.flashes, data)
}

func (m *stubMessage) Request() core.IRequestScope   { return m.req }
func (m *stubMessage) Response() core.IResponseScope { return m.res }
func (m *stubMessage) View() core.IViewScope         { return m.vw }
func (m *stubMessage) Session() core.ISessionScope   { return m.ses }
func (m *stubMessage) Context() context.Context      { return context.Background() }

func newStubMessage(body string) *stubMessage {
	rec := &recorded{}
	return &stubMessage{
		req: &stubRequest{body: []byte(body)},
		res: &stubResponse{rec: rec},
		vw:  &stubView{rec: rec},
		ses: &stubSessionScope{},
		rec: rec,
	}
}

func controllerFor(strategy core.IAuthStrategy, user core.Authenticable) LoginController {
	return LoginController{
		webContext:  &stubWebContext{home: "/home"},
		authContext: &stubAuthContext{strategy: strategy},
		userService: &stubUserService{user: user},
		router:      &stubRouter{},
	}
}

// TestLoginVerifiesTheCredentialsOnAnAlreadyAuthenticatedRequest.
//
// A POST to the login endpoint carries credentials, and credentials that arrive are compared.
// The handler used to answer this request with a redirect home, which is how a login POST ends
// up authenticating nobody: whoever the session already belonged to kept it, and the poster was
// handed that identity. It is safe to authenticate over a live session precisely because the
// strategy replaces the session identifier while doing so.
func TestLoginVerifiesTheCredentialsOnAnAlreadyAuthenticatedRequest(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	strategy := &stubStrategy{loggedIn: true}
	controller := controllerFor(strategy, &stubUser{id: "user-2", password: password})

	message := newStubMessage("username=victim@example.com&password=correct-horse")
	controller.Login(message)

	assert.Equal(t, 1, strategy.loginCalls,
		"the posted credentials have to be authenticated, not skipped over")
	assert.Equal(t, []string{"user-2"}, strategy.loginUsers,
		"and the session must end up belonging to whoever posted them")
	assert.Equal(t, []string{"/home"}, message.rec.redirects)
}

// TestLoginWithABadPasswordLeavesTheLiveSessionAlone pins the failure path: the credentials are
// checked, and a check that fails changes nothing about the session already there. Revoking it
// instead would turn a wrong password into a remote logout for anybody able to make the
// browser post one.
func TestLoginWithABadPasswordLeavesTheLiveSessionAlone(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	strategy := &stubStrategy{loggedIn: true}
	controller := controllerFor(strategy, &stubUser{id: "user-2", password: password})

	message := newStubMessage("username=victim@example.com&password=wrong-horse")
	controller.Login(message)

	assert.Zero(t, strategy.loginCalls, "a rejected password authenticates nobody")
	assert.Equal(t, []string{"/cp.login.show"}, message.rec.redirects,
		"and lands back on the login form rather than the home page")
	assert.Nil(t, message.ses.current,
		"the request's session is left exactly as it was found")
	require.Len(t, message.rec.flashes, 1)
	assert.Contains(t, message.rec.flashes[0], "error")
}

// TestShowLoginDoesNotRenderForAnAlreadyAuthenticatedRequest.
//
// The GET keeps its redirect — an authenticated visitor asking for the login form belongs on
// the home page — and must not render into a response that already carries the 301.
func TestShowLoginDoesNotRenderForAnAlreadyAuthenticatedRequest(t *testing.T) {
	strategy := &stubStrategy{loggedIn: true}
	controller := controllerFor(strategy, nil)

	message := newStubMessage("")
	controller.ShowLogin(message)

	assert.Equal(t, []string{"/home"}, message.rec.redirects)
	assert.Empty(t, message.rec.rendered,
		"a redirected response must not also carry a rendered login form")
}

// TestLoginAuthenticatesAnAnonymousRequest keeps the working path covered, so the
// missing return cannot be "fixed" by never logging anybody in.
func TestLoginAuthenticatesAnAnonymousRequest(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	strategy := &stubStrategy{loggedIn: false}
	controller := controllerFor(strategy, &stubUser{id: "user-1", password: password})

	message := newStubMessage("username=victim@example.com&password=correct-horse")
	controller.Login(message)

	assert.Equal(t, 1, strategy.loginCalls)
	assert.Equal(t, []string{"user-1"}, strategy.loginUsers)
	assert.Equal(t, []string{"/home"}, message.rec.redirects)
}

// TestLoginPublishesTheSessionItAuthenticated.
//
// Login rotates the session identifier, which means the session this request resolved on the
// way in no longer exists by the time the handler returns. Anything that asks afterwards —
// this handler, middleware still to run, IsLoggedIn on the way to the redirect — reads the
// request's session scope, so a handler that does not republish leaves the whole rest of the
// request looking at a deleted session and a successful login looking like a failed one.
func TestLoginPublishesTheSessionItAuthenticated(t *testing.T) {
	password, err := util.HashWithSalt("correct-horse")
	require.NoError(t, err)

	rotated := &stubSession{}
	strategy := &stubStrategy{loggedIn: false, session: rotated}
	controller := controllerFor(strategy, &stubUser{id: "user-1", password: password})

	message := newStubMessage("username=victim@example.com&password=correct-horse")
	controller.Login(message)

	require.NotNil(t, message.ses.current,
		"the handler must install the session it authenticated onto the request")
	assert.Same(t, rotated, message.ses.current,
		"and it must be the session the strategy returned, not the one that came in")
}
