package middleware

import (
	"graecoFramework/auth/service"
	"graecoFramework/http"
)

type JwtMiddleware struct {
}

func (thiz JwtMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(message http.Message) {
		jwtService := service.NewJwtService()

		token := message.GetBearerToken()
		if token == "" {
			message.ResponseJSON("Unauthorized", 401)
			return
		}

		if !jwtService.ValidateJwt(token) {
			message.ResponseJSON("Unauthorized", 401)
			return
		}

		next(message)
	}
}
