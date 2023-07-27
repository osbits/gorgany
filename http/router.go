package http

import (
	"github.com/go-chi/chi"
)

var router *chi.Mux

func init() {
	if router != nil {
		return
	}
	router = chi.NewRouter()
}

func GetRouter() *chi.Mux {
	return router
}

type RouteConfig struct {
	Path        string
	Method      Method
	Handler     HandlerFunc
	Middlewares []IMiddleware
}
