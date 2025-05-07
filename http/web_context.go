// http/web_context.go
package http

import (
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"regexp"
	"strings"
	"sync"

	"git.qix.sx/gorgany/gorgany.git/app/core"
)

type WebContext struct {
	router     core.Router
	msgFactory core.MessageFactory
	inpFactory core.InputResolverFactory

	controllers []core.IController
	middleware  []core.IMiddlewareConfig
	notFound    core.HandlerFunc

	// Route cache for performance optimization
	routeCache      map[string]core.IRouteConfig
	routeCacheMutex sync.RWMutex

	// Pre-compiled patterns for faster matching
	compiledPatterns      map[string]*regexp.Regexp
	compiledPatternsMutex sync.RWMutex
}

// Regular WebContext methods...

func (wc *WebContext) SetRouter(r core.Router)                         { wc.router = r }
func (wc *WebContext) GetRouter() core.Router                          { return wc.router }
func (wc *WebContext) SetNewMessage(f core.MessageFactory)             { wc.msgFactory = f }
func (wc *WebContext) GetNewMessage() core.MessageFactory              { return wc.msgFactory }
func (wc *WebContext) SetNewInputResolver(f core.InputResolverFactory) { wc.inpFactory = f }
func (wc *WebContext) GetNewInputResolver() core.InputResolverFactory  { return wc.inpFactory }

func (wc *WebContext) AddController(c core.IController) {
	wc.controllers = append(wc.controllers, c)
}
func (wc *WebContext) GetControllers() []core.IController { return wc.controllers }

func (wc *WebContext) AddMiddleware(reg core.IMiddlewareConfig) {
	wc.middleware = append(wc.middleware, reg)
}
func (wc *WebContext) GetMiddlewares() []core.IMiddlewareConfig {
	return wc.middleware
}

func (wc *WebContext) SetNotFound(h core.HandlerFunc) { wc.notFound = h }
func (wc *WebContext) GetNotFound() core.HandlerFunc  { return wc.notFound }

// LookupRoute finds a matching route for the given method and path
func (wc *WebContext) LookupRoute(method, path string) (core.IRouteConfig, bool) {
	// Check cache first
	cacheKey := method + ":" + path
	wc.routeCacheMutex.RLock()
	if entry, found := wc.routeCache[cacheKey]; found {
		wc.routeCacheMutex.RUnlock()
		return entry, true
	}
	wc.routeCacheMutex.RUnlock()

	// Try to find a matching route
	for _, ctrl := range wc.controllers {
		for _, r := range ctrl.GetRoutes() {
			cfg := r.(*router.RouteConfig)
			if strings.EqualFold(string(cfg.Method), method) && wc.pathMatch(cfg.Pattern(), path) {
				// Cache the result for future requests
				wc.routeCacheMutex.Lock()
				if wc.routeCache == nil {
					wc.routeCache = make(map[string]core.IRouteConfig)
				}
				wc.routeCache[cacheKey] = cfg
				wc.routeCacheMutex.Unlock()

				return cfg, true
			}
		}
	}

	return nil, false
}

// pathMatch determines if a path matches a pattern, with optimized pattern handling
func (wc *WebContext) pathMatch(pattern, path string) bool {
	// Exact match (fastest path)
	if pattern == path {
		return true
	}

	// For patterns with parameters or wildcards
	if strings.Contains(pattern, "{") || strings.Contains(pattern, "*") {
		// Get or compile the regex for this pattern
		regex := wc.getCompiledPattern(pattern)
		if regex != nil {
			return regex.MatchString(path)
		}
	}

	return false
}

// getCompiledPattern returns a cached regex pattern or compiles a new one
func (wc *WebContext) getCompiledPattern(pattern string) *regexp.Regexp {
	// Check if we already have this pattern compiled
	wc.compiledPatternsMutex.RLock()
	regex, exists := wc.compiledPatterns[pattern]
	wc.compiledPatternsMutex.RUnlock()

	if exists {
		return regex
	}

	// Compile the pattern
	var regexStr string

	if strings.Contains(pattern, "{") && strings.Contains(pattern, "}") {
		// Parameter pattern like /users/{id}
		regexPattern := regexp.MustCompile(`\{([^{}]+)\}`)
		regexStr = "^" + regexPattern.ReplaceAllString(pattern, `([^/]+)`) + "$"
	} else if strings.HasSuffix(pattern, "/*") {
		// Wildcard pattern like /api/*
		basePattern := strings.TrimSuffix(pattern, "/*")
		regexStr = "^" + regexp.QuoteMeta(basePattern) + "(/.*)?$"
	} else {
		return nil // Not a pattern that needs regex
	}

	compiledRegex, err := regexp.Compile(regexStr)
	if err != nil {
		return nil
	}

	// Store the compiled pattern
	wc.compiledPatternsMutex.Lock()
	if wc.compiledPatterns == nil {
		wc.compiledPatterns = make(map[string]*regexp.Regexp)
	}
	wc.compiledPatterns[pattern] = compiledRegex
	wc.compiledPatternsMutex.Unlock()

	return compiledRegex
}

// Initialize should be called after all routes are added
func (wc *WebContext) CacheRoutes() {
	// Pre-compile patterns for all routes
	wc.compiledPatterns = make(map[string]*regexp.Regexp)
	wc.routeCache = make(map[string]core.IRouteConfig)

	for _, ctrl := range wc.controllers {
		for _, r := range ctrl.GetRoutes() {
			cfg := r.(*router.RouteConfig)
			pattern := cfg.Pattern()
			if strings.Contains(pattern, "{") || strings.Contains(pattern, "*") {
				wc.getCompiledPattern(pattern)
			}
		}
	}
}
