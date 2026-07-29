package middleware

import (
	"net/http"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/auth"
	error2 "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jwtTestUser is what the stub user service hands back.
type jwtTestUser struct {
	core.Authenticable
	username string
	role     core.UserRole
}

func (u *jwtTestUser) GetUsername() string    { return u.username }
func (u *jwtTestUser) GetRole() core.UserRole { return u.role }
func (u *jwtTestUser) GetId() string          { return u.username }

type stubUserService struct {
	user core.Authenticable
	err  error
}

func (s *stubUserService) Get(any) (core.Authenticable, error) { return s.user, s.err }
func (s *stubUserService) GetByUsername(string) (core.Authenticable, error) {
	return s.user, s.err
}
func (s *stubUserService) Save(core.Authenticable) error { return nil }

const jwtTestSecret = "test-secret-for-jwt-middleware"

func withJwtSecret(t *testing.T) {
	t.Helper()
	previous := viper.Get("auth.jwt.secret")
	viper.Set("auth.jwt.secret", jwtTestSecret)
	viper.Set("auth.jwt.lifeTime", 3600)
	t.Cleanup(func() { viper.Set("auth.jwt.secret", previous) })
}

// containerResolvedMiddleware builds the middleware the way the framework does, by
// resolving it through the container.
func containerResolvedMiddleware(t *testing.T, roles []core.UserRole, user core.Authenticable) JwtMiddleware {
	t.Helper()

	c := service.NewContainer()
	require.NoError(t, c.SingletonLazy(func() core.IUserService {
		return &stubUserService{user: user}
	}))

	mw := &JwtMiddleware{Roles: roles}
	require.NoError(t, c.Make(mw))
	require.NotNil(t, mw.JwtService, "the container must fill JwtService")

	return *mw
}

func signToken(t *testing.T, user core.Authenticable) string {
	t.Helper()
	token, err := auth.NewJwtService().GenerateJwt(user, jwtTestSecret)
	require.NoError(t, err)
	return token
}

// TestRolesNoLongerPanic is the T3.3 headline. JwtMiddleware constructed its own
// auth.NewJwtService() outside the container, so the service's own
// `userService core.IUserService` field was never filled — and the role check
// dereferenced it. The documented way to use the middleware was the way that
// crashed.
func TestRolesNoLongerPanic(t *testing.T) {
	withJwtSecret(t)

	user := &jwtTestUser{username: "ann", role: roleAdmin}
	mw := containerResolvedMiddleware(t, []core.UserRole{roleAdmin}, user)

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodGet, "/api/protected", map[string]string{
		"Authorization": "Bearer " + signToken(t, user),
	})

	require.NotPanics(t, func() { handler(message) })
	assert.True(t, reached, "a matching role must pass through")
}

// TestRoleMismatchIsForbidden
func TestJwtRoleMismatchIsForbidden(t *testing.T) {
	withJwtSecret(t)

	user := &jwtTestUser{username: "bo", role: roleUser}
	mw := containerResolvedMiddleware(t, []core.UserRole{roleAdmin}, user)

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })

	message := newMessage(http.MethodGet, "/api/protected", map[string]string{
		"Authorization": "Bearer " + signToken(t, user),
	})
	handler(message)

	assert.False(t, reached)
	assert.Equal(t, http.StatusForbidden, message.recorded.Status)

	body, err := envelope(message.recorded.Body)
	require.NoError(t, err)
	assert.Equal(t, "FORBIDDEN", body["status_code"])
}

// TestNoRolesSkipsTheUserLookupEntirely — the pre-v2 code path that happened to
// work, since it never touched the nil service.
func TestNoRolesSkipsTheUserLookupEntirely(t *testing.T) {
	withJwtSecret(t)

	user := &jwtTestUser{username: "ann", role: roleAdmin}
	mw := containerResolvedMiddleware(t, nil, user)

	reached := false
	handler := mw.Handle(func(core.HttpMessage) { reached = true })
	handler(newMessage(http.MethodGet, "/api/protected", map[string]string{
		"Authorization": "Bearer " + signToken(t, user),
	}))

	assert.True(t, reached)
}

// TestMissingOrInvalidTokenPanicsWithJwtAuthError pins the contract
// RecoveryMiddleware relies on to reach a registered JwtAuthError handler.
func TestMissingOrInvalidTokenPanicsWithJwtAuthError(t *testing.T) {
	withJwtSecret(t)

	user := &jwtTestUser{username: "ann", role: roleAdmin}

	tests := map[string]map[string]string{
		"no header":       nil,
		"empty bearer":    {"Authorization": "Bearer "},
		"garbage token":   {"Authorization": "Bearer not-a-jwt"},
		"wrong signature": {"Authorization": "Bearer " + wrongSecretToken(t, user)},
	}

	for name, headers := range tests {
		t.Run(name, func(t *testing.T) {
			mw := containerResolvedMiddleware(t, []core.UserRole{roleAdmin}, user)
			handler := mw.Handle(func(core.HttpMessage) {
				t.Fatal("handler must not be reached")
			})

			message := newMessage(http.MethodGet, "/api/protected", headers)

			defer func() {
				recovered := recover()
				require.NotNil(t, recovered, "must panic so the error chain runs")
				_, ok := recovered.(*error2.JwtAuthError)
				assert.Truef(t, ok, "must panic with *JwtAuthError, got %T", recovered)
			}()
			handler(message)
		})
	}
}

// TestBearerPrefixIsCaseInsensitiveAndTrimmed
func TestBearerPrefixIsCaseInsensitiveAndTrimmed(t *testing.T) {
	withJwtSecret(t)

	user := &jwtTestUser{username: "ann", role: roleAdmin}
	token := signToken(t, user)

	for _, header := range []string{
		"Bearer " + token,
		"bearer " + token,
		"BEARER " + token,
		"  Bearer   " + token + "  ",
	} {
		mw := containerResolvedMiddleware(t, []core.UserRole{roleAdmin}, user)

		reached := false
		handler := mw.Handle(func(core.HttpMessage) { reached = true })
		handler(newMessage(http.MethodGet, "/api/protected", map[string]string{
			"Authorization": header,
		}))

		assert.Truef(t, reached, "header %q must be accepted", header)
	}
}

// TestNilJwtServiceFailsThroughTheErrorChain covers a middleware built outside the
// container: it must report through the error chain rather than nil-dereference.
func TestNilJwtServiceFailsThroughTheErrorChain(t *testing.T) {
	withJwtSecret(t)

	mw := JwtMiddleware{Roles: []core.UserRole{roleAdmin}} // JwtService left nil

	handler := mw.Handle(func(core.HttpMessage) {
		t.Fatal("handler must not be reached")
	})

	defer func() {
		recovered := recover()
		require.NotNil(t, recovered)
		_, ok := recovered.(*error2.JwtAuthError)
		assert.True(t, ok)
	}()
	handler(newMessage(http.MethodGet, "/api/protected", map[string]string{
		"Authorization": "Bearer whatever",
	}))
}

func wrongSecretToken(t *testing.T, user core.Authenticable) string {
	t.Helper()
	token, err := auth.NewJwtService().GenerateJwt(user, "a-completely-different-secret")
	require.NoError(t, err)
	return token
}
