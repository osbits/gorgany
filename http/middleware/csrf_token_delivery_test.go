package middleware

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A4: before this fix SessionMiddleware set X-CSRF-Token only on the single response
// that created a session; the valid-session branch returned without it. A client that
// missed that one response could not obtain a token, and once v2 closed the CSRF
// middleware's two bypasses every mutating request it made was reliably rejected.

// writableSession is a session whose items can be written, which the token issuer
// needs. fakeSession is read-only and is left alone so the CSRF middleware tests keep
// asserting against a session nothing can mutate.
type writableSession struct {
	core.ISession

	items   map[string]string
	expiry  time.Time
	expired bool

	lastActivity time.Time
}

func newWritableSession() *writableSession {
	return &writableSession{items: map[string]string{}}
}

func (s *writableSession) GetItem(key string) string   { return s.items[key] }
func (s *writableSession) SetItem(key, value string)   { s.items[key] = value }
func (s *writableSession) IsExpired() bool             { return s.expired }
func (s *writableSession) SetExpiry(t time.Time)       { s.expiry = t }
func (s *writableSession) SetLastActivity(t time.Time) { s.lastActivity = t }
func (s *writableSession) GetId() string               { return "session-1" }

type fakeSessionStorage struct {
	core.ISessionStorage

	added   []core.ISession
	deleted []core.ISession
}

func (s *fakeSessionStorage) GetSessionLifetime() time.Duration { return time.Hour }
func (s *fakeSessionStorage) AddSession(session core.ISession)  { s.added = append(s.added, session) }
func (s *fakeSessionStorage) DeleteSession(session core.ISession) {
	s.deleted = append(s.deleted, session)
}

// sessionMiddlewareWith wires a SessionMiddleware around an existing session, the way
// the container would.
func sessionMiddlewareWith(session core.ISession) (SessionMiddleware, *fakeAuthStrategy) {
	strategy := &fakeAuthStrategy{session: session}
	mw := SessionMiddleware{
		AuthContext:    &fakeAuthContext{strategy: strategy},
		SessionStorage: &fakeSessionStorage{},
		csrfService:    &auth.CsrfService{},
	}
	return mw, strategy
}

// TestAPreExistingSessionReceivesAToken is the A4 headline. This is the branch that
// used to return without touching the header.
func TestAPreExistingSessionReceivesAToken(t *testing.T) {
	session := newWritableSession()
	mw, _ := sessionMiddlewareWith(session)

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodGet, "/dashboard", nil)
	handler(message)

	require.True(t, reached, "the request must still be served")

	token := message.recorded.Headers.Get(core.CSRFTokenHeader)
	require.NotEmpty(t, token, "every session-carrying response must publish a token")
	assert.Equal(t, token, session.GetItem(core.CSRFSessionKey),
		"the published token is the one stored in the session")
}

// TestTheTokenAPreExistingSessionReceivesPassesTheMiddleware closes the loop the brief
// asked for: the token handed out is actually accepted by CSRFMiddleware. Publishing a
// token that the check rejects would look fixed and behave identically.
func TestTheTokenAPreExistingSessionReceivesPassesTheMiddleware(t *testing.T) {
	session := newWritableSession()
	sessionMw, _ := sessionMiddlewareWith(session)

	get := newMessage(http.MethodGet, "/dashboard", nil)
	sessionMw.Handle(func(core.HttpMessage) {})(get)

	token := get.recorded.Headers.Get(core.CSRFTokenHeader)
	require.NotEmpty(t, token)

	// Now use it on a mutating request through the real CSRF middleware.
	csrfMw := NewCSRFMiddleware()
	csrfMw.AuthContext = &fakeAuthContext{strategy: &fakeAuthStrategy{session: session}}

	passed := false
	post := newMessage(http.MethodPost, "/widgets", map[string]string{core.CSRFTokenHeader: token})
	csrfMw.Handle(func(core.HttpMessage) { passed = true })(post)

	assert.True(t, passed, "the token just published must satisfy the CSRF check")
	assert.False(t, post.recorded.Written, "nothing should have been rejected")
}

// TestTheTokenIsStableAcrossRequests: the token must not rotate per request, or an
// in-flight request from another tab would carry an already-invalidated one.
func TestTheTokenIsStableAcrossRequests(t *testing.T) {
	session := newWritableSession()
	mw, _ := sessionMiddlewareWith(session)
	handler := mw.Handle(func(core.HttpMessage) {})

	first := newMessage(http.MethodGet, "/a", nil)
	handler(first)
	second := newMessage(http.MethodGet, "/b", nil)
	handler(second)

	tokenA := first.recorded.Headers.Get(core.CSRFTokenHeader)
	tokenB := second.recorded.Headers.Get(core.CSRFTokenHeader)

	require.NotEmpty(t, tokenA)
	assert.Equal(t, tokenA, tokenB, "the token is reused, not rotated per request")
}

// TestAnExpiredSessionIsReplacedAndStillPublishesAToken keeps the creation path, which
// already worked, covered alongside the new one.
func TestAnExpiredSessionIsReplacedAndStillPublishesAToken(t *testing.T) {
	expired := newWritableSession()
	expired.expired = true

	created := newWritableSession()
	strategy := &fakeAuthStrategy{session: expired, newSession: created}

	storage := &fakeSessionStorage{}
	mw := SessionMiddleware{
		AuthContext:    &fakeAuthContext{strategy: strategy},
		SessionStorage: storage,
		csrfService:    &auth.CsrfService{},
	}

	scope := &fakeSessionScope{}
	message := newMessage(http.MethodGet, "/dashboard", nil)
	message.session = scope
	mw.Handle(func(core.HttpMessage) {})(message)

	assert.Same(t, created, scope.current, "the new session is installed on the message")
	assert.Equal(t, []core.ISession{expired}, storage.deleted, "the expired session is dropped")
	assert.Equal(t, []core.ISession{created}, storage.added)
	assert.NotEmpty(t, message.recorded.Headers.Get(core.CSRFTokenHeader))
}

// TestOptionsCarriesNoToken: the middleware returns early for preflight, and a
// preflight response is not one a client reads a token from.
func TestOptionsCarriesNoToken(t *testing.T) {
	session := newWritableSession()
	mw, _ := sessionMiddlewareWith(session)

	message := newMessage(http.MethodOptions, "/widgets", nil)
	message.ctx = context.WithValue(context.Background(),
		core.MessageContextKey, &optionsMessageContext{req: message.req.raw})

	reached := false
	mw.Handle(func(core.HttpMessage) { reached = true })(message)

	assert.True(t, reached)
	assert.Empty(t, message.recorded.Headers.Get(core.CSRFTokenHeader))
}

// TestAMissingCsrfServiceDoesNotBreakTheRequest: the field is container-injected, so a
// misconfigured app can leave it nil. Losing the token header is a degradation; a nil
// dereference on every request is an outage.
func TestAMissingCsrfServiceDoesNotBreakTheRequest(t *testing.T) {
	session := newWritableSession()
	strategy := &fakeAuthStrategy{session: session}
	mw := SessionMiddleware{
		AuthContext:    &fakeAuthContext{strategy: strategy},
		SessionStorage: &fakeSessionStorage{},
	}

	reached := false
	message := newMessage(http.MethodGet, "/dashboard", nil)
	require.NotPanics(t, func() {
		mw.Handle(func(core.HttpMessage) { reached = true })(message)
	})

	assert.True(t, reached)
	assert.Empty(t, message.recorded.Headers.Get(core.CSRFTokenHeader))
}

type optionsMessageContext struct {
	core.IMessageContext
	req *http.Request
}

func (c *optionsMessageContext) GetRequest() *http.Request { return c.req }
