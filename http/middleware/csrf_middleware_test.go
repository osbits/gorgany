package middleware

import (
	"context"
	"net/http"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------ auth doubles

type fakeSession struct {
	core.ISession
	items map[string]string
}

func (s *fakeSession) GetItem(key string) string { return s.items[key] }

type fakeAuthStrategy struct {
	core.IAuthStrategy

	session core.ISession
	// madeWithStrategy is what the old code consulted to decide whether to skip
	// the CSRF check entirely.
	madeWithStrategy bool
	strategyAsked    bool

	// newSession is what NewSessionWithoutUser hands back, for the SessionMiddleware
	// tests that exercise the session-creation path.
	newSession core.ISession
}

func (s *fakeAuthStrategy) NewSessionWithoutUser(context.Context) (core.ISession, error) {
	return s.newSession, nil
}

func (s *fakeAuthStrategy) CurrentSession(context.Context) core.ISession { return s.session }

func (s *fakeAuthStrategy) IsRequestMadeWithStrategy(context.Context) bool {
	s.strategyAsked = true
	return s.madeWithStrategy
}

type fakeAuthContext struct {
	core.IAuthContext

	strategy core.IAuthStrategy
	byName   map[string]core.IAuthStrategy
}

func (c *fakeAuthContext) ResolveAuthStrategyByContext(context.Context) core.IAuthStrategy {
	return c.strategy
}

func (c *fakeAuthContext) Strategy(name ...string) core.IAuthStrategy {
	if len(name) == 0 {
		return c.strategy
	}
	return c.byName[name[0]]
}

// csrfWith builds a CSRF middleware around a session holding sessionToken.
func csrfWith(sessionToken string, madeWithStrategy bool) (*CSRFMiddleware, *fakeAuthStrategy) {
	var session core.ISession
	if sessionToken != "" {
		session = &fakeSession{items: map[string]string{csrfTokenKey: sessionToken}}
	}

	strategy := &fakeAuthStrategy{session: session, madeWithStrategy: madeWithStrategy}

	mw := NewCSRFMiddleware()
	mw.AuthContext = &fakeAuthContext{strategy: strategy}
	return mw, strategy
}

// ----------------------------------------------------------------------- tests

// TestOptionsIsAnsweredWithoutInvokingTheHandler is the T3.2 headline. OPTIONS was
// on the exempt list, and the router registers every route under OPTIONS as well as
// its declared method, so any mutating handler was reachable via OPTIONS with no
// token — and it ran. This asserts no side effect.
func TestOptionsIsAnsweredWithoutInvokingTheHandler(t *testing.T) {
	mw, _ := csrfWith("expected", true)

	sideEffect := false
	handler := mw.Handle(func(core.HttpMessage) { sideEffect = true })

	message := newMessage(http.MethodOptions, "/widgets/1", nil)
	handler(message)

	assert.False(t, sideEffect, "OPTIONS must never reach the route handler")
	assert.Equal(t, http.StatusNoContent, message.recorded.Status)
}

// TestOptionsIsNotOnTheExemptList pins the list itself, since the bypass was a
// single entry in it.
func TestOptionsIsNotOnTheExemptList(t *testing.T) {
	mw := NewCSRFMiddleware()

	assert.Equal(t,
		[]string{http.MethodGet, http.MethodHead, http.MethodTrace},
		mw.ExemptMethods,
		"only methods that are safe by definition may be exempt")
	assert.NotContains(t, mw.ExemptMethods, http.MethodOptions)
}

// TestSafeMethodsSkipTheCheck
func TestSafeMethodsSkipTheCheck(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodTrace} {
		t.Run(method, func(t *testing.T) {
			mw, _ := csrfWith("expected", true)

			reached := false
			handler := mw.Handle(func(core.HttpMessage) { reached = true })
			handler(newMessage(method, "/widgets", nil))

			assert.True(t, reached, "%s must pass through", method)
		})
	}
}

// TestMutatingMethodsRequireAToken covers every method that can change state.
func TestMutatingMethodsRequireAToken(t *testing.T) {
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		t.Run(method, func(t *testing.T) {
			mw, _ := csrfWith("expected", true)

			reached := false
			handler := mw.Handle(func(core.HttpMessage) { reached = true })

			message := newMessage(method, "/widgets/1", nil)
			handler(message)

			assert.False(t, reached, "%s with no token must be rejected", method)
			assert.Equal(t, http.StatusForbidden, message.recorded.Status)
		})
	}
}

// TestTheCheckIsNotSkippedWhenThereIsNoSession is the second bypass. The middleware
// short-circuited on !IsRequestMadeWithStrategy() — "no cookie, no check" — and a
// request arriving without a session is exactly the shape a cross-site forgery has.
func TestTheCheckIsNotSkippedWhenThereIsNoSession(t *testing.T) {
	mw, strategy := csrfWith("", false) // no session, request not made with strategy

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodPost, "/widgets", map[string]string{
		core.CSRFTokenHeader: "anything",
	})
	handler(message)

	assert.False(t, reached, "a request with no session must not bypass the check")
	assert.Equal(t, http.StatusForbidden, message.recorded.Status)
	assert.False(t, strategy.strategyAsked,
		"IsRequestMadeWithStrategy must no longer be consulted at all")
}

// TestValidTokenPassesThrough
func TestValidTokenPassesThrough(t *testing.T) {
	mw, _ := csrfWith("expected-token", true)

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodPost, "/widgets", map[string]string{
		core.CSRFTokenHeader: "expected-token",
	})
	handler(message)

	assert.True(t, reached)
	assert.False(t, message.recorded.Written)
}

func TestMismatchedTokenIsRejected(t *testing.T) {
	mw, _ := csrfWith("expected-token", true)

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodPost, "/widgets", map[string]string{
		core.CSRFTokenHeader: "wrong-token",
	})
	handler(message)

	assert.False(t, reached)
	assert.Equal(t, http.StatusForbidden, message.recorded.Status)
}

// TestTokenPrefixIsNotAccepted guards the constant-time comparison: the old `!=`
// would also have rejected this, but a prefix match must never be treated as equal.
func TestTokenPrefixIsNotAccepted(t *testing.T) {
	mw, _ := csrfWith("expected-token", true)

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	handler(newMessage(http.MethodPost, "/widgets", map[string]string{
		core.CSRFTokenHeader: "expected",
	}))

	assert.False(t, reached)
}

// TestRejectionsUseTheStandardEnvelope: rejections used to emit a bare
// {"error": "..."} that no client parsing the framework's shape could read.
func TestRejectionsUseTheStandardEnvelope(t *testing.T) {
	tests := []struct {
		name         string
		sessionToken string
		header       map[string]string
		wantStatus   int
		wantCode     string
		wantReason   string
	}{
		{
			name:         "token missing",
			sessionToken: "expected",
			header:       nil,
			wantStatus:   http.StatusForbidden,
			wantCode:     "FORBIDDEN",
			wantReason:   "CSRF token missing",
		},
		{
			name:         "no active session",
			sessionToken: "",
			header:       map[string]string{core.CSRFTokenHeader: "provided"},
			wantStatus:   http.StatusForbidden,
			wantCode:     "FORBIDDEN",
			wantReason:   "No active session",
		},
		{
			name:         "invalid token",
			sessionToken: "expected",
			header:       map[string]string{core.CSRFTokenHeader: "provided"},
			wantStatus:   http.StatusForbidden,
			wantCode:     "FORBIDDEN",
			wantReason:   "Invalid CSRF token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw, _ := csrfWith(tt.sessionToken, true)
			handler := mw.Handle(func(core.HttpMessage) {})

			message := newMessage(http.MethodPost, "/widgets", tt.header)
			handler(message)

			require.Equal(t, tt.wantStatus, message.recorded.Status)

			body, err := envelope(message.recorded.Body)
			require.NoError(t, err)
			assert.Equal(t, float64(tt.wantStatus), body["status"])
			assert.Equal(t, tt.wantCode, body["status_code"])
			assert.Contains(t, body["errors"], tt.wantReason)
			assert.Contains(t, body, "body", "the envelope always carries a body key")
		})
	}
}

// TestUninitializedCSRFProtectionIsRejected
func TestUninitializedCSRFProtectionIsRejected(t *testing.T) {
	strategy := &fakeAuthStrategy{session: &fakeSession{items: map[string]string{}}}
	mw := NewCSRFMiddleware()
	mw.AuthContext = &fakeAuthContext{strategy: strategy}

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodPost, "/widgets", map[string]string{
		core.CSRFTokenHeader: "provided",
	})
	handler(message)

	assert.False(t, reached)
	body, err := envelope(message.recorded.Body)
	require.NoError(t, err)
	assert.Contains(t, body["errors"], "CSRF protection not initialized")
}

// TestMissingAuthStrategyIsA500
func TestMissingAuthStrategyIsA500(t *testing.T) {
	mw := NewCSRFMiddleware()
	mw.AuthContext = &fakeAuthContext{strategy: nil}

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodPost, "/widgets", nil)
	handler(message)

	assert.False(t, reached)
	assert.Equal(t, http.StatusInternalServerError, message.recorded.Status)
}

// TestLowercaseMethodIsStillMatched guards the EqualFold comparison.
func TestLowercaseMethodIsStillMatched(t *testing.T) {
	mw, _ := csrfWith("expected", true)

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodGet, "/widgets", nil)
	message.req.raw.Method = "get"
	handler(message)

	assert.True(t, reached, "a lowercase safe method must still be exempt")
}
