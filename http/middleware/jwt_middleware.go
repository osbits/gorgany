package middleware

import (
	"gorgany/auth/service"
	"gorgany/http"
	"gorgany/service/dto"
)

type JwtMiddleware struct {
}

func (thiz JwtMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(message http.Message) {
		jwtService := service.NewJwtService()

		token := message.GetBearerToken()
		if token == "" {
			message.ResponseJSON(dto.WrapPayload(nil, 401, nil), 401)
			return
		}

		if !jwtService.ValidateJwt(token) {
			message.ResponseJSON(dto.WrapPayload(nil, 401, nil), 401)
			return
		}

		next(message)
	}
}
