package http

import (
	"errors"
	"fmt"
	error2 "gorgany/error"
	"gorgany/service/dto"
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
			Catch(r, message)
		}
	}()

	for _, middleware := range defaultMiddlewares {
		middlewares = util.Prepend[IMiddleware](middlewares, middleware)
	}

	reflectedHandler := reflect.ValueOf(handler)
	resolver := inputResolver{
		reflectedHandler: reflectedHandler,
		message:          message,
	}

	args, err := resolver.resolve()
	if err != nil {
		processParsingErrors(err, &message)
		return
	}

	if preProcess(middlewares, message) {
		reflectedHandler.Call(args)
	}
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
	if message.IsApiNamespace() {
		message.ResponseJSON(dto.WrapPayload(nil, 500, nil), 200)
		return
	}
	//todo 500 view
	message.Response(fmt.Sprintf("Oops... 500 error.\n %v", err), 500)
}

func preProcess(middlewares []IMiddleware, message Message) bool {
	if len(middlewares) == 0 {
		return true
	}

	preProcessed := true
	for _, middleware := range middlewares {
		res := middleware.Handle(message)
		if res == false {
			preProcessed = false
			break
		}
	}

	return preProcessed
}

func processParsingErrors(err error, message *Message) {
	var paramError *error2.InputParamParseError
	if errors.As(err, &paramError) {
		if message.IsApiNamespace() {
			message.ResponseJSON(dto.WrapPayload(nil, 404, nil), 200)
			return
		}
		message.Response("", 404)
		return
	}

	var bodyError *error2.InputBodyParseError
	if errors.As(err, &bodyError) {
		error2.Catch(err)
		if message.IsApiNamespace() {
			message.ResponseJSON(dto.WrapPayload(nil, 400, nil), 200)
			return
		}
		message.Response("", 400)
		return
	}
}
