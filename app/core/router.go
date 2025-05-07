package core

import "net/http"

type IController interface {
	GetRoutes() []IRouteConfig
}

type Controllers []IController

func (thiz Controllers) AddController(controller IController) {
	thiz = append(thiz, controller)
}

type IMiddlewareConfig interface {
	GetPattern() string
	GetExcludePattern() string
	GetApplyOn404() bool
	GetMiddleware() IMiddleware
}

type IMiddlewareConfigBuilder interface {
	WithPattern(pattern string) IMiddlewareConfigBuilder
	WithExcludePattern(pattern string) IMiddlewareConfigBuilder
	WithApplyOn404(applyOn404 bool) IMiddlewareConfigBuilder
	WithMiddleware(mw IMiddleware) IMiddlewareConfigBuilder
	Build() IMiddlewareConfig
}

type IMiddleware interface {
	Handle(func(message HttpMessage)) func(message HttpMessage)
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
	GetPath() string
	GetMethod() Method
	GetHandler() HandlerFunc
	GetName() string
	GetNamespace() string
	GetMiddlewares() []IMiddleware
}
