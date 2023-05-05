package service

import (
	"github.com/golang-jwt/jwt/v5"
	"graecoFramework/model"
	"os"
	"time"
)

func NewJwtService() *JwtService {
	return &JwtService{}
}

type JwtService struct {
}

func (thiz JwtService) GenerateJwt(user model.Authenticable) (string, error) {
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
	t, _ := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(os.Getenv("JWT_SECRET_KEY")), nil
	})

	return t.Valid
}

func (thiz JwtService) ParseJwt(token string) (jwt.MapClaims, error) {
	t, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		return []byte(os.Getenv("JWT_SECRET_KEY")), nil
	})

	return t.Claims.(jwt.MapClaims), err
}

func (thiz JwtService) GetUser(token string) (model.Authenticable, error) {
	claims, err := thiz.ParseJwt(token)
	if err != nil {
		return nil, err
	}

	return GetAuthEntityService().GetByUsername(claims["user"].(string))
}
