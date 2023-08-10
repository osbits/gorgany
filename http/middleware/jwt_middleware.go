package middleware

import (
	"gorgany/auth"
	error2 "gorgany/error"
	"gorgany/proxy"
)

type JwtMiddleware struct {
	Roles []proxy.UserRole
}

func (thiz JwtMiddleware) Handle(message proxy.HttpMessage) bool {
	jwtService := auth.NewJwtService()

	token := message.GetBearerToken()
	if token == "" {
		panic(error2.NewJwtAuthError())
	}

	if !jwtService.ValidateJwt(token) {
		panic(error2.NewJwtAuthError())
	}

	if thiz.Roles == nil || len(thiz.Roles) == 0 {
		return true
	}

	user, err := jwtService.GetUser(token)
	if err != nil {
		panic(error2.NewJwtAuthError())
	}

	for _, role := range thiz.Roles {
		if role == user.GetRole() {
			return true
		}
	}

	return false
}
