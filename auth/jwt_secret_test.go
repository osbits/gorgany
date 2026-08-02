package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// strongTestSecret is 43 characters of the sort of value the framework asks for: it is
// long enough to be a real HMAC-SHA256 key rather than something a laptop can grind.
const strongTestSecret = "PyD8yhvAyBFC0Qs4Q9k1TfKp7cJmVn2xLr6WdZbGtHs"

// withJwtLifetime gives GenerateJwt a lifetime to put in `exp`. Without it the claim is
// `now`, so a freshly minted token is already expired.
func withJwtLifetime(t *testing.T) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("auth.jwt.lifeTime", 3600)
}

type secretTestUser struct {
	core.Authenticable
}

func (u *secretTestUser) GetUsername() string    { return "ann" }
func (u *secretTestUser) GetRole() core.UserRole { return core.UserRole("admin") }

// unusableSecrets is every shape a misconfigured deployment actually produces.
//
// "" is `JWT_SECRET=` in a .env, a compose file or a CI secret that resolved empty, and a
// literal `secret: ""` or a YAML null in config.yaml. The placeholder literals are what an
// app gets if it substitutes placeholders itself and keeps the unresolved text. The short
// and repeated values are keys a laptop grinds through.
//
// The cases are chosen so that each rule in ValidateJwtSecret is the *only* thing rejecting
// at least one of them, because the rules overlap heavily and an unexercised rule is one
// nobody notices breaking. A short secret is usually also repetitive, so "short but varied"
// is the case only the length floor catches, and a placeholder is usually shorter than the
// floor, so "a long placeholder" is the case only the placeholder rule catches. See
// TestValidateJwtSecretReportsWhichRuleRejectedTheKey, which pins that separation directly.
var unusableSecrets = map[string]string{
	"empty":                "",
	"a single space":       " ",
	"whitespace only":      "   \t\n ",
	"unresolved literal":   "${JWT_SECRET}",
	"a long placeholder":   "${APPLICATION_JWT_SIGNING_SECRET}",
	"short":                "s3cret",
	"short but varied":     "Tr0ub4dor&3",
	"just under the floor": "correct-horse-battery-staple-01",
	"padded to length":     strings.Repeat("a", 64),
}

// TestGenerateJwtRefusesAnUnusableSecret. GenerateJwt(user, "") returned a working token
// and a nil error, so an app whose secret never resolved handed out tokens anyone could
// mint for themselves.
func TestGenerateJwtRefusesAnUnusableSecret(t *testing.T) {
	service := JwtService{}

	for name, secret := range unusableSecrets {
		t.Run(name, func(t *testing.T) {
			token, err := service.GenerateJwt(&secretTestUser{}, secret)

			require.Error(t, err, "a token signed with this key is forgeable")
			assert.Empty(t, token)
			assert.Contains(t, err.Error(), "auth.jwt.secret",
				"the error must name the key the operator has to fix")
		})
	}
}

// TestValidateJwtRefusesAnUnusableSecret: the token is genuinely signed with the same
// unusable key, which is exactly what an attacker does once they know the key is empty or
// guessable.
func TestValidateJwtRefusesAnUnusableSecret(t *testing.T) {
	service := JwtService{}

	for name, secret := range unusableSecrets {
		t.Run(name, func(t *testing.T) {
			forged := signWithClaims(t, secret, jwt.MapClaims{
				"username": "admin",
				RoleClaim:  "admin",
				"exp":      time.Now().Add(24 * time.Hour).Unix(),
			})

			assert.False(t, service.ValidateJwt(forged, secret),
				"a signature this key produces proves nothing")
		})
	}
}

// TestParseJwtRefusesAnUnusableSecret, and with it GetUser and CurrentUser, which both go
// through it.
func TestParseJwtRefusesAnUnusableSecret(t *testing.T) {
	service := JwtService{}

	for name, secret := range unusableSecrets {
		t.Run(name, func(t *testing.T) {
			forged := signWithClaims(t, secret, jwt.MapClaims{
				"username": "admin",
				"exp":      time.Now().Add(24 * time.Hour).Unix(),
			})

			claims, err := service.ParseJwt(forged, secret)

			require.Error(t, err)
			assert.Nil(t, claims)
			assert.Contains(t, err.Error(), "auth.jwt.secret")
		})
	}
}

// TestGetUserRefusesAnUnusableSecret pins the path JwtMiddleware's role check takes: a
// forged username must not be resolved through the app's own user service.
func TestGetUserRefusesAnUnusableSecret(t *testing.T) {
	service := JwtService{userService: &claimsUserService{user: &claimsTestUser{username: "admin"}}}

	forged := signWithClaims(t, "", jwt.MapClaims{
		"username": "admin",
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
	})

	user, err := service.GetUser(forged, "")
	require.Error(t, err)
	assert.Nil(t, user)
}

// TestValidateJwtSecretReportsWhichRuleRejectedTheKey pins each rule separately.
//
// Without this the rules hide each other. Every "weak" secret anyone reaches for is both
// short and repetitive, so the documented 32-byte floor and the distinct-byte floor each
// look tested while only one of them is doing the work: delete the length comparison and
// "s3cret" is still rejected — for having six distinct bytes — and a genuinely varied
// eleven-byte key sails through. The same masking hides the placeholder rule, which only
// matters for a placeholder long enough to clear the floor, and the blank rule, which only
// changes the wording for a key the floor would reject anyway.
//
// The assertions are on the diagnosis rather than on the rejection because the diagnosis is
// the only evidence of *which* rule fired, and it is also what the operator acts on.
func TestValidateJwtSecretReportsWhichRuleRejectedTheKey(t *testing.T) {
	tests := []struct {
		rule     string
		secret   string
		diagnose string
	}{
		{"blank", "", "empty or whitespace only"},
		{"blank", "   \t\n ", "empty or whitespace only"},
		// 33 bytes of 18 distinct characters: past both floors, so nothing but the
		// placeholder rule stands between it and being used as a signing key.
		{"placeholder", "${APPLICATION_JWT_SIGNING_SECRET}", "literal placeholder"},
		// 11 and 31 bytes, 10 and 15 distinct: past the distinct-byte floor, so the length
		// floor is the only rule that rejects them.
		{"length", "Tr0ub4dor&3", "which is short enough to guess"},
		{"length", "correct-horse-battery-staple-01", "which is short enough to guess"},
		// 64 bytes, 1 distinct: past the length floor.
		{"distinct bytes", strings.Repeat("a", 64), "only 1 distinct bytes"},
	}

	for _, test := range tests {
		t.Run(test.rule+": "+test.secret, func(t *testing.T) {
			err := ValidateJwtSecret(test.secret)

			require.Error(t, err, "this key must not be usable for signing")
			assert.Contains(t, err.Error(), test.diagnose,
				"the %s rule has to be the one that rejects this key", test.rule)
		})
	}
}

// TestAStrongSecretStillWorksEndToEnd — the guard must reject only what it is meant to.
func TestAStrongSecretStillWorksEndToEnd(t *testing.T) {
	withJwtLifetime(t)

	service := JwtService{}

	token, err := service.GenerateJwt(&secretTestUser{}, strongTestSecret)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	assert.True(t, service.ValidateJwt(token, strongTestSecret))

	claims, err := service.ParseJwt(token, strongTestSecret)
	require.NoError(t, err)
	assert.Equal(t, "ann", claims["username"])
}

// TestAnHs384TokenIsRejectedWhereHs256IsAccepted is the one thing
// jwt.WithValidMethods([]string{"HS256"}) actually buys.
//
// golang-jwt/jwt v5 already rejects alg=none, its case variants and an RSA/ECDSA algorithm
// against an HMAC key; see jwtParseOptions for the three separate mechanisms that do that.
// What it does not reject without the option is a *different HMAC* method:
// HS384 and HS512 take a []byte too, so a token the holder of the secret signed as HS256
// can be re-signed as HS384 and still verify. Pinning the method keeps the accepted set to
// the one method GenerateJwt produces.
func TestAnHs384TokenIsRejectedWhereHs256IsAccepted(t *testing.T) {
	service := JwtService{}

	claims := jwt.MapClaims{
		"username": "ann",
		"exp":      time.Now().Add(time.Hour).Unix(),
	}

	hs256, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte(strongTestSecret))
	require.NoError(t, err)
	hs384, err := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).
		SignedString([]byte(strongTestSecret))
	require.NoError(t, err)

	assert.True(t, service.ValidateJwt(hs256, strongTestSecret),
		"the method the framework signs with must keep working")
	assert.False(t, service.ValidateJwt(hs384, strongTestSecret),
		"a different HMAC method against the same key must not verify")

	_, err = service.ParseJwt(hs384, strongTestSecret)
	assert.Error(t, err, "ParseJwt is the path GetUser and CurrentUser take")
}
