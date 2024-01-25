package auth

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"github.com/golang-jwt/jwt/v5"
	"github.com/spf13/viper"
	"time"
)

func NewJwtService() *JwtService {
	return &JwtService{}
}

type JwtService struct {
}

func (thiz JwtService) GenerateJwt(user core.Authenticable, secret string) (string, error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)

	jwtLifeTime := viper.GetInt("auth.jwt.lifeTime")
	claims["exp"] = time.Now().Add(time.Duration(jwtLifeTime) * time.Second).Unix()
	claims["username"] = user.GetUsername()

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

func (thiz JwtService) ValidateJwt(token string, secret string) bool {
	t, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})

	if err != nil {
		return false
	}

	return t.Valid
}

func (thiz JwtService) ParseJwt(token string, secret string) (jwt.MapClaims, error) {
	t, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	return t.Claims.(jwt.MapClaims), err
}

func (thiz JwtService) GetUser(token string, secret string) (core.Authenticable, error) {
	claims, err := thiz.ParseJwt(token, secret)
	if err != nil {
		return nil, err
	}

	return GetAuthEntityService().GetByUsername(claims["username"].(string))
}

// CurrentUser
// ctx - instance of core.IMessageContext
func (thiz JwtService) CurrentUser(ctx context.Context, secret string) (core.Authenticable, error) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Context is not IMessageContext instance")
	}

	token := messageContext.GetBearerToken()
	if token == "" {
		return nil, fmt.Errorf("User not found")
	}

	return thiz.GetUser(token, secret)
}
