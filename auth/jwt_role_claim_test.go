package auth

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roleUser struct {
	core.Authenticable
	username string
	role     core.UserRole
}

func (u *roleUser) GetUsername() string    { return u.username }
func (u *roleUser) GetRole() core.UserRole { return u.role }

// TestGeneratedTokensCarryTheRole is C6: the token used to hold only exp and username, so
// a client had no way to know the user's role without an extra request.
func TestGeneratedTokensCarryTheRole(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("auth.jwt.lifeTime", 3600)

	const secret = "test-secret"
	service := JwtService{}

	token, err := service.GenerateJwt(&roleUser{username: "ann", role: "admin"}, secret)
	require.NoError(t, err)

	claims, err := service.ParseJwt(token, secret)
	require.NoError(t, err)

	assert.Equal(t, "ann", claims["username"])
	assert.Equal(t, "admin", claims[RoleClaim])

	role, ok := RoleFromClaims(claims)
	require.True(t, ok)
	assert.Equal(t, core.UserRole("admin"), role)
}

// TestRoleFromClaimsRejectsAnythingUnusable. The claim set is chosen by whatever can
// produce a signature, so every shape has to be handled without panicking — this is the
// same class as the username claim in B1.
func TestRoleFromClaimsRejectsAnythingUnusable(t *testing.T) {
	cases := map[string]jwt.MapClaims{
		"absent":       {"username": "ann"},
		"null":         {RoleClaim: nil},
		"number":       {RoleClaim: 7},
		"object":       {RoleClaim: map[string]any{"role": "admin"}},
		"array":        {RoleClaim: []any{"admin"}},
		"bool":         {RoleClaim: true},
		"empty string": {RoleClaim: ""},
	}

	for name, claims := range cases {
		t.Run(name, func(t *testing.T) {
			var role core.UserRole
			var ok bool
			require.NotPanics(t, func() { role, ok = RoleFromClaims(claims) })
			assert.False(t, ok)
			assert.Empty(t, role)
		})
	}
}

// TestAnEmptyRoleIsStillWritten: a user with no role produces an empty claim rather than
// an absent one, and RoleFromClaims reports that as unusable. Pinned so the claim never
// becomes a way to smuggle "" past a comparison.
func TestAnEmptyRoleIsStillWritten(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("auth.jwt.lifeTime", 3600)

	const secret = "test-secret"
	service := JwtService{}

	token, err := service.GenerateJwt(&roleUser{username: "ann", role: ""}, secret)
	require.NoError(t, err)

	claims, err := service.ParseJwt(token, secret)
	require.NoError(t, err)
	assert.Equal(t, "", claims[RoleClaim])

	_, ok := RoleFromClaims(claims)
	assert.False(t, ok, "an empty role must not read as a usable one")
}

// TestTheRoleClaimIsNotTheAuthorityForAuthorisation documents and pins the resolution of
// the staleness question the brief asked about.
//
// A token is immutable for its lifetime, so a role baked into one outlives a role
// change. The user-service lookup stays the authority: demote a user and the very next
// request sees the new role, even though the token they are still holding says otherwise.
func TestTheRoleClaimIsNotTheAuthorityForAuthorisation(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("auth.jwt.lifeTime", 3600)

	const secret = "test-secret"

	// A token minted while the user was an admin.
	user := &roleUser{username: "ann", role: "admin"}
	service := JwtService{userService: &roleUserService{user: user}}

	token, err := service.GenerateJwt(user, secret)
	require.NoError(t, err)

	claims, err := service.ParseJwt(token, secret)
	require.NoError(t, err)
	claimedRole, _ := RoleFromClaims(claims)
	require.Equal(t, core.UserRole("admin"), claimedRole)

	// The user is demoted. The token is unchanged — it is signed, so it cannot be
	// updated — and still claims admin.
	user.role = "user"

	stillClaimed, _ := RoleFromClaims(claims)
	assert.Equal(t, core.UserRole("admin"), stillClaimed,
		"the signed claim is frozen, which is exactly why it cannot authorise")

	// The lookup, which is what JwtMiddleware uses, sees the demotion immediately.
	resolved, err := service.GetUser(token, secret)
	require.NoError(t, err)
	assert.Equal(t, core.UserRole("user"), resolved.GetRole(),
		"the user service is the authority, so revocation takes effect at once")
}

type roleUserService struct {
	user core.Authenticable
}

func (s *roleUserService) Get(any) (core.Authenticable, error) { return s.user, nil }
func (s *roleUserService) GetByUsername(string) (core.Authenticable, error) {
	return s.user, nil
}
func (s *roleUserService) Save(core.Authenticable) error { return nil }
