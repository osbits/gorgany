package middleware

import (
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/auth"
	error2 "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/service/dto"
	"github.com/spf13/viper"
)

type JwtMiddleware struct {
	Roles []core.UserRole
}

func (thiz JwtMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		jwtService := auth.NewJwtService()

		token := message.Request().Header().Get("Authorization")
		// Remove "Bearer " prefix if present
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}

		if token == "" {
			panic(error2.NewJwtAuthError())
		}

		if !jwtService.ValidateJwt(token, viper.GetString("auth.jwt.secret")) {
			panic(error2.NewJwtAuthError())
		}

		if thiz.Roles == nil || len(thiz.Roles) == 0 {
			next(message)
			return
		}

		user, err := jwtService.GetUser(token, viper.GetString("auth.jwt.secret"))
		if err != nil {
			panic(error2.NewJwtAuthError())
		}

		for _, role := range thiz.Roles {
			if role == user.GetRole() {
				next(message)
				return
			}
		}

		message.Response().JSON(dto.ReturnObject(nil, core.ForbiddenHttpStatus, nil), 403)
		return
	}
}
