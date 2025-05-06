package http

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"net/http"
)

type WebContext struct {
	homeUrl     string
	router      core.Router
	newMessage  func(w http.ResponseWriter, r *http.Request) (core.HttpMessage, error)
	controllers []core.IController
	middleware  []core.IMiddleware
	notFound    core.HandlerFunc
}

func (w *WebContext) GetHomeUrl() string {
	return w.homeUrl
}

func (w *WebContext) SetHomeUrl(url string) {
	w.homeUrl = url
}

func (w *WebContext) GetRouter() core.Router {
	return w.router
}

func (w *WebContext) SetRouter(router core.Router) {
	w.router = router
}

func (w WebContext) GetNewMessageFactory() func(w http.ResponseWriter, r *http.Request) (core.HttpMessage, error) {
	return w.newMessage
}

func (w *WebContext) SetNewMessage(newMessage func(w http.ResponseWriter, r *http.Request) (core.HttpMessage, error)) {
	w.newMessage = newMessage
}

func (w *WebContext) GetControllers() []core.IController {
	return w.controllers
}

func (w *WebContext) SetControllers(controllers []core.IController) {
	w.controllers = controllers
}

func (w *WebContext) AddController(controller core.IController) {
	w.controllers = append(w.controllers, controller)
}

func (w *WebContext) GetMiddlewares() []core.IMiddleware {
	return w.middleware
}

func (w *WebContext) SetMiddleware(middleware []core.IMiddleware) {
	w.middleware = middleware
}

func (w *WebContext) AddMiddleware(middleware core.IMiddleware) {
	w.middleware = append(w.middleware, middleware)
}

func (w *WebContext) GetNotFound() core.HandlerFunc {
	return w.notFound
}

func (w *WebContext) SetNotFound(notFound core.HandlerFunc) {
	w.notFound = notFound
}
