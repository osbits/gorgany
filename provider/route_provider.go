package provider

import (
	"github.com/go-chi/chi"
	"graecoFramework/http"
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

func test(i int, d float32, l RouteProvider) {

}

func (thiz RouteProvider) initRoutes() {
	for _, c := range FrameworkRegistrar.GetControllers() {
		routesConfig := c.GetRoutes()
		for _, routeConfig := range routesConfig {
			switch routeConfig.Method {
			case http.GET:
				thiz.router.Get(routeConfig.Path, routeConfig.Handler)
				break
			case http.PUT:
				thiz.router.Put(routeConfig.Path, routeConfig.Handler)
				break
			case http.DELETE:
				thiz.router.Delete(routeConfig.Path, routeConfig.Handler)
				break
			case http.POST:
				thiz.router.Post(routeConfig.Path, routeConfig.Handler)
				break
			default:
				thiz.router.Method(string(routeConfig.Method), routeConfig.Path, routeConfig.Handler)
				break
			}
		}
	}
}
