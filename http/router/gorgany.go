package router

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"github.com/go-chi/chi"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

func NewGorganyRouter() *GorganyRouter {
	return &GorganyRouter{
		engine:       chi.NewRouter(),
		routesByName: make(map[string]core.IRouteConfig),
	}
}

type GorganyRouter struct {
	engine       *chi.Mux
	routesByName map[string]core.IRouteConfig

	// Route cache for performance optimization
	routeCache      map[string]core.IRouteConfig
	routeCacheMutex sync.RWMutex

	// Pre-compiled patterns for faster matching
	compiledPatterns      map[string]*regexp.Regexp
	compiledPatternsMutex sync.RWMutex
}

func (thiz *GorganyRouter) Engine() http.Handler {
	return thiz.engine
}

func (thiz *GorganyRouter) SetEntryPoint(entryPoint http.Handler) {
	thiz.engine = chi.NewRouter()
	thiz.engine.Mount("/", entryPoint)
}

func (thiz *GorganyRouter) RegisterRoute(route core.IRouteConfig) {
	if route.GetName() == "" {
		return
	}

	if thiz.routesByName == nil {
		thiz.routesByName = make(map[string]core.IRouteConfig)
	}
	thiz.routesByName[route.GetName()] = route
}

func (thiz *GorganyRouter) UrlByName(name string, params map[string]any) string {
	route := thiz.RouteByName(name)
	if route == nil {
		return "/"
	}

	return thiz.replaceRouteSegments(route.Pattern(), params)
}

func (thiz *GorganyRouter) UrlByNameSequence(name string, params ...any) string {
	route := thiz.RouteByName(name)
	if route == nil {
		return "/"
	}

	return thiz.replaceRouteSegmentsSequence(route.Pattern(), params...)
}

func (thiz *GorganyRouter) RouteByName(name string) core.IRouteConfig {
	return thiz.routesByName[name]
}

func (thiz *GorganyRouter) replaceRouteSegments(routePattern string, params map[string]any) string {
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

func (thiz *GorganyRouter) LookupRoute(method, path string) (routeConfig core.IRouteConfig, found bool) {
	// Check cache first
	cacheKey := method + ":" + path
	thiz.routeCacheMutex.RLock()
	if entry, found := thiz.routeCache[cacheKey]; found {
		thiz.routeCacheMutex.RUnlock()
		return entry, true
	}
	thiz.routeCacheMutex.RUnlock()

	for _, r := range thiz.routesByName {
		if strings.EqualFold(string(r.GetMethod()), method) && thiz.pathMatch(r.Pattern(), path) {
			// Cache the result for future requests
			thiz.routeCacheMutex.Lock()
			if thiz.routeCache == nil {
				thiz.routeCache = make(map[string]core.IRouteConfig)
			}
			thiz.routeCache[cacheKey] = r
			thiz.routeCacheMutex.Unlock()

			return r, true
		}
	}

	return nil, false
}

// pathMatch determines if a path matches a pattern, with optimized pattern handling
func (wc *GorganyRouter) pathMatch(pattern, path string) bool {
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
func (wc *GorganyRouter) getCompiledPattern(pattern string) *regexp.Regexp {
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

// Initialize should be called after all routesByName are added
func (thiz *GorganyRouter) CompilePatterns() {
	// Pre-compile patterns for all routesByName
	thiz.compiledPatterns = make(map[string]*regexp.Regexp)
	thiz.routeCache = make(map[string]core.IRouteConfig)

	for _, r := range thiz.routesByName {
		pattern := r.Pattern()
		if strings.Contains(pattern, "{") || strings.Contains(pattern, "*") {
			thiz.getCompiledPattern(pattern)
		}
	}
}

func (thiz *GorganyRouter) replaceRouteSegmentsSequence(routePattern string, params ...any) string {
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

func (thiz RouteConfig) GetPath() string {
	return thiz.Path
}

func (thiz RouteConfig) GetMethod() core.Method {
	return thiz.Method
}

func (thiz RouteConfig) GetHandler() core.HandlerFunc {
	return thiz.Handler
}

func (thiz RouteConfig) GetMiddlewares() []core.IMiddleware {
	return thiz.Middlewares
}

func (thiz RouteConfig) GetName() string {
	return thiz.Name
}

func (thiz RouteConfig) GetNamespace() string {
	return thiz.Namespace
}

func (thiz RouteConfig) Pattern() string {
	url := thiz.Path
	if thiz.Namespace != "" {
		url = fmt.Sprintf("/{namespace:%s}%s", thiz.Namespace, url)
	}
	return url
}
