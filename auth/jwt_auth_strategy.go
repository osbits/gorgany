package auth

import (
	"context"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"os"
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

	bearerToken := messageContext.GetBearerToken()
	return thiz.jwtService.ValidateJwt(bearerToken)
}

func (thiz *JwtAuthStrategy) Logout(ctx context.Context) {
}

func (thiz *JwtAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	return thiz.jwtService.CurrentUser(ctx)
}

func (thiz *JwtAuthStrategy) ResolveSessionId(ctx context.Context) string {
	return ""
}

func (thiz *JwtAuthStrategy) CurrentSession(ctx context.Context) core.ISession {
	return nil
}

func (thiz *JwtAuthStrategy) IsRequestMadeWithStrategy(ctx context.Context) bool {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		err2.HandleError("Ctx is not core.IMessageContext instance")
		return false
	}

	token := messageContext.GetBearerToken()
	if token == "" {
		return false
	}

	_, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(os.Getenv("JWT_SECRET_KEY")), nil
	})
	if err != nil {
		return false
	}

	return true
}
