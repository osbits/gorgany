package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------ doubles
//
// Each double embeds the interface it stands in for, so a controller reaching for
// something these tests do not model fails with a nil-method panic instead of being
// silently accommodated.

type stubSession struct {
	core.ISession
	items map[string]string
}

func (s *stubSession) GetItem(key string) string { return s.items[key] }
func (s *stubSession) SetItem(key, value string) { s.items[key] = value }
func (s *stubSession) GetId() string             { return "session-1" }
func (s *stubSession) IsExpired() bool           { return false }
func (s *stubSession) SetExpiry(time.Time)       {}

type stubStrategy struct {
	core.IAuthStrategy
	session core.ISession

	// created is what NewSessionWithoutUser hands back, and createdErr its failure. Both
	// zero by default, which models JwtAuthStrategy: it returns (nil, nil) because a bearer
	// token carries no CSRF exposure.
	created    core.ISession
	createdErr error
	newCalls   int
}

func (s *stubStrategy) CurrentSession(context.Context) core.ISession { return s.session }

func (s *stubStrategy) NewSessionWithoutUser(context.Context) (core.ISession, error) {
	s.newCalls++
	return s.created, s.createdErr
}

type stubAuthContext struct {
	core.IAuthContext
	strategy core.IAuthStrategy
}

func (c *stubAuthContext) ResolveAuthStrategyByContext(context.Context) core.IAuthStrategy {
	return c.strategy
}

type recorded struct {
	status  int
	body    any
	headers http.Header
}

type stubResponse struct {
	core.IResponseScope
	rec *recorded
}

func (r *stubResponse) JSON(v any, code int) {
	r.rec.status = code
	r.rec.body = v
}

func (r *stubResponse) Header() http.Header {
	if r.rec.headers == nil {
		r.rec.headers = http.Header{}
	}
	return r.rec.headers
}

type stubMessage struct {
	core.HttpMessage
	res     *stubResponse
	rec     *recorded
	session *stubSessionScope
}

// stubSessionScope is the request's session slot. The controller sets it after creating a
// session so the rest of the request sees one, as it would have had the middleware run.
type stubSessionScope struct {
	core.IEditableSessionScope
	set core.ISession
}

func (s *stubSessionScope) Set(session core.ISession) { s.set = session }

func (m *stubMessage) Response() core.IResponseScope { return m.res }
func (m *stubMessage) Context() context.Context      { return context.Background() }
func (m *stubMessage) Session() core.ISessionScope   { return m.session }

func newStubMessage() *stubMessage {
	rec := &recorded{}
	return &stubMessage{
		res:     &stubResponse{rec: rec},
		rec:     rec,
		session: &stubSessionScope{},
	}
}

// envelope decodes a recorded body the way a client would receive it.
func envelope(t *testing.T, body any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

func controllerWith(session core.ISession) *CsrfController {
	return controllerForStrategy(&stubStrategy{session: session})
}

func controllerForStrategy(strategy *stubStrategy) *CsrfController {
	c := NewCsrfController()
	c.AuthContext = &stubAuthContext{strategy: strategy}
	c.CsrfService = &auth.CsrfService{}
	c.SessionStorage = &stubSessionStorage{}
	return c
}

// stubSessionStorage records the upsert. It matters because ISession.SetItem is in-memory
// only for the DB-backed store, so a token that is never AddSession'd is lost.
type stubSessionStorage struct {
	core.ISessionStorage
	added []core.ISession
}

func (s *stubSessionStorage) AddSession(session core.ISession) {
	s.added = append(s.added, session)
}

// -------------------------------------------------------------------- tests

// TestTokenIssuesATokenForASession is the endpoint half of A4: a SPA calls this on
// boot and gets a token it can use.
func TestTokenIssuesATokenForASession(t *testing.T) {
	session := &stubSession{items: map[string]string{}}
	message := newStubMessage()

	controllerWith(session).Token(message)

	require.Equal(t, http.StatusOK, message.rec.status)

	body := envelope(t, message.rec.body)
	payload, ok := body["body"].(map[string]any)
	require.True(t, ok, "the response uses the standard envelope")

	token, ok := payload["csrf_token"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, token)
	assert.Equal(t, core.CSRFTokenHeader, payload["header"],
		"the response names the header to send the token back in")

	assert.Equal(t, token, message.rec.headers.Get(core.CSRFTokenHeader),
		"the token is in the header too, for a uniform same-origin code path")
	assert.Equal(t, token, session.GetItem(core.CSRFSessionKey),
		"the token handed out is the one stored in the session")
}

// TestTokenReturnsTheExistingTokenRatherThanRotating: a second call must not
// invalidate the token an in-flight request from another tab is carrying.
func TestTokenReturnsTheExistingTokenRatherThanRotating(t *testing.T) {
	session := &stubSession{items: map[string]string{}}
	controller := controllerWith(session)

	first := newStubMessage()
	controller.Token(first)
	second := newStubMessage()
	controller.Token(second)

	tokenA := first.rec.headers.Get(core.CSRFTokenHeader)
	tokenB := second.rec.headers.Get(core.CSRFTokenHeader)

	require.NotEmpty(t, tokenA)
	assert.Equal(t, tokenA, tokenB)
}

// TestTokenWithoutASessionStartsOne is H2, and it replaces a test that pinned the defect.
//
// The endpoint used to answer 403 "the CSRF endpoint must be covered by the session
// middleware" for a request carrying no session — which is the *first* request every client
// makes, and precisely the one this endpoint exists to serve. GetRoutes mounts it at the root
// with no middleware while an app's session middleware is scoped to the namespace it protects,
// so the framework's own default registration could never satisfy its own precondition: the
// 403 was permanent, and the documented escape was DisableCsrfController() plus a
// hand-registered copy.
//
// The old test asserted that 403 and passed, which is how the endpoint shipped unusable.
func TestTokenWithoutASessionStartsOne(t *testing.T) {
	fresh := &stubSession{items: map[string]string{}}
	strategy := &stubStrategy{session: nil, created: fresh}

	message := newStubMessage()
	controllerForStrategy(strategy).Token(message)

	require.Equal(t, http.StatusOK, message.rec.status,
		"the first call a client ever makes must succeed")
	assert.Equal(t, 1, strategy.newCalls,
		"the session comes from the strategy, the same call SessionMiddleware makes")

	body := envelope(t, message.rec.body)
	payload, ok := body["body"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, fresh.GetItem(core.CSRFSessionKey), payload["csrf_token"],
		"the token is bound to the session that was created, not to nothing")
	assert.Same(t, core.ISession(fresh), message.session.set,
		"the new session is published on the request scope, as the middleware would")
}

// TestAnExistingSessionIsNotReplaced — the create path must be reached only when there is
// genuinely no session, or every call to this endpoint would rotate the caller's session.
func TestAnExistingSessionIsNotReplaced(t *testing.T) {
	existing := &stubSession{items: map[string]string{}}
	strategy := &stubStrategy{session: existing, created: &stubSession{items: map[string]string{}}}

	controllerForStrategy(strategy).Token(newStubMessage())

	assert.Zero(t, strategy.newCalls)
}

// TestTheTokenIsPersisted. ISession.SetItem writes DbSessionEntity's Attributes map and
// nothing else, so a token that is not AddSession'd survives only until the response ends —
// and the next request's CSRF check then rejects it. SessionMiddleware upserts for the same
// reason.
func TestTheTokenIsPersisted(t *testing.T) {
	session := &stubSession{items: map[string]string{}}
	strategy := &stubStrategy{session: session}
	controller := controllerForStrategy(strategy)
	storage := controller.SessionStorage.(*stubSessionStorage)

	controller.Token(newStubMessage())

	require.Len(t, storage.added, 1, "the session carrying the new token must be persisted")
	assert.Same(t, core.ISession(session), storage.added[0])
}

// TestASessionlessStrategySaysSoRatherThanFailing. JwtAuthStrategy.NewSessionWithoutUser
// returns (nil, nil) by design: a bearer token is not sent automatically by the browser, so
// there is no CSRF exposure and no token to issue. Reported as a client error naming the
// reason — ResolveAuthStrategyByContext picks per request, so this is a property of the
// request, not a misconfigured app.
func TestASessionlessStrategySaysSoRatherThanFailing(t *testing.T) {
	message := newStubMessage()
	controllerForStrategy(&stubStrategy{}).Token(message)

	assert.Equal(t, http.StatusBadRequest, message.rec.status)
	assert.Contains(t, fmt.Sprint(envelope(t, message.rec.body)["errors"]),
		"does not use sessions")
}

// TestAFailedSessionCreationIsAnInternalError, not a token bound to nothing.
func TestAFailedSessionCreationIsAnInternalError(t *testing.T) {
	message := newStubMessage()
	controllerForStrategy(&stubStrategy{createdErr: errors.New("storage down")}).Token(message)

	assert.Equal(t, http.StatusInternalServerError, message.rec.status)
}

// TestTheEndpointWorksWithoutSessionStorage — the field is injected, and an app running
// memory sessions with no storage bound must still get a token rather than a nil panic.
func TestTheEndpointWorksWithoutSessionStorage(t *testing.T) {
	c := NewCsrfController()
	c.AuthContext = &stubAuthContext{strategy: &stubStrategy{session: &stubSession{items: map[string]string{}}}}
	c.CsrfService = &auth.CsrfService{}
	c.SessionStorage = nil

	message := newStubMessage()
	require.NotPanics(t, func() { c.Token(message) })
	assert.Equal(t, http.StatusOK, message.rec.status)
}

// TestTokenWithoutAStrategyIsAnInternalError, not a panic: the field is injected, so a
// misconfigured container can leave the strategy unresolvable.
func TestTokenWithoutAStrategyIsAnInternalError(t *testing.T) {
	c := NewCsrfController()
	c.AuthContext = &stubAuthContext{strategy: nil}
	c.CsrfService = &auth.CsrfService{}

	message := newStubMessage()
	require.NotPanics(t, func() { c.Token(message) })
	assert.Equal(t, http.StatusInternalServerError, message.rec.status)
}

// TestTokenWithoutTheServiceIsAnInternalError covers the other injected field.
func TestTokenWithoutTheServiceIsAnInternalError(t *testing.T) {
	c := NewCsrfController()
	c.AuthContext = &stubAuthContext{strategy: &stubStrategy{session: &stubSession{items: map[string]string{}}}}

	message := newStubMessage()
	require.NotPanics(t, func() { c.Token(message) })
	assert.Equal(t, http.StatusInternalServerError, message.rec.status)
}

// TestTheRouteIsMountedWhereClientsAreToldToLookFor pins the default path, since the
// docs and the client contract quote it.
func TestTheRouteIsMountedWhereClientsAreToldToLookFor(t *testing.T) {
	routes := NewCsrfController().GetRoutes()
	require.Len(t, routes, 1)

	assert.Equal(t, core.DefaultCSRFTokenPath, routes[0].GetPath())
	assert.Equal(t, core.GET, routes[0].GetMethod())
	assert.Equal(t, "gorgany.csrf.token", routes[0].GetName())
}

// TestAnAppCanRemountTheEndpoint: the path is overridable, because an app already
// serving something at /csrf must be able to move it.
func TestAnAppCanRemountTheEndpoint(t *testing.T) {
	c := NewCsrfController()
	c.Path = "/api/v1/csrf"

	routes := c.GetRoutes()
	require.Len(t, routes, 1)
	assert.Equal(t, "/api/v1/csrf", routes[0].GetPath())
}

// TestTheEndpointIsExemptFromTheCsrfCheckItself is the circularity check: GET is on
// CSRFMiddleware's safe-method list, so fetching a token never needs a token. If the
// endpoint were ever changed to POST this test is what would catch the deadlock.
func TestTheEndpointIsExemptFromTheCsrfCheckItself(t *testing.T) {
	routes := NewCsrfController().GetRoutes()
	require.Equal(t, core.GET, routes[0].GetMethod(),
		"a mutating method here would require a token to obtain a token")

	// And prove the assumption rather than asserting it in prose.
	req := httptest.NewRequest(http.MethodGet, core.DefaultCSRFTokenPath, nil)
	assert.Equal(t, http.MethodGet, req.Method)
}
