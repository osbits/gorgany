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

	"github.com/spf13/viper"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	"github.com/osbits/gorgany/v2/http/middleware"
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

func (r *stubResponse) Text(body string, code int) {
	r.rec.status = code
	r.rec.body = body
}

func (r *stubResponse) SetHeader(key, value string) { r.Header().Set(key, value) }

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
	req     *stubRequest
	session *stubSessionScope
}

// stubRequest exists because the rate limiter (I2) derives its bucket key from the request:
// the client address from RemoteAddr, and the route from the method and path. RemoteAddr is
// fixed, so every call in a test shares one bucket — which is what makes a burst measurable.
type stubRequest struct {
	core.IRequestScope
	raw *http.Request
}

func (r *stubRequest) RawRequest() *http.Request { return r.raw }
func (r *stubRequest) Header() http.Header       { return r.raw.Header }
func (r *stubRequest) PathParam(string) string   { return "" }

// stubSessionScope is the request's session slot. The controller sets it after creating a
// session so the rest of the request sees one, as it would have had the middleware run.
type stubSessionScope struct {
	core.IEditableSessionScope
	set core.ISession
}

func (s *stubSessionScope) Set(session core.ISession) { s.set = session }

func (m *stubMessage) Response() core.IResponseScope { return m.res }
func (m *stubMessage) Request() core.IRequestScope   { return m.req }
func (m *stubMessage) Context() context.Context      { return context.Background() }
func (m *stubMessage) Session() core.ISessionScope   { return m.session }

func newStubMessage() *stubMessage {
	rec := &recorded{}
	request := httptest.NewRequest(http.MethodGet, core.DefaultCSRFTokenPath, nil)
	request.RemoteAddr = "203.0.113.7:54321"
	// A SPA fetches this endpoint, so the refusal should come back as an envelope rather
	// than text — which is also the path the limiter's own negotiation takes.
	request.Header.Set("Accept", core.ApplicationJson.String())

	return &stubMessage{
		res:     &stubResponse{rec: rec},
		rec:     rec,
		req:     &stubRequest{raw: request},
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

// stubSessionStorage records anything the controller writes to storage, which should be
// nothing at all — see CsrfController.SessionStorage.
type stubSessionStorage struct {
	core.ISessionStorage
	added []core.ISession
}

func (s *stubSessionStorage) AddSession(session core.ISession) error {
	s.added = append(s.added, session)
	return nil
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

// TestTheTokenIsPersistedByTheSessionItself, not by this endpoint writing to storage.
//
// This test used to assert the opposite — that Token() ends with AddSession — on the belief
// that ISession.SetItem is in-memory only. It is not: a DB-backed session writes through to
// its row on every SetItem, and a memory store hands out the object it is holding, so the
// upsert added nothing. What it did add was a way for a public, unauthenticated endpoint to
// put a session back into storage after it had been revoked, so it is gone; the assertion
// that pinned it is inverted here rather than deleted, because "the token still survives the
// response" is the property that mattered and it has to keep being checked.
func TestTheTokenIsPersistedByTheSessionItself(t *testing.T) {
	session := &stubSession{items: map[string]string{}}
	strategy := &stubStrategy{session: session}
	controller := controllerForStrategy(strategy)
	storage := controller.SessionStorage.(*stubSessionStorage)

	controller.Token(newStubMessage())

	assert.NotEmpty(t, session.GetItem(core.CSRFSessionKey),
		"the token is written into the session, which is what persists it")
	assert.Empty(t, storage.added,
		"the endpoint must not write a session back to storage: it resolves the session at "+
			"the top of the handler, so anything it upserts may already have been revoked")
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

// ------------------------------ I2: the write path H2 opened must be metered

// I2. H2 made this endpoint start a session when the request carries none. That is what makes
// it usable, and it also made it the one public route in a typical app that writes a row to
// `sessions`: only RecoveryMiddleware is global by default, rate limiting is opt-in, this
// route is registered outside an app's middleware patterns, and an app's session middleware is
// scoped to the namespace it protects. Before H2 it answered 403 and wrote nothing; after, a
// curl loop grew the table without bound.
//
// The limiter and H4's sweep are one mitigation in two halves: the limit caps the arrival
// rate, the sweep collects each row once it expires, and the product bounds the table. Either
// alone leaves it unbounded — the limit only slows growth, and the sweep cannot keep up with
// rows arriving faster than a lifetime.

func TestTheTokenRouteIsRateLimitedByDefault(t *testing.T) {
	routes := NewCsrfController().GetRoutes()
	require.Len(t, routes, 1)

	mws := routes[0].GetMiddlewares()
	require.Len(t, mws, 1, "the endpoint that creates sessions must be metered")

	limiter, ok := mws[0].(*middleware.RateLimitMiddleware)
	require.True(t, ok, "expected a rate limiter, got %T", mws[0])
	assert.Equal(t, DefaultCsrfRate, limiter.Rate)
}

// TestTheDefaultRateIsLooseEnoughForRealTraffic. The number has to clear the worst plausible
// legitimate burst, not the average: an office behind one NAT opening the app at 09:00, or an
// app behind a proxy with TrustRateLimitForwardedFor left off, where every request shares a
// single bucket. Tightening this to something that looks more "secure" breaks those and
// inconveniences an attacker who can just use more addresses.
func TestTheDefaultRateIsLooseEnoughForRealTraffic(t *testing.T) {
	require.NoError(t, DefaultCsrfRate.Validate())

	assert.Equal(t, time.Minute, DefaultCsrfRate.Window)
	assert.GreaterOrEqual(t, DefaultCsrfRate.Burst, 100,
		"a whole office behind one address must not be locked out")
}

func TestTheRateIsConfigurable(t *testing.T) {
	c := NewCsrfController()
	c.Rate = middleware.PerMinute(7)

	limiter, ok := c.GetRoutes()[0].GetMiddlewares()[0].(*middleware.RateLimitMiddleware)
	require.True(t, ok)
	assert.Equal(t, middleware.PerMinute(7), limiter.Rate)
}

// TestAnInvalidRateFallsBackToTheDefault rather than panicking inside
// NewRateLimitMiddleware at boot. A zero RateLimit is what a struct literal that sets other
// fields produces, so it must mean "unset", not "reject every request".
func TestAnInvalidRateFallsBackToTheDefault(t *testing.T) {
	c := NewCsrfController()
	c.Rate = middleware.RateLimit{} // Burst 0 would refuse everything

	limiter, ok := c.GetRoutes()[0].GetMiddlewares()[0].(*middleware.RateLimitMiddleware)
	require.True(t, ok)
	assert.Equal(t, DefaultCsrfRate, limiter.Rate)
}

func TestTheRateLimitCanBeTurnedOff(t *testing.T) {
	c := NewCsrfController()
	c.DisableRateLimit = true

	assert.Empty(t, c.GetRoutes()[0].GetMiddlewares(),
		"an app metering at the edge must be able to opt out")
}

// TestForwardedForIsNotTrustedByDefault. X-Forwarded-For is caller-supplied, so trusting it
// with no proxy in front lets a client pick its own bucket and rotate through unlimited ones —
// which is the same as not rate-limiting at all.
func TestForwardedForIsNotTrustedByDefault(t *testing.T) {
	limiter, ok := NewCsrfController().GetRoutes()[0].GetMiddlewares()[0].(*middleware.RateLimitMiddleware)
	require.True(t, ok)
	assert.False(t, limiter.TrustForwardedFor)

	c := NewCsrfController()
	c.TrustRateLimitForwardedFor = true
	behindProxy, ok := c.GetRoutes()[0].GetMiddlewares()[0].(*middleware.RateLimitMiddleware)
	require.True(t, ok)
	assert.True(t, behindProxy.TrustForwardedFor,
		"an app behind a trusted proxy must be able to key on the real client")
}

// TestTheLimitActuallyRefusesSessionCreation exercises the limiter over the real handler, so
// what is pinned is that a flood stops creating sessions — not merely that a middleware is
// attached to a slice.
func TestTheLimitActuallyRefusesSessionCreation(t *testing.T) {
	withStorageConfigured(t)

	const burst = 3

	c := NewCsrfController()
	c.Rate = middleware.RateLimit{Burst: burst, Window: time.Hour}
	c.CsrfService = &auth.CsrfService{}
	c.SessionStorage = &stubSessionStorage{}

	strategy := &stubStrategy{}
	c.AuthContext = &stubAuthContext{strategy: strategy}
	// Every call arrives with no session, so every allowed one creates a new one.
	strategy.created = &stubSession{items: map[string]string{}}

	limiter, ok := c.GetRoutes()[0].GetMiddlewares()[0].(*middleware.RateLimitMiddleware)
	require.True(t, ok)
	metered := limiter.Handle(c.Token)

	var refused int
	for i := 0; i < burst*4; i++ {
		message := newStubMessage()
		strategy.created = &stubSession{items: map[string]string{}}
		metered(message)

		if message.rec.status == http.StatusTooManyRequests {
			refused++
		}
	}

	assert.Equal(t, burst, strategy.newCalls,
		"only the allowed calls may create a session")
	assert.Equal(t, burst*3, refused, "the rest must be refused")
}

func withStorageConfigured(t *testing.T) {
	t.Helper()

	previous := viper.Get("auth.session.storage")
	viper.Set("auth.session.storage", "database")
	t.Cleanup(func() { viper.Set("auth.session.storage", previous) })
}
