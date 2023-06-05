package middleware

import (
	"gorgany/auth/service"
	"gorgany/http"
	"gorgany/service/dto"
)

type JwtMiddleware struct {
}

func (thiz JwtMiddleware) Handle(message http.Message) bool {
	jwtService := service.NewJwtService()

	token := message.GetBearerToken()
	if token == "" {
		message.ResponseJSON(dto.WrapPayload(nil, 401, nil), 401)
		return false
	}

	if !jwtService.ValidateJwt(token) {
		message.ResponseJSON(dto.WrapPayload(nil, 401, nil), 401)
		return false
	}

	return true
}
