// http/dispatch.go
package http

import (
	"context"
	"fmt"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"github.com/go-chi/chi"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"regexp"
)

type Dispatcher struct {
	webContext core.IWebContext `container:"inject"`
}

func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method, path := r.Method, r.URL.Path

	routeConfig, found := d.webContext.GetRouter().LookupRoute(method, path)

	// Create and set chi route context
	rctx := chi.NewRouteContext()
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	// Extract and add URL parameters
	if found {
		d.extractURLParams(r, path, routeConfig)
	}

	regs := d.webContext.GetMiddlewares()
	chain := make([]core.IMiddleware, 0, len(regs)+len(routeConfig.GetMiddlewares()))
	for _, reg := range regs {
		if matches(reg.GetPattern(), path) && !matches(reg.GetExcludePattern(), path) && (found || reg.GetApplyOn404()) {
			chain = append(chain, reg.GetMiddleware())
		}
	}
	//chain = append(chain, localMW...)

	final := func(msg core.HttpMessage) {
		if found {
			callHandler(d.webContext, routeConfig.GetHandler(), msg)
		} else if nf := d.webContext.GetNotFound(); nf != nil {
			reflect.ValueOf(nf).Call([]reflect.Value{reflect.ValueOf(msg)})
		} else {
			msg.Response("", 404)
		}
	}

	for i := len(chain) - 1; i >= 0; i-- {
		final = chain[i].Handle(final)
	}

	msg, err := d.webContext.GetNewMessage()(&ResponseWriterWrapper{
		Flusher:        w.(http.Flusher),
		Hijacker:       w.(http.Hijacker),
		ReaderFrom:     w.(io.ReaderFrom),
		ResponseWriter: w,
		StringWriter:   w.(io.StringWriter),
		Writer:         w.(io.Writer),
		StatusCode:     200,
	}, r)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	defer msg.Close()

	final(msg)
}

// extractURLParams extracts URL parameters and adds them to the chi context
func (d *Dispatcher) extractURLParams(r *http.Request, path string, routeConfig core.IRouteConfig) {
	// Find the matching route pattern that generated this handler
	pattern := routeConfig.Pattern()

	if pattern != "" && strings.Contains(pattern, "{") {
		rctx := chi.RouteContext(r.Context())

		// Parse parameters from pattern like /users/{id} or /users/{id:[0-9]+}
		pathSegments := strings.Split(strings.TrimPrefix(path, "/"), "/")
		patternSegments := strings.Split(strings.TrimPrefix(pattern, "/"), "/")

		for i, patternSeg := range patternSegments {
			if i < len(pathSegments) && strings.HasPrefix(patternSeg, "{") && strings.HasSuffix(patternSeg, "}") {
				// Extract parameter name and constraint (if any)
				paramContent := patternSeg[1 : len(patternSeg)-1]
				paramName := paramContent
				paramPattern := ""

				// Check if there's a constraint pattern
				if colonIdx := strings.Index(paramContent, ":"); colonIdx >= 0 {
					paramName = paramContent[:colonIdx]
					paramPattern = paramContent[colonIdx+1:]
				}

				paramValue := pathSegments[i]

				// Verify the parameter matches its constraint if specified
				if paramPattern != "" {
					patternRegex, err := regexp.Compile("^" + paramPattern + "$")
					if err == nil && patternRegex.MatchString(paramValue) {
						rctx.URLParams.Add(paramName, paramValue)
					}
				} else {
					// No constraint, just add the parameter
					rctx.URLParams.Add(paramName, paramValue)
				}
			}
		}
	}
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

func callHandler(wc core.IWebContext, handler core.HandlerFunc, m core.HttpMessage) {
	defer func() {
		if rec := recover(); rec != nil {
			if err, ok := rec.(error); ok {
				err2.HandleErrorWithStacktrace(err)
				m.Response("", 500)
				return
			}
		}
	}()

	resolverFactory := wc.GetNewInputResolver()
	if resolverFactory == nil {
		err2.HandleError(fmt.Sprintf("path: %s, err: inputResolverFactory is not initialized", m.GetRequest().URL))
		return
	}

	resolver, err := resolverFactory(handler, m)
	if err != nil {
		err2.HandleErrorWithStacktrace(err)
		m.Response("", 500)
		return
	}

	inputResolver, ok := resolver.(*InputResolver)
	if !ok {
		err2.HandleErrorWithStacktrace(fmt.Errorf("dispatch: resolver is not an InputResolver"))
		m.Response("", 500)
		return
	}

	args, err := inputResolver.Resolve()
	if err != nil {
		err2.HandleErrorWithStacktrace(err)
		m.Response("", 500)
		return
	}
	reflect.ValueOf(handler).Call(args)
}

func matches(pattern, path string) bool {
	if pattern == "/**" {
		return true
	}
	if len(pattern) > 2 && pattern[len(pattern)-2:] == "**" {
		return path[:len(pattern)-2] == pattern[:len(pattern)-2]
	}
	return pattern == path
}
