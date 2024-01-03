package auth

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"os"
)

type JwtAuthStrategy struct {
	jwtService *JwtService `container:"inject"`
}

func (thiz *JwtAuthStrategy) NewSessionWithoutUser(ctx context.Context) (string, error) {
	return "", nil
}

func (thiz *JwtAuthStrategy) Login(user core.Authenticable, ctx context.Context) (string, error) {
	return thiz.jwtService.GenerateJwt(user, viper.GetString("auth.jwt.secret"))
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

func (thiz *JwtAuthStrategy) GetCurrentOrCreateSession(ctx context.Context) core.ISession {
	return nil
}

func (thiz *JwtAuthStrategy) GetSessionId(ctx context.Context) string {
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
	_, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(os.Getenv("JWT_SECRET_KEY")), nil
	})
	if err != nil {
		fmt.Println(err)
		return false
	}

	return true
}
