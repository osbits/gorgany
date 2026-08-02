package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signWithClaims mints a validly-signed token carrying exactly the given claims.
// The signature is real; only the claim set is hostile.
func signWithClaims(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

type claimsUserService struct {
	user core.Authenticable
}

func (s *claimsUserService) Get(any) (core.Authenticable, error) { return s.user, nil }
func (s *claimsUserService) GetByUsername(string) (core.Authenticable, error) {
	return s.user, nil
}
func (s *claimsUserService) Save(core.Authenticable) error { return nil }

// TestGetUserRejectsAMalformedUsernameClaim is the B1 headline site.
//
// `claims["username"].(string)` was unchecked. Anything able to produce a valid
// signature chooses the claim set, so a token with the claim absent, null, or of the
// wrong JSON type panicked inside GetUser — reached from JwtMiddleware on any
// role-guarded route.
func TestGetUserRejectsAMalformedUsernameClaim(t *testing.T) {
	const secret = strongTestSecret

	tests := map[string]jwt.MapClaims{
		"claim absent":      {"exp": time.Now().Add(time.Hour).Unix()},
		"claim is null":     {"username": nil, "exp": time.Now().Add(time.Hour).Unix()},
		"claim is a number": {"username": 42, "exp": time.Now().Add(time.Hour).Unix()},
		"claim is an object": {
			"username": map[string]any{"nested": "value"},
			"exp":      time.Now().Add(time.Hour).Unix(),
		},
		"claim is an array": {"username": []any{"a"}, "exp": time.Now().Add(time.Hour).Unix()},
		"claim is a bool":   {"username": true, "exp": time.Now().Add(time.Hour).Unix()},
		"claim is empty":    {"username": "", "exp": time.Now().Add(time.Hour).Unix()},
	}

	for name, claims := range tests {
		t.Run(name, func(t *testing.T) {
			service := JwtService{userService: &claimsUserService{user: nil}}
			token := signWithClaims(t, secret, claims)

			var user core.Authenticable
			var err error
			require.NotPanics(t, func() { user, err = service.GetUser(token, secret) },
				"a hostile claim set must be rejected, not panic")

			require.Error(t, err)
			assert.Nil(t, user)
			assert.Contains(t, err.Error(), "username")
		})
	}
}

// TestGetUserAcceptsAWellFormedClaim keeps the happy path.
func TestGetUserAcceptsAWellFormedClaim(t *testing.T) {
	const secret = strongTestSecret

	expected := &claimsTestUser{username: "ann"}
	service := JwtService{userService: &claimsUserService{user: expected}}

	token := signWithClaims(t, secret, jwt.MapClaims{
		"username": "ann",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})

	user, err := service.GetUser(token, secret)
	require.NoError(t, err)
	assert.Same(t, expected, user)
}

// TestGetUserWithoutAUserServiceIsAnError covers the nil-service path, which
// JwtMiddleware used to reach whenever it built its own service outside the
// container (T3.3).
func TestGetUserWithoutAUserServiceIsAnError(t *testing.T) {
	const secret = strongTestSecret
	service := JwtService{}

	token := signWithClaims(t, secret, jwt.MapClaims{
		"username": "ann",
		"exp":      time.Now().Add(time.Hour).Unix(),
	})

	var err error
	require.NotPanics(t, func() { _, err = service.GetUser(token, secret) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user service")
}

type claimsTestUser struct {
	core.Authenticable
	username string
}

func (u *claimsTestUser) GetUsername() string { return u.username }
