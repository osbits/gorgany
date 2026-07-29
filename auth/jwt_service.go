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

// RoleClaim is the claim GenerateJwt writes the user's role into.
//
// It is informational, not authoritative. See RoleFromClaims.
const RoleClaim = "role"

func (thiz JwtService) GenerateJwt(user core.Authenticable, secret string) (string, error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", fmt.Errorf("jwt: unexpected claims type %T on a freshly minted token", token.Claims)
	}

	jwtLifeTime := viper.GetInt("auth.jwt.lifeTime")
	claims["exp"] = time.Now().Add(time.Duration(jwtLifeTime) * time.Second).Unix()
	claims["username"] = user.GetUsername()
	claims[RoleClaim] = string(user.GetRole())

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

// RoleFromClaims reads the role claim from an already-verified token.
//
// The claim exists so a client can render its own UI — show an admin menu, hide a
// button — without a round trip, and so a log line can name the role without a lookup.
//
// It is deliberately **not** used for authorisation. A signed token is immutable for its
// whole lifetime, so a role baked into one outlives a role change until the token
// expires: demote an admin and they stay an admin for up to auth.jwt.lifeTime. The
// user-service lookup in JwtMiddleware remains the authority precisely because it sees
// the current role, which is what makes revocation work. That costs a lookup per
// role-guarded request, and the alternative costs correctness.
//
// An app that wants to skip the lookup can read this claim itself and accept the
// staleness window knowingly. The framework will not make that trade silently.
func RoleFromClaims(claims jwt.MapClaims) (core.UserRole, bool) {
	raw, present := claims[RoleClaim]
	if !present {
		return "", false
	}

	role, isString := raw.(string)
	if !isString || role == "" {
		return "", false
	}
	return core.UserRole(role), true
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
