package http

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/service"
	"git.qix.sx/gorgany/gorgany.git/util"
	"net/http"
	"reflect"
)

func Dispatch(w http.ResponseWriter, r *http.Request, handler core.HandlerFunc, middlewares []core.IMiddleware) {
	if originalPath := r.Context().Value(core.OriginalURLPathKey).(string); originalPath != "" {
		r.URL.Path = originalPath
	}

	message := &Message{}
	defer func() {
		err := message.Close()
		if err != nil {
			err2.HandleError(err)
		}
	}()

	err := service.GetContainer().Make(message, map[string]any{"writer": w, "request": r})
	if err != nil {
		err2.HandleErrorWithStacktrace(err)
		w.WriteHeader(500)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			err, ok := r.(error)
			if !ok {
				err = fmt.Errorf("%v", r)
			}
			Catch(err, message)
		}
	}()

	middlewares = mergeMiddlewaresWithGlobal(middlewares)

	highPriorityMiddleware := util.FindAll(middlewares, func(el core.IMiddleware) bool {
		return el.Priority() == core.High
	})

	if !preProcess(highPriorityMiddleware, message) {
		return
	}

	reflectedHandler := reflect.ValueOf(handler)
	resolver := inputResolver{
		reflectedHandler: reflectedHandler,
		message:          message,
	}

	args, err := resolver.resolve()
	if err != nil {
		Catch(err, message)
		return
	}

	mediumPriorityMiddleware := util.FindAll(middlewares, func(el core.IMiddleware) bool {
		return el.Priority() == core.Medium
	})

	if !preProcess(mediumPriorityMiddleware, message) {
		return
	}

	if handler == nil {
		return
	}

	reflectedHandler.Call(args)

	// lowPriorityMiddleware todo: not implemented yet
}

func preProcess(middlewares []core.IMiddleware, message *Message) bool {
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

func mergeMiddlewaresWithGlobal(handlerMiddlewares []core.IMiddleware) []core.IMiddleware {
	for _, middleware := range internal.GetFrameworkRegistrar().GetMiddlewares() {
		rtC := reflect.TypeOf(middleware)
		corsMiddlewareName := util.IndirectType(rtC).Name()

		overridden := util.InArrayFunc(handlerMiddlewares, func(el core.IMiddleware) bool {
			middlewareName := util.IndirectType(reflect.TypeOf(el)).Name()
			return corsMiddlewareName == middlewareName
		})

		if overridden {
			continue
		}

		handlerMiddlewares = util.Prepend[core.IMiddleware](handlerMiddlewares, middleware)
	}

	return handlerMiddlewares
}
