package auth

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jwtHeaderContext is a message context carrying nothing but an Authorization header,
// which is all the strategy looks at.
type jwtHeaderContext struct {
	header http.Header
}

func newJwtHeaderContext(authorization string) *jwtHeaderContext {
	header := http.Header{}
	if authorization != "" {
		header.Set("Authorization", authorization)
	}
	return &jwtHeaderContext{header: header}
}

func (c *jwtHeaderContext) GetURL() *url.URL                      { return &url.URL{} }
func (c *jwtHeaderContext) GetRequestURL() string                 { return "/api/things" }
func (c *jwtHeaderContext) GetCookieManager() core.ICookieManager { return nil }
func (c *jwtHeaderContext) GetHeader() http.Header                { return c.header }
func (c *jwtHeaderContext) GetPathParam(string) string            { return "" }
func (c *jwtHeaderContext) GetSession() core.ISession             { return nil }
func (c *jwtHeaderContext) SetSession(core.ISession)              {}
func (c *jwtHeaderContext) GetRequest() *http.Request             { return nil }
func (c *jwtHeaderContext) GetRequestId() string                  { return "req-jwt" }
func (c *jwtHeaderContext) GetIp() string                         { return "203.0.113.9" }
func (c *jwtHeaderContext) GetRequestContext() context.Context    { return context.Background() }

// sessionOnlyStrategy stands in for the strategy a session-based app actually wants
// selected. Only the methods ResolveAuthStrategyByContext touches do anything.
type sessionOnlyStrategy struct {
	core.IAuthStrategy
}

func (s *sessionOnlyStrategy) IsRequestMadeWithStrategy(context.Context) bool { return false }

// forgedBearer is a token an attacker mints for themselves once the app's key is empty or
// guessable: the signature is real, the identity is chosen.
func forgedBearer(t *testing.T, secret string) string {
	t.Helper()

	return "Bearer " + signWithClaims(t, secret, jwt.MapClaims{
		"username": "admin",
		RoleClaim:  "admin",
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
	})
}

// TestIsRequestMadeWithStrategyRefusesAnUnusableSecret.
//
// The method claimed a request whenever jwt.Parse succeeded, and with an unusable key
// anyone can produce a token that parses. Because AppProvider registers this strategy under
// "api" unconditionally, that captured ResolveAuthStrategyByContext even in an app that
// only ever configured session auth — which substitutes the RBAC principal, drops session
// handling and makes Logout a no-op.
func TestIsRequestMadeWithStrategyRefusesAnUnusableSecret(t *testing.T) {
	for name, secret := range unusableSecrets {
		t.Run(name, func(t *testing.T) {
			viper.Reset()
			t.Cleanup(viper.Reset)
			viper.Set("auth.jwt.secret", secret)

			strategy := &JwtAuthStrategy{jwtService: &JwtService{}}
			ctx := context.WithValue(context.Background(), core.MessageContextKey,
				core.IMessageContext(newJwtHeaderContext(forgedBearer(t, secret))))

			assert.False(t, strategy.IsRequestMadeWithStrategy(ctx),
				"a strategy that cannot verify anything must not claim the request")
			assert.False(t, strategy.IsLoggedIn(ctx))
		})
	}
}

// TestResolveAuthStrategyByContextDoesNotSelectJwtWithAnUnusableSecret is the consequence
// that matters: the registry hands the request to the app's real strategy instead.
func TestResolveAuthStrategyByContextDoesNotSelectJwtWithAnUnusableSecret(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("auth.jwt.secret", "")

	jwtStrategy := &JwtAuthStrategy{jwtService: &JwtService{}}
	sessionStrategy := &sessionOnlyStrategy{}

	authContext := &AuthContext{}
	authContext.RegisterAuthStrategy(core.DefaultKeyInRegistrar, sessionStrategy)
	authContext.RegisterAuthStrategy("api", jwtStrategy)

	ctx := context.WithValue(context.Background(), core.MessageContextKey,
		core.IMessageContext(newJwtHeaderContext(forgedBearer(t, ""))))

	resolved := authContext.ResolveAuthStrategyByContext(ctx)

	assert.NotSame(t, core.IAuthStrategy(jwtStrategy), resolved,
		"a forged bearer token must not hijack strategy resolution")
	assert.Same(t, core.IAuthStrategy(sessionStrategy), resolved)
}

// TestIsRequestMadeWithStrategyStillClaimsARealToken keeps the strategy usable for the apps
// that configure JWT properly.
func TestIsRequestMadeWithStrategyStillClaimsARealToken(t *testing.T) {
	withJwtLifetime(t)
	viper.Set("auth.jwt.secret", strongTestSecret)

	strategy := &JwtAuthStrategy{jwtService: &JwtService{}}

	token, err := strategy.jwtService.GenerateJwt(&secretTestUser{}, strongTestSecret)
	require.NoError(t, err)

	ctx := context.WithValue(context.Background(), core.MessageContextKey,
		core.IMessageContext(newJwtHeaderContext("Bearer "+token)))

	assert.True(t, strategy.IsRequestMadeWithStrategy(ctx))
	assert.True(t, strategy.IsLoggedIn(ctx))
}
