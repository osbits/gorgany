package http

import (
	"github.com/go-chi/chi"
	"net/http"
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
	CorsConfig  *CorsConfig
}

type CorsConfig struct {
	AllowedOrigins     []string
	AllowOriginFunc    func(r *http.Request, origin string) bool
	AllowedMethods     []string
	AllowedHeaders     []string
	ExposedHeaders     []string
	AllowCredentials   bool
	MaxAge             int
	OptionsPassthrough bool
}
