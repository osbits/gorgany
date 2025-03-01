package http

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/service"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/go-chi/chi"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

func Dispatch(applicationContext core.IApplicationContext, w http.ResponseWriter, r *http.Request, handlerFunc core.HandlerFunc, middlewares []core.IMiddleware) {
	if originalPath := r.Context().Value(core.OriginalURLPathKey).(string); originalPath != "" {
		r.URL.Path = originalPath
		ctx := chi.RouteContext(r.Context())

		for i, v := range ctx.URLParams.Values {
			unescapedValue, err := url.PathUnescape(v)
			if err != nil {
				err2.HandleError(err)
				return
			}

			index := strings.Index(strings.ToLower(originalPath), strings.ToLower(unescapedValue))
			if index == -1 {
				err2.HandleError(fmt.Errorf("Mismatch between incoming parameters and original URL: %s, %v", originalPath, ctx.URLParams.Values))
				return
			}
			originalValue := originalPath[index : index+len(unescapedValue)]
			v = originalValue
			ctx.URLParams.Values[i] = v
		}
	}

	message := &Message{applicationContext: applicationContext}
	defer func() {
		err := message.Close()
		if err != nil {
			err2.HandleError(err)
		}
	}()

	err := service.GetContainer().Make(message, map[string]any{"writer": &ResponseWriterWrapper{
		Flusher:        w.(http.Flusher),
		Hijacker:       w.(http.Hijacker),
		ReaderFrom:     w.(io.ReaderFrom),
		ResponseWriter: w,
		StringWriter:   w.(io.StringWriter),
		Writer:         w.(io.Writer),
		StatusCode:     200,
	}, "request": r})
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

	handler := func(m core.HttpMessage) {
		reflectedHandler := reflect.ValueOf(handlerFunc)
		resolver := inputResolver{
			reflectedHandler: reflectedHandler,
			message:          m,
		}

		args, err := resolver.resolve()
		if err != nil {
			Catch(err, m)
			return
		}

		if handlerFunc == nil {
			return
		}

		reflectedHandler.Call(args)
	}

	middlewares = mergeMiddlewaresWithGlobal(applicationContext, middlewares)

	for i := len(middlewares) - 1; i >= 0; i-- {
		handler = middlewares[i].Handle(handler)
	}
	handler(message)
}

func mergeMiddlewaresWithGlobal(applicationContext core.IApplicationContext, handlerMiddlewares []core.IMiddleware) []core.IMiddleware {
	for _, middleware := range applicationContext.GetMiddlewares() {
		rtC := reflect.TypeOf(middleware)
		globalMiddlewareName := util.IndirectType(rtC).Name()

		overridden := util.InArrayFunc(handlerMiddlewares, func(el core.IMiddleware) bool {
			middlewareName := util.IndirectType(reflect.TypeOf(el)).Name()
			return globalMiddlewareName == middlewareName
		})

		if overridden {
			continue
		}

		handlerMiddlewares = util.Prepend(handlerMiddlewares, middleware)
	}

	return handlerMiddlewares
}
