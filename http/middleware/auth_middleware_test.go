package middleware

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ------------------------------------------------------------------ auth doubles

type fakeUser struct {
	core.Authenticable
	role core.UserRole
}

func (u *fakeUser) GetRole() core.UserRole { return u.role }

type loginStrategy struct {
	core.IAuthStrategy

	loggedIn bool
	user     core.Authenticable
	userErr  error
}

func (s *loginStrategy) IsLoggedIn(context.Context) bool { return s.loggedIn }

func (s *loginStrategy) CurrentUser(context.Context) (core.Authenticable, error) {
	return s.user, s.userErr
}

func authWith(roles []core.UserRole, strategies map[string]core.IAuthStrategy) AuthMiddleware {
	return AuthMiddleware{
		Roles:       roles,
		AuthContext: &fakeAuthContext{byName: strategies},
	}
}

const (
	roleAdmin core.UserRole = "admin"
	roleUser  core.UserRole = "user"
)

// ----------------------------------------------------------------------- tests

// TestNilUserDoesNotPanic is the first half of T3.4. CurrentUser can return
// (nil, nil) on several paths and the code checked only err before calling
// user.GetRole(), which took the connection down.
func TestNilUserDoesNotPanic(t *testing.T) {
	mw := authWith([]core.UserRole{roleAdmin}, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{loggedIn: true, user: nil},
	})

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodGet, "/api/widgets", nil)

	require.NotPanics(t, func() { handler(message) })

	assert.False(t, reached)
	// A session pointing at a user who no longer exists is not authenticated, so
	// 401 is right here — not 403.
	assert.Equal(t, http.StatusUnauthorized, message.recorded.Status)
}

// TestRoleMismatchIs403 is the second half of T3.4. A role mismatch produced 401,
// telling an authenticated user with the wrong role that they were unauthenticated.
func TestRoleMismatchIs403(t *testing.T) {
	mw := authWith([]core.UserRole{roleAdmin}, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{
			loggedIn: true,
			user:     &fakeUser{role: roleUser},
		},
	})

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodGet, "/api/widgets", nil)
	handler(message)

	assert.False(t, reached)
	assert.Equal(t, http.StatusForbidden, message.recorded.Status,
		"an authenticated user with the wrong role is forbidden, not unauthenticated")

	body, err := envelope(message.recorded.Body)
	require.NoError(t, err)
	assert.Equal(t, "FORBIDDEN", body["status_code"])
}

// TestMissingSessionIs401 pins the other side of the same distinction.
func TestMissingSessionIs401(t *testing.T) {
	mw := authWith([]core.UserRole{roleAdmin}, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{loggedIn: false},
	})

	message := newMessage(http.MethodGet, "/api/widgets", nil)
	mw.Handle(func(core.HttpMessage) {})(message)

	assert.Equal(t, http.StatusUnauthorized, message.recorded.Status)

	body, err := envelope(message.recorded.Body)
	require.NoError(t, err)
	assert.Equal(t, "NOT_AUTHORIZED", body["status_code"])
}

func TestMatchingRolePassesThrough(t *testing.T) {
	mw := authWith([]core.UserRole{roleAdmin, roleUser}, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{
			loggedIn: true,
			user:     &fakeUser{role: roleUser},
		},
	})

	reached := false
	mw.Handle(func(core.HttpMessage) { reached = true })(newMessage(http.MethodGet, "/api/x", nil))

	assert.True(t, reached)
}

func TestNoRolesRequiredOnlyNeedsALogin(t *testing.T) {
	mw := authWith(nil, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{loggedIn: true},
	})

	reached := false
	mw.Handle(func(core.HttpMessage) { reached = true })(newMessage(http.MethodGet, "/api/x", nil))

	assert.True(t, reached, "with no Roles set, CurrentUser is never consulted")
}

// TestCurrentUserErrorIsNotReportedAsACredentialProblem: a broken user lookup is
// not the caller's fault. It panics so the registered error handlers decide, which
// RecoveryMiddleware makes reachable.
func TestCurrentUserErrorIsNotReportedAsACredentialProblem(t *testing.T) {
	mw := authWith([]core.UserRole{roleAdmin}, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{
			loggedIn: true,
			userErr:  errors.New("user store is down"),
		},
	})

	message := newMessage(http.MethodGet, "/api/x", nil)

	assert.PanicsWithError(t, "user store is down", func() {
		mw.Handle(func(core.HttpMessage) {})(message)
	})
	assert.False(t, message.recorded.Written, "no 401 may be written for a lookup failure")
}

// TestAMissingStrategyIsSkippedNotDereferenced
func TestAMissingStrategyIsSkippedNotDereferenced(t *testing.T) {
	mw := AuthMiddleware{
		AuthStrategies: []string{"nonexistent"},
		AuthContext:    &fakeAuthContext{byName: map[string]core.IAuthStrategy{}},
	}

	message := newMessage(http.MethodGet, "/api/x", nil)
	require.NotPanics(t, func() { mw.Handle(func(core.HttpMessage) {})(message) })
	assert.Equal(t, http.StatusUnauthorized, message.recorded.Status)
}

// TestASecondStrategyIsTriedWhenTheFirstDoesNotRecogniseTheCaller
func TestASecondStrategyIsTriedWhenTheFirstDoesNotRecogniseTheCaller(t *testing.T) {
	mw := AuthMiddleware{
		Roles:          []core.UserRole{roleAdmin},
		AuthStrategies: []string{"jwt", "session"},
		AuthContext: &fakeAuthContext{byName: map[string]core.IAuthStrategy{
			"jwt":     &loginStrategy{loggedIn: false},
			"session": &loginStrategy{loggedIn: true, user: &fakeUser{role: roleAdmin}},
		}},
	}

	reached := false
	mw.Handle(func(core.HttpMessage) { reached = true })(newMessage(http.MethodGet, "/api/x", nil))

	assert.True(t, reached)
}

// ------------------------------------------------------- JSON vs HTML detection

// TestJSONIsDetectedWithoutAnApiNamespace is the third part of T3.4. The decision
// keyed on Content-Type == application/json or PathParam("namespace") == "api". A
// GET carries no Content-Type, so an app was forced to put every route in an `api`
// namespace just to get a JSON 401.
func TestJSONIsDetectedWithoutAnApiNamespace(t *testing.T) {
	tests := []struct {
		name    string
		method  string
		target  string
		headers map[string]string
	}{
		{
			name:    "Accept: application/json on a GET",
			method:  http.MethodGet,
			target:  "/widgets",
			headers: map[string]string{"Accept": "application/json"},
		},
		{
			name:   "/api/ path prefix",
			method: http.MethodGet,
			target: "/api/widgets",
		},
		{
			name:   "bare /api path",
			method: http.MethodGet,
			target: "/api",
		},
		{
			name:    "Content-Type on a POST",
			method:  http.MethodPost,
			target:  "/widgets",
			headers: map[string]string{"Content-Type": "application/json"},
		},
		{
			name:    "Content-Type with a charset parameter",
			method:  http.MethodPost,
			target:  "/widgets",
			headers: map[string]string{"Content-Type": "application/json; charset=utf-8"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := authWith(nil, map[string]core.IAuthStrategy{
				core.DefaultKeyInRegistrar: &loginStrategy{loggedIn: false},
			})

			message := newMessage(tt.method, tt.target, tt.headers)
			mw.Handle(func(core.HttpMessage) {})(message)

			assert.Equal(t, http.StatusUnauthorized, message.recorded.Status)
			assert.NotNil(t, message.recorded.Body, "must answer with the JSON envelope")
			assert.Empty(t, message.recorded.RedirectURL, "must not redirect an API client")
		})
	}
}

// TestBrowserGetStillRedirects guards against over-matching: a browser sends
// `Accept: text/html,...,*/*`, and treating the wildcard as JSON would turn every
// login redirect into a JSON body.
func TestBrowserGetStillRedirects(t *testing.T) {
	mw := authWith(nil, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{loggedIn: false},
	})

	message := newMessage(http.MethodGet, "/dashboard", map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,*/*;q=0.8",
	})
	mw.Handle(func(core.HttpMessage) {})(message)

	assert.Equal(t, http.StatusFound, message.recorded.Status)
	assert.Equal(t, core.DefaultLoginUrl, message.recorded.RedirectURL)
	assert.Nil(t, message.recorded.Body)
}

// TestApiNamespacePathParamStillWorks keeps the pre-v2 signal working.
func TestApiNamespacePathParamStillWorks(t *testing.T) {
	mw := authWith(nil, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{loggedIn: false},
	})

	message := newMessage(http.MethodGet, "/widgets", nil)
	message.req.pathParams["namespace"] = "api"

	mw.Handle(func(core.HttpMessage) {})(message)

	assert.Equal(t, http.StatusUnauthorized, message.recorded.Status)
	assert.NotNil(t, message.recorded.Body)
}

// TestForbiddenBrowserRequestIsNotRedirectedToLogin: logging in again does not fix
// a role mismatch, so a redirect would loop the user.
func TestForbiddenBrowserRequestIsNotRedirectedToLogin(t *testing.T) {
	mw := authWith([]core.UserRole{roleAdmin}, map[string]core.IAuthStrategy{
		core.DefaultKeyInRegistrar: &loginStrategy{
			loggedIn: true,
			user:     &fakeUser{role: roleUser},
		},
	})

	message := newMessage(http.MethodGet, "/dashboard", map[string]string{
		"Accept": "text/html",
	})
	mw.Handle(func(core.HttpMessage) {})(message)

	assert.Equal(t, http.StatusForbidden, message.recorded.Status)
	assert.Empty(t, message.recorded.RedirectURL)
}
