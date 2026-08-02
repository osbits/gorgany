package auth

import (
	"context"
	"github.com/osbits/gorgany/v2/util"

	"github.com/golang-jwt/jwt/v5"
	"github.com/osbits/gorgany/v2/app/core"
	err2 "github.com/osbits/gorgany/v2/err"
	"github.com/spf13/viper"
)

type JwtAuthStrategy struct {
	jwtService *JwtService `container:"inject"`
}

func (thiz *JwtAuthStrategy) NewSessionWithoutUser(ctx context.Context) (core.ISession, error) {
	return nil, nil
}

func (thiz *JwtAuthStrategy) Login(user core.Authenticable, ctx context.Context) (core.ISession, error) {
	jwt, err := thiz.jwtService.GenerateJwt(user, viper.GetString("auth.jwt.secret"))
	if err != nil {
		return nil, err
	}

	return &Session{id: jwt}, nil
}

func (thiz *JwtAuthStrategy) IsLoggedIn(ctx context.Context) bool {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		err2.HandleError("Ctx is not core.IMessageContext instance")
		return false
	}

	bearerToken := util.ParseBearerToken(messageContext.GetHeader().Get("Authorization"))
	return thiz.jwtService.ValidateJwt(bearerToken, viper.GetString("auth.jwt.secret"))
}

// Logout is a no-op and reports success. A bearer token carries no server-side state to
// revoke: it stops working when it expires, and nothing this strategy can do to the request
// shortens that. An app that needs revocable tokens has to keep a deny list of its own.
func (thiz *JwtAuthStrategy) Logout(ctx context.Context) error {
	return nil
}

func (thiz *JwtAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	return thiz.jwtService.CurrentUser(ctx, viper.GetString("auth.jwt.secret"))
}

func (thiz *JwtAuthStrategy) ResolveSessionId(ctx context.Context) string {
	return ""
}

func (thiz *JwtAuthStrategy) CurrentSession(ctx context.Context) core.ISession {
	return nil
}

// IsRequestMadeWithStrategy claims a request only when this strategy can actually verify
// the token it carries.
//
// The secret check is not redundant with the boot validation, and it is the reason an
// unconfigured app is safe. AppProvider registers this strategy under "api" for every app,
// including one that only ever configured session auth, and AuthContext.ResolveAuthStrategyByContext
// hands the request to the first strategy that claims it. The claim used to be "jwt.Parse
// succeeded", so with an empty or guessable key any request carrying a bearer token the
// caller signed themselves captured strategy resolution — which substitutes the principal
// RBAC decides against, silently drops session handling, and turns Logout into a no-op. None
// of that requires the app to have mounted JwtMiddleware or configured JWT at all.
func (thiz *JwtAuthStrategy) IsRequestMadeWithStrategy(ctx context.Context) bool {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		err2.HandleError("Ctx is not core.IMessageContext instance")
		return false
	}

	secret := viper.GetString("auth.jwt.secret")
	if ValidateJwtSecret(secret) != nil {
		return false
	}

	token := util.ParseBearerToken(messageContext.GetHeader().Get("Authorization"))
	if token == "" {
		return false
	}

	// jwtParseOptions pins the signing method; see its comment for what that does and does
	// not buy.
	_, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	}, jwtParseOptions...)
	if err != nil {
		return false
	}

	return true
}

func (thiz *JwtAuthStrategy) ShouldRotateSession(session core.ISession) bool {
	return false // JWT sessions don't need rotation
}

func (thiz *JwtAuthStrategy) RotateSession(ctx context.Context, oldSession core.ISession) (core.ISession, error) {
	return oldSession, nil // JWT sessions don't need rotation
}
