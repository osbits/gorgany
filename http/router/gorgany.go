package router

import (
	"fmt"
	"github.com/go-chi/chi"
	"gorgany/app/core"
	"net/http"
	"regexp"
	"strings"
)

func NewGorganyRouter() *GorganyRouter {
	return &GorganyRouter{
		eninge: chi.NewRouter(),
		routes: make(map[string]core.IRouteConfig),
	}
}

type GorganyRouter struct {
	eninge *chi.Mux
	routes map[string]core.IRouteConfig
}

func (thiz GorganyRouter) Engine() http.Handler {
	return thiz.eninge
}

func (thiz GorganyRouter) RegisterRoute(route core.IRouteConfig) {
	castRoute := route.(*RouteConfig)
	if castRoute.Name == "" {
		return
	}

	if thiz.routes == nil {
		thiz.routes = make(map[string]core.IRouteConfig)
	}
	thiz.routes[castRoute.Name] = castRoute
}

func (thiz GorganyRouter) UrlByName(name string, params map[string]any) string {
	route := GetRouter().RouteByName(name)
	if route == nil {
		return ""
	}

	return thiz.replaceRouteSegments(route.Pattern(), params)
}

func (thiz GorganyRouter) UrlByNameSequence(name string, params ...any) string {
	route := GetRouter().RouteByName(name)
	if route == nil {
		return ""
	}

	return thiz.replaceRouteSegmentsSequence(route.Pattern(), params...)
}

func (thiz GorganyRouter) RouteByName(name string) core.IRouteConfig {
	return thiz.routes[name]
}

func (thiz GorganyRouter) replaceRouteSegments(routePattern string, params map[string]any) string {
	r := regexp.MustCompile(`{([^}]+)(:[^}]+)?}`)

	result := r.ReplaceAllStringFunc(routePattern, func(match string) string {
		index := strings.IndexByte(match, ':')
		if index == -1 {
			index = len(match) - 1
		}
		paramName := match[1:index]
		if value, ok := params[paramName]; ok {
			return fmt.Sprintf("%v", value)
		}
		panic(fmt.Errorf("Expected parameter '%s' for pattern '%s' was not found", paramName, routePattern))
	})

	return result
}

func (thiz GorganyRouter) replaceRouteSegmentsSequence(routePattern string, params ...any) string {
	r := regexp.MustCompile(`{([^}]+)(:[^}]+)?}`)

	paramIndex := -1
	result := r.ReplaceAllStringFunc(routePattern, func(match string) string {
		paramIndex++
		index := strings.IndexByte(match, ':')
		if index == -1 {
			index = len(match) - 1
		}
		paramName := match[1:index]
		if len(params) < paramIndex {
			panic(fmt.Errorf("Expected parameter '%s' for pattern '%s' was not found", paramName, routePattern))
		}

		p := params[paramIndex]
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

func (thiz RouteConfig) Pattern() string {
	url := thiz.Path
	if thiz.Namespace != "" {
		url = fmt.Sprintf("/%s%s", thiz.Namespace, url)
	}
	return url
}
