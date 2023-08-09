package provider

import (
	"fmt"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"gorgany/http"
	"gorgany/proxy"
	http2 "net/http"
	"reflect"
	"strings"
)

type RouteProvider struct {
	router proxy.Router
}

func NewRouteProvider() *RouteProvider {
	return &RouteProvider{}
}

func (thiz RouteProvider) InitProvider() {
	proxy.SetRouter(http.NewGorganyRouter())
	thiz.router = proxy.GetRouter()

	if len(FrameworkRegistrar.GetControllers()) == 0 {
		panic("You did`nt create any controllers.")
	}

	http.SetDefaultMiddlewares(FrameworkRegistrar.middlewares)

	thiz.initRoutes()
}

func (thiz RouteProvider) initRoutes() {
	availableLangsRegex := thiz.buildLangRegex()
	routerEngine := thiz.router.Engine().(chi.Router)
	for _, c := range FrameworkRegistrar.GetControllers() {
		for _, routeConfig := range c.GetRoutes() {
			handler := routeConfig.Handler

			reflectedHandler := reflect.TypeOf(handler)
			if reflectedHandler.Kind() != reflect.Func {
				reflectedController := reflect.TypeOf(c)
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
				if pattern[len(pattern)-1] == '/' && len(pattern) > 1 {
					pattern = pattern[:len(pattern)-1]
				}

				routerEngine.Options(pattern, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, nil, middlewares)
				})

				switch routeConfig.Method {
				case http.GET:
					routerEngine.Get(pattern, func(w http2.ResponseWriter, r *http2.Request) {
						http.Dispatch(w, r, handler, middlewares)
					})
					break
				case http.PUT:
					routerEngine.Put(pattern, func(w http2.ResponseWriter, r *http2.Request) {
						http.Dispatch(w, r, handler, middlewares)
					})
					break
				case http.DELETE:
					routerEngine.Delete(pattern, func(w http2.ResponseWriter, r *http2.Request) {
						http.Dispatch(w, r, handler, middlewares)
					})
					break
				case http.POST:
					routerEngine.Post(pattern, func(w http2.ResponseWriter, r *http2.Request) {
						http.Dispatch(w, r, handler, middlewares)
					})
					break
				default:
					panic("Method is unsupported yet")
				}
			}
		}
	}
}

func (thiz RouteProvider) buildLangRegex() string {
	availableLangs := viper.GetStringSlice("i18n.lang.available")
	availableLangs = append(availableLangs, viper.GetString("i18n.lang.default"))

	return strings.Join(availableLangs, "|")
}
