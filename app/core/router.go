package core

import "net/http"

// IController defines the interface for web controllers
type IController interface {
	// GetRoutes returns the route configurations for this controller
	GetRoutes() []IRouteConfig
}

// Controllers represents a collection of controllers
type Controllers []IController

// AddController adds a new controller to the collection
func (thiz Controllers) AddController(controller IController) {
	thiz = append(thiz, controller)
}

// IMiddlewareConfig defines the interface for middleware configuration
type IMiddlewareConfig interface {
	// GetPattern returns the URL pattern this middleware applies to
	GetPattern() string
	// GetExcludePattern returns the URL pattern this middleware should not apply to
	GetExcludePattern() string
	// IsFilter returns whether this middleware is a filter
	IsFilter() bool
	// GetMiddleware returns the middleware implementation
	GetMiddleware() IMiddleware
}

// IMiddlewareConfigBuilder defines the interface for building middleware configurations
type IMiddlewareConfigBuilder interface {
	// WithPattern sets the URL pattern for the middleware
	WithPattern(pattern string) IMiddlewareConfigBuilder
	// WithExcludePattern sets the URL pattern to exclude from middleware
	WithExcludePattern(pattern string) IMiddlewareConfigBuilder
	// AsFilter marks the middleware as a filter
	AsFilter() IMiddlewareConfigBuilder
	// WithMiddleware sets the middleware implementation
	WithMiddleware(mw IMiddleware) IMiddlewareConfigBuilder
	// Build creates and returns the middleware configuration
	Build() IMiddlewareConfig
}

// IMiddleware defines the interface for HTTP middleware
type IMiddleware interface {
	// Handle wraps the next handler with middleware functionality
	Handle(func(message HttpMessage)) func(message HttpMessage)
}

// Router defines the interface for HTTP routing
type Router interface {
	http.Handler
	RouteLinker

	// Engine returns the underlying HTTP handler
	Engine() http.Handler
	// RegisterRoute registers a new route configuration
	RegisterRoute(config IRouteConfig)
	RegisterMiddleware(config IMiddlewareConfig)
}

// RouteLinker defines the interface for route URL generation
type RouteLinker interface {
	// UrlByName generates a URL for a named route with parameters
	UrlByName(name string, params map[string]any) string
	// UrlByNameSequence generates a URL for a named route with sequential parameters
	UrlByNameSequence(name string, params ...any) string
	// RouteByName retrieves a route configuration by name
	RouteByName(name string) IRouteConfig
}

// IRouteConfig defines the interface for route configuration
type IRouteConfig interface {
	// Pattern returns the URL pattern for this route
	Pattern() string
	// GetPath returns the path for this route
	GetPath() string
	// GetMethod returns the HTTP method for this route
	GetMethod() Method
	// GetHandler returns the handler function for this route
	GetHandler() HandlerFunc
	// GetName returns the name of this route
	GetName() string
	// GetNamespace returns the namespace for this route
	GetNamespace() string
	// GetMiddlewares returns the middleware chain for this route
	GetMiddlewares() []IMiddleware
}
