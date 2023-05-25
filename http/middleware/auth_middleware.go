package middleware

import (
	"gorgany"
	"gorgany/http"
)

type AuthMiddleware struct {
	Role []gorgany.UserRole
}

func (thiz *AuthMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(message http.Message) {
		if !message.IsLoggedIn() {
			message.Redirect("/login", 302)
			return
		}
		if thiz.Role == nil {
			next(message)
			return
		}
		user, err := message.CurrentUser()
		if err != nil {
			panic(err) //todo
		}
		for _, role := range thiz.Role {
			if role == user.GetRole() {
				next(message)
				return
			}
		}

		message.Response("", 403)
	}
}
