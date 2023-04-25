package middleware

import (
	"graecoFramework/http"
)

type AuthMiddleware struct {
}

func (thiz AuthMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(message http.Message) {
		if !message.IsLoggedIn() {
			message.Redirect("/login", 301)
			return
		}
		next(message)
	}
}
