package middleware

import (
	"gorgany/auth/service"
	error2 "gorgany/error"
	"gorgany/http"
	"gorgany"
)

type JwtMiddleware struct {
	Roles []gorgany.UserRole
}

func (thiz JwtMiddleware) Handle(message http.Message) bool {
	jwtService := service.NewJwtService()

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
