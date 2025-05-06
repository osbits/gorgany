package http

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"github.com/go-chi/chi"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

func Dispatch(
	wc core.IWebContext,
	w http.ResponseWriter,
	r *http.Request,
	handler core.HandlerFunc,
) {
	if orig := r.Context().Value(core.OriginalURLPathKey); orig != nil {
		if s, ok := orig.(string); ok && s != "" {
			r.URL.Path = s
			fixParams(r)
		}
	}

	msg, err := wc.GetNewMessageFactory()(&ResponseWriterWrapper{
		Flusher:        w.(http.Flusher),
		Hijacker:       w.(http.Hijacker),
		ReaderFrom:     w.(io.ReaderFrom),
		ResponseWriter: w,
		StringWriter:   w.(io.StringWriter),
		Writer:         w.(io.Writer),
		StatusCode:     200,
	}, r)
	if err != nil {
		err2.HandleError(fmt.Errorf("route: %s, error: %v", r.URL.Path, err))
		w.WriteHeader(500)
		return
	}

	defer func() {
		if r := recover(); r != nil {
			err, ok := r.(error)
			if !ok {
				err = fmt.Errorf("%v", r)
			}
			Catch(err, msg)
		}

		if err := msg.Close(); err != nil {
		}
	}()

	final := func(m core.HttpMessage) {
		callHandler(handler, m)
	}
	for i := len(wc.GetMiddlewares()) - 1; i >= 0; i-- {
		final = wc.GetMiddlewares()[i].Handle(final)
	}

	final(msg)
}

func fixParams(r *http.Request) {
	ctx := chi.RouteContext(r.Context())
	for i, v := range ctx.URLParams.Values {
		uv, _ := url.PathUnescape(v)
		idx := strings.Index(strings.ToLower(r.URL.Path), strings.ToLower(uv))
		if idx >= 0 {
			ctx.URLParams.Values[i] = r.URL.Path[idx : idx+len(uv)]
		}
	}
}

func callHandler(handler core.HandlerFunc, m core.HttpMessage) {
	defer func() {
		if rec := recover(); rec != nil {
			if err, ok := rec.(error); ok {
				err2.HandleErrorWithStacktrace(err)
				m.Response("", 500)
				return
			}
		}
	}()

	fnVal := reflect.ValueOf(handler)
	resolver := &inputResolver{reflectedHandler: fnVal, message: m}
	args, err := resolver.resolve()
	if err != nil {
		err2.HandleErrorWithStacktrace(err)
		m.Response("", 500)
		return
	}
	fnVal.Call(args)
}
