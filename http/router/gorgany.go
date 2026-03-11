package router

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/util"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	grghttp "git.qix.sx/gorgany/gorgany.git/http"
	"github.com/go-chi/chi"
)

var (
	placeholderRE = regexp.MustCompile(`\{([^{}]+)\}`)
	alnumRE       = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

type ChiRouterAdapter struct {
	engine      chi.Router
	webCtx      core.IWebContext `container:"inject"`
	namedRoutes map[string]core.IRouteConfig
}

func (r *ChiRouterAdapter) Init() {
	r.engine = chi.NewRouter()
	r.namedRoutes = make(map[string]core.IRouteConfig)

	r.engine.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			wrapper := &grghttp.ResponseWriterWrapper{
				Flusher:        w.(http.Flusher),
				Hijacker:       w.(http.Hijacker),
				ReaderFrom:     w.(io.ReaderFrom),
				ResponseWriter: w,
				StringWriter:   w.(io.StringWriter),
				Writer:         w.(io.Writer),
				StatusCode:     200,
			}
			msg, err := r.webCtx.GetNewMessage()(wrapper, req)
			if err != nil {
				w.WriteHeader(500)
				return
			}

			ctx := context.WithValue(req.Context(), core.FullMessageInstanceContextKey, msg)
			defer msg.Close()
			next.ServeHTTP(wrapper, req.WithContext(ctx))
		})
	})

	r.engine.NotFound(func(w http.ResponseWriter, req *http.Request) {
		msg, _ := r.webCtx.GetNewMessage()(w, req)
		if nf := r.webCtx.GetNotFound(); nf != nil {
			reflect.ValueOf(nf).Call([]reflect.Value{reflect.ValueOf(msg)})
		} else {
			msg.Response().Bytes(nil, 404)
		}
	})
}

func (r *ChiRouterAdapter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.engine.ServeHTTP(w, req)
}

func (r *ChiRouterAdapter) RegisterMiddleware(mw core.IMiddlewareConfig) {
	r.webCtx.AddMiddleware(mw)
	if mw.IsFilter() {
		r.engine.Use(r.adaptFilter(mw))
	}
}

func (r *ChiRouterAdapter) RegisterRoute(rc core.IRouteConfig) {
	pattern := rc.Pattern()
	method := string(rc.GetMethod())

	var mws []func(http.Handler) http.Handler

	for _, cfg := range r.webCtx.GetMiddlewares() {
		if cfg.IsFilter() {
			continue
		}

		isAnyExcludeSuitable := util.ContainsByClosure(cfg.GetExcludePatterns(), func(exclude string) bool {
			return matchesPattern(exclude, pattern)
		})

		if matchesPattern(cfg.GetPattern(), pattern) && !isAnyExcludeSuitable {
			mws = append(mws, r.adaptRouteMiddleware(cfg))
		}
	}

	for _, mw := range rc.GetMiddlewares() {
		cfg := grghttp.NewMiddlewareConfigBuilder().
			WithMiddleware(mw).
			WithPattern(pattern).
			Build()
		r.webCtx.AddMiddleware(cfg)
		mws = append(mws, r.adaptRouteMiddleware(cfg))
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		msgRaw := req.Context().Value(core.FullMessageInstanceContextKey)

		var msg core.HttpMessage
		var ok bool

		if msg, ok = msgRaw.(core.HttpMessage); !ok {
			grghttp.Catch(fmt.Errorf("route: %s, Message not found in context", req.URL.Path), msg)
		}

		resolver, err := r.webCtx.GetNewInputResolver()(rc.GetHandler(), msg)
		if err != nil {
			grghttp.Catch(err, msg)
			return
		}
		args, err := resolver.(*grghttp.InputResolver).Resolve()
		if err != nil {
			grghttp.Catch(err, msg)
			return
		}
		reflect.ValueOf(rc.GetHandler()).Call(args)
	})

	r.engine.
		With(mws...).
		MethodFunc(method, pattern, h)
	r.engine.
		With(mws...).
		Options(pattern, h)
	r.namedRoutes[rc.GetName()] = rc
}

func (r *ChiRouterAdapter) adaptFilter(cfg core.IMiddlewareConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			path := req.URL.Path

			isAnyExcludeSuitable := util.ContainsByClosure(cfg.GetExcludePatterns(), func(exclude string) bool {
				return matches(exclude, path)
			})
			if matches(cfg.GetPattern(), path) && !isAnyExcludeSuitable {
				msgRaw := req.Context().Value(core.FullMessageInstanceContextKey)

				var msg core.HttpMessage
				var ok bool

				if msg, ok = msgRaw.(core.HttpMessage); !ok {
					err.HandleError(fmt.Errorf("route: %s, Message not found in context", req.URL.Path))
					w.WriteHeader(500)
					return
				}

				cfg.GetMiddleware().Handle(func(_ core.HttpMessage) {
					next.ServeHTTP(w, req)
				})(msg)
			} else {
				next.ServeHTTP(w, req)
			}
		})
	}
}

func (r *ChiRouterAdapter) adaptRouteMiddleware(cfg core.IMiddlewareConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			msgRaw := req.Context().Value(core.FullMessageInstanceContextKey)

			var msg core.HttpMessage
			var ok bool

			if msg, ok = msgRaw.(core.HttpMessage); !ok {
				err.HandleError(fmt.Errorf("route: %s, Message not found in context", req.URL.Path))
				w.WriteHeader(500)
				return
			}

			cfg.GetMiddleware().Handle(func(_ core.HttpMessage) {
				next.ServeHTTP(w, req)
			})(msg)
		})
	}
}

func normalizeRoutePattern(routePattern string) string {
	return placeholderRE.ReplaceAllStringFunc(routePattern, func(ph string) string {
		content := ph[1 : len(ph)-1]
		parts := strings.SplitN(content, ":", 2)
		if len(parts) == 2 {
			regex := parts[1]
			if alnumRE.MatchString(regex) {
				return regex
			}
			return "*"
		}
		return "*"
	})
}

func matchesPattern(pattern, routePattern string) bool {
	if pattern == "" || pattern == "/**" {
		return true
	}

	normalized := normalizeRoutePattern(routePattern)

	if strings.HasSuffix(pattern, "**") {

		prefix := strings.TrimSuffix(pattern[:len(pattern)-2], "/")
		return normalized == prefix || strings.HasPrefix(normalized, prefix+"/")
	}

	return pattern == normalized
}

func matches(pattern, path string) bool {
	if pattern == "" {
		return false
	}

	if pattern == "/**" {
		return true
	}

	if len(pattern) > 2 && strings.HasSuffix(pattern, "**") {
		return strings.HasPrefix(path, pattern[:len(pattern)-2])
	}
	return pattern == path
}

func (thiz *ChiRouterAdapter) Engine() http.Handler {
	return thiz.engine
}

func (thiz *ChiRouterAdapter) UrlByName(name string, params map[string]any) string {
	route := thiz.RouteByName(name)
	if route == nil {
		return "/"
	}

	return thiz.replaceRouteSegments(route.Pattern(), params)
}

func (thiz *ChiRouterAdapter) UrlByNameSequence(name string, params ...any) string {
	route := thiz.RouteByName(name)
	if route == nil {
		return "/"
	}

	return thiz.replaceRouteSegmentsSequence(route.Pattern(), params...)
}

func (thiz *ChiRouterAdapter) RouteByName(name string) core.IRouteConfig {
	return thiz.namedRoutes[name]
}

func (thiz *ChiRouterAdapter) replaceRouteSegments(routePattern string, params map[string]any) string {
	r := regexp.MustCompile(`{([^}]+)(:[^}]+)?}`)

	result := r.ReplaceAllStringFunc(routePattern, func(match string) string {
		index := strings.IndexByte(match, ':')
		defaultValue := ""
		if index == -1 {
			index = len(match) - 1
		} else {
			defaultValue = match[index+1 : len(match)-1]
		}
		paramName := match[1:index]
		if value, ok := params[paramName]; ok {
			return fmt.Sprintf("%v", value)
		} else if defaultValue != "" {
			return defaultValue
		}
		panic(fmt.Errorf("Expected parameter '%s' for pattern '%s' was not found", paramName, routePattern))
	})

	return result
}

func (thiz *ChiRouterAdapter) replaceRouteSegmentsSequence(routePattern string, params ...any) string {
	r := regexp.MustCompile(`{([^}]+)(:[^}]+)?}`)

	paramIndex := -1
	result := r.ReplaceAllStringFunc(routePattern, func(match string) string {
		paramIndex++
		index := strings.IndexByte(match, ':')
		defaultValue := ""
		if index == -1 {
			index = len(match) - 1
		} else {
			defaultValue = match[index+1 : len(match)-1]
		}
		paramName := match[1:index]
		if len(params) < paramIndex {
			panic(fmt.Errorf("Expected parameter '%s' for pattern '%s' was not found", paramName, routePattern))
		}

		p := params[paramIndex]
		if p == nil && defaultValue != "" {
			return defaultValue
		} else if p == nil {
			panic(fmt.Errorf("Expected parameter '%s' for pattern '%s' was not found", paramName, routePattern))
		}
		return fmt.Sprintf("%v", p)
	})

	return result
}

type RouteConfig struct {
	Path        string
	Method      core.Method
	Handler     core.HandlerFunc
	Middlewares []core.IMiddleware
	Namespace   string
	Name        string
}

func (thiz RouteConfig) GetPath() string {
	return thiz.Path
}

func (thiz RouteConfig) GetMethod() core.Method {
	return thiz.Method
}

func (thiz RouteConfig) GetHandler() core.HandlerFunc {
	return thiz.Handler
}

func (thiz RouteConfig) GetMiddlewares() []core.IMiddleware {
	return thiz.Middlewares
}

func (thiz RouteConfig) GetName() string {
	return thiz.Name
}

func (thiz RouteConfig) GetNamespace() string {
	return thiz.Namespace
}

func (thiz RouteConfig) Pattern() string {
	url := thiz.Path
	if thiz.Namespace != "" {
		url = fmt.Sprintf("/{namespace:%s}%s", thiz.Namespace, url)
	}
	return url
}
