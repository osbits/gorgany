package middleware

import (
	"gorgany"
	"gorgany/http"
)

type AuthMiddleware struct {
	Roles []gorgany.UserRole
}

func (thiz *AuthMiddleware) Handle(message http.Message) bool {
	if !message.IsLoggedIn() {
		message.Redirect("/login", 302)
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

	//message.Response("", 403)
	return true//todo tmp solution, do not forget to uncomment it!!
}
