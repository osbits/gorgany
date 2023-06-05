package middleware

import (
	"gorgany/auth/service"
	error2 "gorgany/error"
	"gorgany/http"
)

type JwtMiddleware struct {
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

	return true
}
