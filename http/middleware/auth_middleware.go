package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/http/router"
)

type AuthMiddleware struct {
	Roles []core.UserRole
}

func (thiz *AuthMiddleware) Handle(message core.HttpMessage) bool {
	if !message.IsLoggedIn() {
		message.Redirect(router.GetRouter().UrlByNameSequence("cp.login.show"), 302)
		return false
	}
	if thiz.Roles == nil || len(thiz.Roles) == 0 {
		return true
	}

	user, err := message.CurrentUser()
	if err != nil {
		panic(err) //todo
	}
	for _, role := range thiz.Roles {
		if role == user.GetRole() {
			return true
		}
	}

	message.Response("", 403)
	return false
}
