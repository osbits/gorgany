package auth

import (
	"context"
	"fmt"
	"github.com/golang-jwt/jwt/v5"
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/util"
	"github.com/spf13/viper"
	"time"
)

func NewJwtService() *JwtService {
	return &JwtService{}
}

type JwtService struct {
	userService core.IUserService `container:"inject"`
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
	claims, ok := t.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("jwt: unexpected claims type %T", t.Claims)
	}
	return claims, err
}

func (thiz JwtService) GetUser(token string, secret string) (core.Authenticable, error) {
	claims, err := thiz.ParseJwt(token, secret)
	if err != nil {
		return nil, err
	}

	// A validly-signed token whose `username` claim is absent or not a string used
	// to panic here. The claim set is attacker-influenced — anything that can obtain
	// a signature can choose the claims — so this must be a rejection, not a crash.
	username, ok := claims["username"].(string)
	if !ok {
		return nil, fmt.Errorf("jwt: token has no usable 'username' claim (got %T)", claims["username"])
	}
	if username == "" {
		return nil, fmt.Errorf("jwt: token has an empty 'username' claim")
	}

	if thiz.userService == nil {
		return nil, fmt.Errorf("jwt: no user service is wired, so a token cannot be resolved to a user")
	}

	return thiz.userService.GetByUsername(username)
}

// CurrentUser
// ctx - instance of core.IMessageContext
func (thiz JwtService) CurrentUser(ctx context.Context, secret string) (core.Authenticable, error) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Context is not IMessageContext instance")
	}

	token := util.ParseBearerToken(messageContext.GetHeader().Get("Authorization"))
	if token == "" {
		return nil, fmt.Errorf("User not found")
	}

	return thiz.GetUser(token, secret)
}
