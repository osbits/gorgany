package provider

import (
	"fmt"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"gorgany/http"
	http2 "net/http"
	"reflect"
	"strings"
)

type RouteProvider struct {
	router *chi.Mux
}

func NewRouteProvider() *RouteProvider {
	return &RouteProvider{}
}

func (thiz RouteProvider) InitProvider() {
	thiz.router = http.GetRouter()

	if len(FrameworkRegistrar.GetControllers()) == 0 {
		panic("You did`nt create any controllers.")
	}

	http.SetDefaultMiddlewares(FrameworkRegistrar.middlewares)

	thiz.initRoutes()
}

func (thiz RouteProvider) initRoutes() {
	availableLangsRegex := thiz.buildLangRegex()

	for _, c := range FrameworkRegistrar.GetControllers() {
		for _, routeConfig := range c.GetRoutes() {
			handler := routeConfig.Handler

			reflectedHandler := reflect.TypeOf(handler)
			if reflectedHandler.Kind() != reflect.Func {
				reflectedController := reflect.TypeOf(c)
				panic(fmt.Sprintf("Handler must be function. Controller: %s, route: %s", reflectedController.String(), routeConfig.Path))
			}

			middlewares := routeConfig.Middlewares

			route := routeConfig.Path
			if routeConfig.Namespace != "" {
				route = fmt.Sprintf("/%s%s", routeConfig.Namespace, route)
			}

			patterns := []string{route}

			if viper.GetBool("i18n.enabled") {
				patterns = append(patterns, fmt.Sprintf("/{lang:^(%s)?$}%s", availableLangsRegex, route))
			}

			for _, pattern := range patterns {
				if pattern[len(pattern)-1] == '/' && len(pattern) > 1 {
					pattern = pattern[:len(pattern)-1]
				}

				thiz.router.Options(pattern, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, nil, middlewares)
				})

				switch routeConfig.Method {
				case http.GET:
					thiz.router.Get(pattern, func(w http2.ResponseWriter, r *http2.Request) {
						http.Dispatch(w, r, handler, middlewares)
					})
					break
				case http.PUT:
					thiz.router.Put(pattern, func(w http2.ResponseWriter, r *http2.Request) {
						http.Dispatch(w, r, handler, middlewares)
					})
					break
				case http.DELETE:
					thiz.router.Delete(pattern, func(w http2.ResponseWriter, r *http2.Request) {
						http.Dispatch(w, r, handler, middlewares)
					})
					break
				case http.POST:
					thiz.router.Post(pattern, func(w http2.ResponseWriter, r *http2.Request) {
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
