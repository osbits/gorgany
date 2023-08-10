package middleware

import (
	"gorgany/http/router"
	"gorgany/proxy"
)

type AuthMiddleware struct {
	Roles []proxy.UserRole
}

func (thiz *AuthMiddleware) Handle(message proxy.HttpMessage) bool {
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
