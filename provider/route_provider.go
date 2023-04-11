package provider

import (
	"github.com/go-chi/chi"
	"graecoFramework/http"
	http2 "net/http"
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
	for _, c := range FrameworkRegistrar.GetControllers() {
		routesConfig := c.GetRoutes()
		for _, routeConfig := range routesConfig {
			handler := routeConfig.Handler
			switch routeConfig.Method {
			case http.GET:
				thiz.router.Get(routeConfig.Path, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, handler)
				})
				break
			case http.PUT:
				thiz.router.Put(routeConfig.Path, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, handler)
				})
				break
			case http.DELETE:
				thiz.router.Delete(routeConfig.Path, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, handler)
				})
				break
			case http.POST:
				thiz.router.Post(routeConfig.Path, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, handler)
				})
				break
			default:
				panic("Method is unsupported yet")
			}
		}
	}
}
