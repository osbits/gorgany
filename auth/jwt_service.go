package auth

import (
	"context"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"gorgany/proxy"
	"os"
	"time"
)

func NewJwtService() *JwtService {
	return &JwtService{}
}

type JwtService struct {
}

func (thiz JwtService) GenerateJwt(user proxy.Authenticable) (string, error) {
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

func (thiz JwtService) GetUser(token string) (proxy.Authenticable, error) {
	claims, err := thiz.ParseJwt(token)
	if err != nil {
		return nil, err
	}

	return GetAuthEntityService().GetByUsername(claims["user"].(string))
}

// ctx - context with gorgany/http.Message instance
func (thiz JwtService) CurrentUser(ctx context.Context) (proxy.Authenticable, error) {
	message := ctx.Value("message").(proxy.HttpMessage)
	token := message.GetBearerToken()
	if token == "" {
		return nil, fmt.Errorf("User not found")
	}

	return thiz.GetUser(token)
}
