package provider

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	grgerr "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/http"
	"git.qix.sx/gorgany/gorgany.git/http/middleware"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/service"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	gohttp "net/http"
	"reflect"
	"strings"
)

type RouteProvider struct {
	router             core.Router
	notFoundHandler    core.HandlerFunc
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
	thiz.registerDefaultNotFound()
}

func (thiz *RouteProvider) RegisterRouter(router core.Router) {
	thiz.applicationContext.RegisterRouter(router)
	thiz.router = router
}

func (thiz *RouteProvider) RegisterController(controller core.IController) {
	if err := service.GetContainer().Make(controller); err != nil {
		grgerr.HandleError(err)
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

			thiz.addOptionsMethodToCheckPreflightCORS(pattern, middlewares)

			switch routeConfig.Method {
			case core.GET:
				routerEngine.Get(pattern, func(w gohttp.ResponseWriter, r *gohttp.Request) {
					http.Dispatch(thiz.applicationContext, w, r, handler, middlewares)
				})
				break
			case core.PUT:
				routerEngine.Put(pattern, func(w gohttp.ResponseWriter, r *gohttp.Request) {
					http.Dispatch(thiz.applicationContext, w, r, handler, middlewares)
				})
				break
			case core.DELETE:
				routerEngine.Delete(pattern, func(w gohttp.ResponseWriter, r *gohttp.Request) {
					http.Dispatch(thiz.applicationContext, w, r, handler, middlewares)
				})
				break
			case core.POST:
				routerEngine.Post(pattern, func(w gohttp.ResponseWriter, r *gohttp.Request) {
					http.Dispatch(thiz.applicationContext, w, r, handler, middlewares)
				})
				break
			default:
				panic("Method is unsupported yet")
			}
		}
	}
}

func (thiz *RouteProvider) SetNotFoundHandler(handlerFunc core.HandlerFunc) {
	thiz.notFoundHandler = handlerFunc
}

func (thiz *RouteProvider) SetHomeUrl(url string) {
	internal.GetApplicationContext().(core.IApplicationContext).SetHomeUrl(url)
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
		thiz.router.Engine().(chi.Router).Use(func(next gohttp.Handler) gohttp.Handler {
			fn := func(w gohttp.ResponseWriter, r *gohttp.Request) {
				ctx := r.Context()
				ctx = context.WithValue(ctx, core.OriginalURLPathKey, r.URL.Path)
				r = r.WithContext(ctx)

				r.URL.Path = strings.ToLower(r.URL.Path)
				next.ServeHTTP(w, r)
			}
			return gohttp.HandlerFunc(fn)
		})
	}
}

func (thiz *RouteProvider) supportEndSlash() {
	thiz.router.Engine().(chi.Router).Use(func(next gohttp.Handler) gohttp.Handler {
		fn := func(w gohttp.ResponseWriter, r *gohttp.Request) {
			path := r.URL.Path
			if len(path) > 0 && path[len(path)-1] == '/' {
				r.URL.Path = path[:len(path)-1]
			}

			next.ServeHTTP(w, r)
		}
		return gohttp.HandlerFunc(fn)
	})
}

func (thiz *RouteProvider) addOptionsMethodToCheckPreflightCORS(pattern string, middlewares []core.IMiddleware) {
	routerEngine := thiz.router.Engine().(chi.Router)
	var corsMiddleware core.IMiddleware

	findInMiddlewares := func(middlewares []core.IMiddleware) core.IMiddleware {
		for _, m := range middlewares {
			corsMiddlewareEmpty := &middleware.Cors{}
			rtM := reflect.TypeOf(m)
			if rtM.AssignableTo(reflect.TypeOf(corsMiddlewareEmpty)) {
				return m
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

	routerEngine.Options(pattern, func(w gohttp.ResponseWriter, r *gohttp.Request) {
		message := &http.Message{}
		err := service.GetContainer().Make(message, map[string]any{"writer": w, "request": r})
		if err != nil {
			grgerr.HandleErrorWithStacktrace(err)
			w.WriteHeader(500)
			return
		}

		handler := corsMiddleware.Handle(func(message core.HttpMessage) {
			message.Response("", gohttp.StatusOK)
		})

		handler(message)
	})
}

func (thiz *RouteProvider) registerDefaultNotFound() {
	thiz.notFoundHandler = func(message core.HttpMessage) {
		message.Response("NOT FOUND", 404)
	}

	thiz.router.Engine().(chi.Router).NotFound(func(w gohttp.ResponseWriter, r *gohttp.Request) {
		http.Dispatch(thiz.applicationContext, w, r, thiz.notFoundHandler, nil)
	})
}
