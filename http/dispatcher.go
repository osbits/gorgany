package http

import (
	"fmt"
	"gorgany/util"
	"net/http"
	"reflect"
)

type IMiddleware interface {
	Handle(message Message) bool
}

var defaultMiddlewares []IMiddleware

func SetDefaultMiddlewares(middlewares []IMiddleware) {
	defaultMiddlewares = middlewares
}

type HandlerFunc any

func Dispatch(w http.ResponseWriter, r *http.Request, handler HandlerFunc, middlewares []IMiddleware) {
	message := Message{
		writer:  w,
		request: r,
	}

	defer func() {
		if r := recover(); r != nil {
			err, ok := r.(error)
			if !ok {
				err = fmt.Errorf("%v", r)
			}
			Catch(err, &message)
		}
	}()

	fmt.Println("Debug: ", message.GetBodyContent(), r.URL.String())

	for _, middleware := range defaultMiddlewares {
		middlewares = util.Prepend[IMiddleware](middlewares, middleware)
	}

	if !preProcess(middlewares, message) {
		message.Response("nil", 400)
		return
	}

	if handler == nil {
		return
	}

	reflectedHandler := reflect.ValueOf(handler)
	resolver := inputResolver{
		reflectedHandler: reflectedHandler,
		message:          message,
	}

	args, err := resolver.resolve()
	if err != nil {
		Catch(err, &message)
		return
	}

	reflectedHandler.Call(args)
}

func preProcess(middlewares []IMiddleware, message Message) bool {
	if len(middlewares) == 0 {
		return true
	}

	preProcessed := true
	for _, middleware := range middlewares {
		res := middleware.Handle(message)
		fmt.Printf("Middleware: %s, result: %v", reflect.TypeOf(middleware).Name(), res)
		if res == false {
			preProcessed = false
			break
		}
	}

	return preProcessed
}
