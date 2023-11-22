package auth

import (
	"context"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"gorgany/app/core"
	"os"
	"time"
)

func NewJwtService() *JwtService {
	return &JwtService{}
}

type JwtService struct {
}

func (thiz JwtService) GenerateJwt(user core.Authenticable) (string, error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)
	claims["exp"] = time.Now().Add(10 * time.Minute).Unix()
	claims["user"] = user.GetUsername()

	tokenString, err := token.SignedString([]byte(os.Getenv("JWT_SECRET_KEY")))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

func (thiz JwtService) ValidateJwt(token string) bool {
	t, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(os.Getenv("JWT_SECRET_KEY")), nil
	})

	if err != nil {
		return false
	}

	return t.Valid
}

func (thiz JwtService) ParseJwt(token string) (jwt.MapClaims, error) {
	t, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(os.Getenv("JWT_SECRET_KEY")), nil
	})
	if err != nil {
		return nil, err
	}
	return t.Claims.(jwt.MapClaims), err
}

func (thiz JwtService) GetUser(token string) (core.Authenticable, error) {
	claims, err := thiz.ParseJwt(token)
	if err != nil {
		return nil, err
	}

	return GetAuthEntityService().GetByUsername(claims["user"].(string))
}

// CurrentUser
// ctx - instance of core.IMessageContext
func (thiz JwtService) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Context is not IMessageContext instance")
	}

	token := messageContext.GetBearerToken()
	if token == "" {
		return nil, fmt.Errorf("User not found")
	}

	return thiz.GetUser(token)
}
