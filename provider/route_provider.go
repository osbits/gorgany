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
		for _, routeConfig := range c.GetRoutes() {
			handler := routeConfig.Handler
			middlewares := routeConfig.Middlewares
			switch routeConfig.Method {
			case http.GET:
				thiz.router.Get(routeConfig.Path, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, handler, middlewares)
				})
				break
			case http.PUT:
				thiz.router.Put(routeConfig.Path, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, handler, middlewares)
				})
				break
			case http.DELETE:
				thiz.router.Delete(routeConfig.Path, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, handler, middlewares)
				})
				break
			case http.POST:
				thiz.router.Post(routeConfig.Path, func(w http2.ResponseWriter, r *http2.Request) {
					http.Dispatch(w, r, handler, middlewares)
				})
				break
			default:
				panic("Method is unsupported yet")
			}

		}
	}
}
