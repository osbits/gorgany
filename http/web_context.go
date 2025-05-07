package http

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
)

type WebContext struct {
	router     core.Router
	msgFactory core.MessageFactory
	inpFactory core.InputResolverFactory

	controllers []core.IController
	middleware  []core.IMiddlewareConfig
	notFound    core.HandlerFunc
}

// Regular WebContext methods...

func (wc *WebContext) SetRouter(r core.Router)                         { wc.router = r }
func (wc *WebContext) GetRouter() core.Router                          { return wc.router }
func (wc *WebContext) SetNewMessage(f core.MessageFactory)             { wc.msgFactory = f }
func (wc *WebContext) GetNewMessage() core.MessageFactory              { return wc.msgFactory }
func (wc *WebContext) SetNewInputResolver(f core.InputResolverFactory) { wc.inpFactory = f }
func (wc *WebContext) GetNewInputResolver() core.InputResolverFactory  { return wc.inpFactory }

func (wc *WebContext) AddController(c core.IController) {
	wc.controllers = append(wc.controllers, c)
}
func (wc *WebContext) GetControllers() []core.IController { return wc.controllers }

func (wc *WebContext) AddMiddleware(reg core.IMiddlewareConfig) {
	wc.middleware = append(wc.middleware, reg)
}
func (wc *WebContext) GetMiddlewares() []core.IMiddlewareConfig {
	return wc.middleware
}

func (wc *WebContext) SetNotFound(h core.HandlerFunc) { wc.notFound = h }
func (wc *WebContext) GetNotFound() core.HandlerFunc  { return wc.notFound }
