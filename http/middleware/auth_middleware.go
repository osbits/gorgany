package middleware

import (
	"gorgany"
	"gorgany/http"
)

type AuthMiddleware struct {
	Role []gorgany.UserRole
}

func (thiz *AuthMiddleware) Handle(message http.Message) bool {
	if !message.IsLoggedIn() {
		message.Redirect("/login", 302)
		return false
	}
	if thiz.Role == nil {
		return true
	}
	user, err := message.CurrentUser()
	if err != nil {
		panic(err) //todo
	}
	for _, role := range thiz.Role {
		if role == user.GetRole() {
			return true
		}
	}

	message.Response("", 403)
	return false
}
