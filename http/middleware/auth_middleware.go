package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/service/dto"
)

type AuthMiddleware struct {
	Roles          []core.UserRole
	AuthStrategies []string
}

func (thiz AuthMiddleware) Handle(message core.HttpMessage) bool {
	if len(thiz.AuthStrategies) == 0 {
		thiz.AuthStrategies = append(thiz.AuthStrategies, core.DefaultKeyInRegistrar)
	}

	for _, strategy := range thiz.AuthStrategies {
		if !message.IsLoggedIn(strategy) {
			continue
		}
		if thiz.Roles == nil || len(thiz.Roles) == 0 {
			return true
		}

		user, err := message.CurrentUser(strategy)
		if err != nil {
			panic(err) //todo
		}
		for _, role := range thiz.Roles {
			if role == user.GetRole() {
				return true
			}
		}
	}

	if message.GetHeader().Get("Content-Type") == core.ApplicationJson || message.GetPathParam("namespace") == "api" {
		message.ResponseJSON(dto.ReturnObject(nil, core.NotAuthorizedHttpStatus, nil), 200)
		return false
	}
	message.Redirect(router.GetRouter().UrlByNameSequence("cp.login.show"), 302)
	return false
}
