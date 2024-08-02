package provider

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/http"
	middleware2 "git.qix.sx/gorgany/gorgany.git/http/middleware"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/service"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	http2 "net/http"
	"reflect"
	"strings"
)

type RouteProvider struct {
	router             core.Router
	applicationContext core.IApplicationContext
}

func NewRouteProvider() *RouteProvider {
	return &RouteProvider{}
}

func (thiz *RouteProvider) InitProvider(applicationContext core.IApplicationContext) {
	thiz.applicationContext = applicationContext

	thiz.RegisterRouter(router.NewGorganyRouter())
	thiz.caseSensitiveRoutes()
	thiz.supportEndSlash()
}

func (thiz *RouteProvider) RegisterRouter(router core.Router) {
	thiz.applicationContext.RegisterRouter(router)
	thiz.router = router
}

func (thiz *RouteProvider) RegisterController(controller core.IController) {
	if err := service.GetContainer().Make(controller); err != nil {
		err2.HandleError(err)
		return
	}

	availableLangsRegex := thiz.buildLangRegex()
	routerEngine := thiz.router.Engine().(chi.Router)

	caseSensitiveRoutes := viper.GetBool("app.server.caseSensitiveRoutes")

	for _, rc := range controller.GetRoutes() {
		routeConfig := rc.(*router.RouteConfig)
		handler := routeConfig.Handler

		reflectedHandler := reflect.TypeOf(handler)
		if reflectedHandler.Kind() != reflect.Func {
			reflectedController := reflect.TypeOf(controller)
			panic(fmt.Sprintf("Handler must be function. Controller: %s, route: %s", reflectedController.String(), routeConfig.Path))
		}

		middlewares := routeConfig.Middlewares

		thiz.router.RegisterRoute(routeConfig)
		route := routeConfig.Path
		if routeConfig.Namespace != "" {
			route = fmt.Sprintf("/{namespace:%s}%s", routeConfig.Namespace, route)
		}

		patterns := []string{route}

		if viper.GetBool("i18n.enabled") {
			patterns = append(patterns, fmt.Sprintf("/{lang:^(%s)?$}%s", availableLangsRegex, route))
		}

		for _, pattern := range patterns {
			if !caseSensitiveRoutes {
				pattern = strings.ToLower(pattern)
			}

			if pattern[len(pattern)-1] == '/' && len(pattern) > 1 {
				pattern = pattern[:len(pattern)-1]
			}

			thiz.addOptionsMethodToCheckPreflightCORS(pattern, handler, middlewares)

			switch routeConfig.Method {
			case core.GET:
				routerEngine.Get(pattern, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(thiz.applicationContext, w, r, handler, middlewares)
				})
				break
			case core.PUT:
				routerEngine.Put(pattern, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(thiz.applicationContext, w, r, handler, middlewares)
				})
				break
			case core.DELETE:
				routerEngine.Delete(pattern, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(thiz.applicationContext, w, r, handler, middlewares)
				})
				break
			case core.POST:
				routerEngine.Post(pattern, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(thiz.applicationContext, w, r, handler, middlewares)
				})
				break
			default:
				panic("Method is unsupported yet")
			}
		}
	}
}

func (thiz *RouteProvider) SetHomeUrl(url string) {
	internal.GetApplicationContext().(core.IWebApplicationContext).SetHomeUrl(url)
}

func (thiz *RouteProvider) RegisterMiddleware(middleware core.IMiddleware) {
	thiz.applicationContext.RegisterMiddleware(middleware)
}

func (thiz *RouteProvider) buildLangRegex() string {
	availableLangs := viper.GetStringSlice("i18n.lang.available")
	availableLangs = append(availableLangs, viper.GetString("i18n.lang.default"))

	return strings.Join(availableLangs, "|")
}

func (thiz *RouteProvider) caseSensitiveRoutes() {
	if !viper.GetBool("app.server.caseSensitiveRoutes") {
		thiz.router.Engine().(chi.Router).Use(func(next http2.Handler) http2.Handler {
			fn := func(w http2.ResponseWriter, r *http2.Request) {
				ctx := r.Context()
				ctx = context.WithValue(ctx, core.OriginalURLPathKey, r.URL.Path)
				r = r.WithContext(ctx)

				r.URL.Path = strings.ToLower(r.URL.Path)
				next.ServeHTTP(w, r)
			}
			return http2.HandlerFunc(fn)
		})
	}
}

func (thiz *RouteProvider) supportEndSlash() {
	thiz.router.Engine().(chi.Router).Use(func(next http2.Handler) http2.Handler {
		fn := func(w http2.ResponseWriter, r *http2.Request) {
			path := r.URL.Path
			if path[len(path)-1] == '/' {
				r.URL.Path = path[:len(path)-1]
			}

			next.ServeHTTP(w, r)
		}
		return http2.HandlerFunc(fn)
	})
}

func (thiz *RouteProvider) addOptionsMethodToCheckPreflightCORS(pattern string, handler any, middlewares []core.IMiddleware) {
	routerEngine := thiz.router.Engine().(chi.Router)
	var corsMiddleware core.IMiddleware

	findInMiddlewares := func(middlewares []core.IMiddleware) core.IMiddleware {
		for _, middleware := range middlewares {
			corsMiddlewareEmpty := &middleware2.Cors{}
			rtM := reflect.TypeOf(middleware)
			if rtM.AssignableTo(reflect.TypeOf(corsMiddlewareEmpty)) {
				return middleware
			}
		}
		return nil
	}

	corsMiddleware = findInMiddlewares(middlewares)
	if corsMiddleware == nil {
		corsMiddleware = findInMiddlewares(thiz.applicationContext.GetMiddlewares())
	}

	if corsMiddleware == nil {
		return
	}

	routerEngine.Options(pattern, func(w http2.ResponseWriter, r *http2.Request) {
		message := &http.Message{}
		err := service.GetContainer().Make(message, map[string]any{"writer": w, "request": r})
		if err != nil {
			err2.HandleErrorWithStacktrace(err)
			w.WriteHeader(500)
			return
		}

		corsMiddleware.Handle(message)
	})
}
