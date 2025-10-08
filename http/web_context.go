package http

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
)

type WebContext struct {
	homeUrl    string
	msgFactory core.MessageFactory
	inpFactory core.InputResolverFactory

	controllers []core.IController
	middleware  []core.IMiddlewareConfig

	notFound core.HandlerFunc
}

func (wc *WebContext) SetHomeUrl(url string) {
	wc.homeUrl = url
}

func (wc *WebContext) GetHomeUrl() string {
	return wc.homeUrl
}

func (wc *WebContext) SetNewMessage(f core.MessageFactory) {
	wc.msgFactory = f
}

func (wc *WebContext) GetNewMessage() core.MessageFactory {
	return wc.msgFactory
}

func (wc *WebContext) SetNewInputResolver(f core.InputResolverFactory) {
	wc.inpFactory = f
}

func (wc *WebContext) GetNewInputResolver() core.InputResolverFactory {
	return wc.inpFactory
}

func (wc *WebContext) AddController(c core.IController) {
	wc.controllers = append(wc.controllers, c)
}

func (wc *WebContext) GetControllers() []core.IController {
	out := make([]core.IController, len(wc.controllers))
	copy(out, wc.controllers)
	return out
}

func (wc *WebContext) AddMiddleware(reg core.IMiddlewareConfig) {
	wc.middleware = append(wc.middleware, reg)
}
func (wc *WebContext) GetMiddlewares() []core.IMiddlewareConfig {
	out := make([]core.IMiddlewareConfig, len(wc.middleware))
	copy(out, wc.middleware)
	return out
}

func (wc *WebContext) SetNotFound(h core.HandlerFunc) {
	wc.notFound = h
}

func (wc *WebContext) GetNotFound() core.HandlerFunc {
	return wc.notFound
}
