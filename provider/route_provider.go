package provider

import (
	"fmt"
	"github.com/go-chi/chi"
	"github.com/spf13/viper"
	"gorgany/http"
	http2 "net/http"
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

	thiz.initRoutes()
}

func (thiz RouteProvider) initRoutes() {
	availableLangsRegex := thiz.buildLangRegex()
	for _, c := range FrameworkRegistrar.GetControllers() {
		for _, routeConfig := range c.GetRoutes() {
			handler := routeConfig.Handler
			middlewares := routeConfig.Middlewares

			patterns := []string{routeConfig.Path}

			if viper.GetBool("i18n.enabled") {
				patterns = append(patterns, fmt.Sprintf("/{lang:^(%s)?$}%s", availableLangsRegex, routeConfig.Path))
			}

			for _, pattern := range patterns {
				if pattern[len(pattern)-1] == '/' && len(pattern) > 1 {
					pattern = pattern[:len(pattern)-1]
				}

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
