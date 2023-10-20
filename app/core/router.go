package core

import "net/http"

type IController interface {
	GetRoutes() []IRouteConfig
}

type Controllers []IController

func (thiz Controllers) AddController(controller IController) {
	thiz = append(thiz, controller)
}

type IMiddleware interface {
	Handle(message HttpMessage) bool
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
