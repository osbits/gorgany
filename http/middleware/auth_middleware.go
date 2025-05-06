package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/service/dto"
	"github.com/spf13/viper"
)

type AuthMiddleware struct {
	Roles          []core.UserRole
	AuthStrategies []string
	AuthContext    core.IAuthContext `container:"inject"`
}

func (thiz AuthMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		if len(thiz.AuthStrategies) == 0 {
			thiz.AuthStrategies = append(thiz.AuthStrategies, core.DefaultKeyInRegistrar)
		}

		for _, strategy := range thiz.AuthStrategies {
			if !thiz.AuthContext.Strategy(strategy).IsLoggedIn(message.Context()) {
				continue
			}
			if thiz.Roles == nil || len(thiz.Roles) == 0 {
				next(message)
				return
			}

			user, err := thiz.AuthContext.Strategy(strategy).CurrentUser(message.Context())
			if err != nil {
				panic(err) //todo
			}
			for _, role := range thiz.Roles {
				if role == user.GetRole() {
					next(message)
					return
				}
			}
		}

		if message.GetHeader().Get("Content-Type") == core.ApplicationJson.String() || message.GetPathParam("namespace") == "api" {
			message.ResponseJSON(dto.ReturnObject(nil, core.NotAuthorizedHttpStatus, nil), 401)
			return
		}
		loginFormUrl := viper.GetString("auth.login.formUrl")
		if loginFormUrl == "" {
			loginFormUrl = core.DefaultLoginUrl
		}
		message.Redirect(loginFormUrl, 302)
		return
	}
}
