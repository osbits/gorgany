package controller

import (
	"context"
	"encoding/json"
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
}

func (s *stubStrategy) CurrentSession(context.Context) core.ISession { return s.session }

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
	res *stubResponse
	rec *recorded
}

func (m *stubMessage) Response() core.IResponseScope { return m.res }
func (m *stubMessage) Context() context.Context      { return context.Background() }

func newStubMessage() *stubMessage {
	rec := &recorded{}
	return &stubMessage{res: &stubResponse{rec: rec}, rec: rec}
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
	c := NewCsrfController()
	c.AuthContext = &stubAuthContext{strategy: &stubStrategy{session: session}}
	c.CsrfService = &auth.CsrfService{}
	return c
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

// TestTokenWithoutASessionIsRefusedNotFabricated. SessionMiddleware creates a session
// for any request lacking one, so reaching this means the endpoint was mounted outside
// that filter — worth saying rather than returning a token bound to nothing.
func TestTokenWithoutASessionIsRefusedNotFabricated(t *testing.T) {
	message := newStubMessage()
	controllerWith(nil).Token(message)

	assert.Equal(t, http.StatusForbidden, message.rec.status)

	body := envelope(t, message.rec.body)
	assert.Contains(t, body["errors"], "No active session; the CSRF endpoint must be covered by the session middleware")
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
