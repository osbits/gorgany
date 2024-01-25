package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/auth"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/service/dto"
	"github.com/spf13/viper"
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

	if !jwtService.ValidateJwt(token, viper.GetString("auth.jwt.secret")) {
		panic(error2.NewJwtAuthError())
	}

	if thiz.Roles == nil || len(thiz.Roles) == 0 {
		return true
	}

	user, err := jwtService.GetUser(token, viper.GetString("auth.jwt.secret"))
	if err != nil {
		panic(error2.NewJwtAuthError())
	}

	for _, role := range thiz.Roles {
		if role == user.GetRole() {
			return true
		}
	}

	message.ResponseJSON(dto.ReturnObject(nil, core.ForbiddenHttpStatus, nil), 200)
	return false
}
