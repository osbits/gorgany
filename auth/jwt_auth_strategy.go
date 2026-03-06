package auth

import (
	"context"
	"github.com/osbits/gorgany/util"

	"github.com/osbits/gorgany/app/core"
	err2 "github.com/osbits/gorgany/err"
	"github.com/golang-jwt/jwt/v5"
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

func (thiz *JwtAuthStrategy) Logout(ctx context.Context) {
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

func (thiz *JwtAuthStrategy) IsRequestMadeWithStrategy(ctx context.Context) bool {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		err2.HandleError("Ctx is not core.IMessageContext instance")
		return false
	}

	token := util.ParseBearerToken(messageContext.GetHeader().Get("Authorization"))
	if token == "" {
		return false
	}

	_, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(viper.GetString("auth.jwt.secret")), nil
	})
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
