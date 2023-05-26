package http

import (
	"fmt"
	error2 "gorgany/error"
	"gorgany/service/dto"
	"gorgany/util"
	"net/http"
)

type IMiddleware interface {
	Handle(handlerFunc HandlerFunc) HandlerFunc
}

var defaultMiddlewares []IMiddleware

func SetDefaultMiddlewares(middlewares []IMiddleware) {
	defaultMiddlewares = middlewares
}

type HandlerFunc func(message Message)

func Dispatch(w http.ResponseWriter, r *http.Request, handler HandlerFunc, middlewares []IMiddleware) {
	message := Message{
		writer:  w,
		request: r,
	}

	defer func() {
		if r := recover(); r != nil {
			Catch(r, message)
		}
	}()

	for _, middleware := range defaultMiddlewares {
		middlewares = util.Prepend[IMiddleware](middlewares, middleware)
	}

	if len(middlewares) == 0 {
		handler(message)
		return
	}

	h := middlewares[len(middlewares)-1].Handle(handler)

	for i := len(middlewares) - 2; i >= 0; i-- {
		h = middlewares[i].Handle(h)
	}
	h(message)
}

func Catch(err any, message Message) {
	concreteError, ok := err.(error2.ValidationErrors)
	if ok {
		req := message.GetRequest()
		if message.IsApiNamespace() {
			message.ResponseJSON(dto.WrapPayload(nil, 422, concreteError.Errors), 200)
			return
		}
		message.RedirectWithParams(req.Referer(), 301, map[string]any{"validation": concreteError.Errors})
		return
	}
	error2.Catch(err)
	//todo 500 view
	message.Response(fmt.Sprintf("Oops... 500 error.\n %v", err), 500)
}
