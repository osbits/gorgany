package middleware

import (
	"gorgany/app/core"
	"gorgany/auth"
	error2 "gorgany/error"
	"gorgany/service/dto"
)

type JwtMiddleware struct {
	Roles []core.UserRole
}

func (thiz JwtMiddleware) Handle(message core.HttpMessage) bool {
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

	message.ResponseJSON(dto.WrapPayload(nil, 403, nil), 200)
	return false
}
