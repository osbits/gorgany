package proxy

import "net/http"

var router Router

func GetRouter() Router {
	return router
}

func SetRouter(r Router) {
	router = r
}

type Router interface {
	UrlByName(name string, params map[string]any) string
	UrlByNameSequence(name string, params ...any) string
	Engine() http.Handler
	RegisterRoute(config IRouteConfig)
	RouteByName(name string) IRouteConfig
}

type IRouteConfig interface {
	Pattern() string
}
