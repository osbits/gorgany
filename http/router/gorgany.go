package router

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/util"

	"github.com/go-chi/chi"
	"github.com/osbits/gorgany/v2/app/core"
	grghttp "github.com/osbits/gorgany/v2/http"
)

var (
	placeholderRE = regexp.MustCompile(`\{([^{}]+)\}`)
	alnumRE       = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

type ChiRouterAdapter struct {
	engine      chi.Router
	webCtx      core.IWebContext `container:"inject"`
	namedRoutes map[string]core.IRouteConfig

	// allowedMethods tracks the methods registered per pattern, so the OPTIONS
	// responder can report an accurate Allow header.
	allowedMethods map[string][]string

	// concreteRoutes is a second routing trie holding every registered route whose pattern
	// is *not* a trailing-wildcard catch-all.
	//
	// It exists to answer one question the main engine cannot: when a request produces a
	// 405, is there a real route at that path under some other method, or did a catch-all
	// claim the path and merely fail to claim the method? chi resolves `/*` for GET, so
	// mounting SpaController turned every `DELETE /api/nope` into a 405 — telling an API
	// client the resource exists and the verb is wrong, when in fact nothing is there.
	//
	// The main engine cannot be asked, because the catch-all is in it: Match(GET, "/api/nope")
	// is true there. And chi does not populate RoutePattern() in the MethodNotAllowed
	// handler, so the matched pattern is not available either. A parallel trie holding only
	// the concrete routes answers it exactly, using chi's own matching rather than a
	// reimplementation of it.
	concreteRoutes chi.Router
	// concreteRegistered dedupes concreteRoutes registrations by method and pattern.
	concreteRegistered map[string]bool
	// preflightRegistered records the patterns that already have an OPTIONS
	// responder, so it is installed once per pattern rather than once per route.
	preflightRegistered map[string]bool
}

func (r *ChiRouterAdapter) Init() {
	r.engine = chi.NewRouter()
	r.namedRoutes = make(map[string]core.IRouteConfig)
	r.allowedMethods = make(map[string][]string)
	r.preflightRegistered = make(map[string]bool)
	r.concreteRoutes = chi.NewRouter()
	r.concreteRegistered = make(map[string]bool)

	r.engine.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// These optional interfaces are asserted with the two-value form. The
			// unchecked form panicked on any ResponseWriter that did not implement
			// all four — which includes httptest.ResponseRecorder and any writer
			// wrapped by an upstream middleware, so a compression or metrics
			// middleware in front of the router took every request down.
			wrapper := &grghttp.ResponseWriterWrapper{
				ResponseWriter: w,
				Writer:         w,
				StatusCode:     200,
			}
			if flusher, ok := w.(http.Flusher); ok {
				wrapper.Flusher = flusher
			}
			if hijacker, ok := w.(http.Hijacker); ok {
				wrapper.Hijacker = hijacker
			}
			if readerFrom, ok := w.(io.ReaderFrom); ok {
				wrapper.ReaderFrom = readerFrom
			}
			if stringWriter, ok := w.(io.StringWriter); ok {
				wrapper.StringWriter = stringWriter
			}
			msg, err := r.webCtx.GetNewMessage()(wrapper, req)
			if err != nil {
				w.WriteHeader(500)
				return
			}

			ctx := context.WithValue(req.Context(), core.FullMessageInstanceContextKey, msg)
			defer msg.Close()
			next.ServeHTTP(wrapper, req.WithContext(ctx))
		})
	})

	r.engine.NotFound(func(w http.ResponseWriter, req *http.Request) {
		msg, err := r.webCtx.GetNewMessage()(w, req)
		if err != nil || msg == nil {
			// No message means no way to negotiate or to reach an app handler. The
			// status is still the honest one.
			w.WriteHeader(http.StatusNotFound)
			return
		}

		r.writeNotFound(msg)
	})

	// chi answers an unmatched method with a bare 405 and no body. The router registers
	// every route under OPTIONS as well as its declared method, so a 405 here means a
	// real method mismatch, and an API client deserves to be told which one.
	r.engine.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		msg, err := r.webCtx.GetNewMessage()(w, req)
		if err != nil || msg == nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		// A catch-all route claims every path under it for the one method it declares, so
		// chi reports 405 for every *other* method on every path that has no real route —
		// which is how mounting SpaController's `GET /*` turned the whole app's
		// `DELETE /api/nope` into "the resource exists, wrong verb". H1 made the SPA's own
		// GET answer the negotiated 404; this is the same divergence on the verbs the SPA
		// never sees, and it cannot be fixed inside the controller because the request
		// never reaches it.
		//
		// A genuine mismatch — GET is registered for this exact path, the client sent
		// DELETE — still answers 405, now with the Allow header RFC 9110 requires and the
		// previous implementation omitted entirely.
		allowed := r.methodsForPath(req.URL.Path)
		if len(allowed) == 0 {
			// Through writeNotFound, not writeNegotiatedError: this *is* a 404, so an app
			// that registered SetNotFoundHandler must get its own handler here too.
			// Answering the framework default from this branch would give one app two
			// different 404s depending on the verb that produced it.
			r.writeNotFound(msg)
			return
		}

		msg.Response().SetHeader("Allow", strings.Join(allowed, ", "))
		writeNegotiatedError(msg, core.MethodNotAllowedHttpStatus,
			fmt.Sprintf("Method %s is not allowed for this route; allowed: %s",
				req.Method, strings.Join(allowed, ", ")))
	})
}

// writeNotFound answers a 404 the one way this router answers 404s.
//
// It exists because there are now two places that produce one: chi's NotFound, and the
// MethodNotAllowed branch where only a catch-all claimed the path. Both must honour an app's
// SetNotFoundHandler, or an app would see its own 404 for a GET and the framework's for a
// DELETE.
//
// The default used to be Bytes(nil, 404) — an empty body with no content type — so an API
// client hitting an unknown route got nothing it could parse and never the standard
// envelope (C2).
func (r *ChiRouterAdapter) writeNotFound(msg core.HttpMessage) {
	if nf := r.webCtx.GetNotFound(); nf != nil {
		reflect.ValueOf(nf).Call([]reflect.Value{reflect.ValueOf(msg)})
		return
	}

	writeNegotiatedError(msg, core.NotFoundHttpStatus, "No route matches this request")
}

// probeMethods are the methods methodsForPath asks chi about. It is chi's own methodMap
// keyed set; anything outside it makes Match return false regardless of the tree.
var probeMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodConnect, http.MethodOptions,
	http.MethodTrace,
}

// isCatchAllPattern reports whether pattern claims every path beneath it.
//
// Only a trailing wildcard counts. A `{param}` segment matches exactly one segment, so
// `/users/{id}` is a real route for a real resource and a 405 on it is honest.
func isCatchAllPattern(pattern string) bool {
	return pattern == "*" || pattern == "/*" || strings.HasSuffix(pattern, "/*")
}

// methodsForPath returns the methods registered for the concrete route matching path,
// sorted and including OPTIONS, or nil when no concrete route matches it.
//
// nil is the interesting answer: it means the only thing that claimed this path was a
// catch-all, so there is no resource there and 405 would be a lie. See concreteRoutes.
func (r *ChiRouterAdapter) methodsForPath(path string) []string {
	if r.concreteRoutes == nil {
		return nil
	}

	for _, method := range probeMethods {
		rctx := chi.NewRouteContext()
		if !r.concreteRoutes.Match(rctx, method, path) {
			continue
		}

		// Match populates the pattern it matched, which is the key allowedMethods is
		// built under — so the Allow header names what the app actually registered
		// rather than only the method that happened to probe first.
		allowed := append([]string{}, r.allowedMethods[rctx.RoutePattern()]...)
		if len(allowed) == 0 {
			allowed = []string{method}
		}
		if !util.InArray(http.MethodOptions, allowed) {
			allowed = append(allowed, http.MethodOptions)
		}
		sort.Strings(allowed)
		return allowed
	}

	return nil
}

// registerConcrete mirrors a non-catch-all route into concreteRoutes.
//
// The handler is never invoked — only the trie's shape matters — so it is a no-op rather
// than the real one, and no middleware is attached.
func (r *ChiRouterAdapter) registerConcrete(method, pattern string) {
	if r.concreteRoutes == nil {
		return
	}
	if isCatchAllPattern(pattern) {
		return
	}

	key := method + " " + pattern
	if r.concreteRegistered[key] {
		return
	}
	r.concreteRegistered[key] = true

	r.concreteRoutes.MethodFunc(method, pattern, func(http.ResponseWriter, *http.Request) {})
}

// writeNegotiatedError delegates to grghttp.WriteNegotiatedError.
//
// The implementation moved there in H1, when SpaController needed the identical response
// for its own refusals: its `/*` catch-all shadows the 404 above for every GET, so the two
// have to agree or an app's error shape depends on whether a SPA is mounted. Keeping a
// second copy here is what that move existed to prevent.
func writeNegotiatedError(message core.HttpMessage, status core.HttpStatus, reason string) {
	grghttp.WriteNegotiatedError(message, status, reason)
}

func (r *ChiRouterAdapter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.engine.ServeHTTP(w, req)
}

func (r *ChiRouterAdapter) RegisterMiddleware(mw core.IMiddlewareConfig) {
	r.webCtx.AddMiddleware(mw)
	if mw.IsFilter() {
		r.engine.Use(r.adaptFilter(mw))
	}
}

func (r *ChiRouterAdapter) RegisterRoute(rc core.IRouteConfig) {
	pattern := rc.Pattern()
	method := string(rc.GetMethod())

	// Reject a handler whose parameters cannot be resolved, at boot, rather than
	// serving 500s for it. A DTO that does not implement core.HttpCommand, or whose
	// ContentType() has no body parser, used to reach the request path and panic
	// there — once on a nil parser dereference, once through reflect.Call with too
	// few arguments. Both were developer errors in a type declaration, discoverable
	// only in production.
	if err := grghttp.ValidateHandlerParameters(rc.GetHandler()); err != nil {
		panic(fmt.Errorf("route %s %s (%s): %w", method, pattern, rc.GetName(), err))
	}

	var mws []func(http.Handler) http.Handler

	for _, cfg := range r.webCtx.GetMiddlewares() {
		if cfg.IsFilter() {
			continue
		}

		isAnyExcludeSuitable := util.ContainsByClosure(cfg.GetExcludePatterns(), func(exclude string) bool {
			return matchesPattern(exclude, pattern)
		})

		if matchesPattern(cfg.GetPattern(), pattern) && !isAnyExcludeSuitable {
			mws = append(mws, r.adaptRouteMiddleware(cfg))
		}
	}

	// Route-scoped middlewares are attached to this route and this route only.
	//
	// They used to also be published into the shared webCtx list keyed by this
	// route's pattern, which meant the loop above re-matched them on every
	// subsequent registration whose method-agnostic pattern was the same. Register
	// GET /x and then PUT /x and a side-effecting middleware declared on GET fired
	// on the PUT request as well — twice, if PUT declared it too. Nothing else in
	// the framework reads webCtx.GetMiddlewares(), so they no longer go in.
	for _, mw := range rc.GetMiddlewares() {
		cfg := grghttp.NewMiddlewareConfigBuilder().
			WithMiddleware(mw).
			WithPattern(pattern).
			Build()
		mws = append(mws, r.adaptRouteMiddleware(cfg))
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		msgRaw := req.Context().Value(core.FullMessageInstanceContextKey)

		var msg core.HttpMessage
		var ok bool

		if msg, ok = msgRaw.(core.HttpMessage); !ok {
			grghttp.Catch(fmt.Errorf("route: %s, Message not found in context", req.URL.Path), msg)
		}

		resolver, err := r.webCtx.GetNewInputResolver()(rc.GetHandler(), msg)
		if err != nil {
			grghttp.Catch(err, msg)
			return
		}
		args, err := resolver.(*grghttp.InputResolver).Resolve()
		if err != nil {
			grghttp.Catch(err, msg)
			return
		}
		reflect.ValueOf(rc.GetHandler()).Call(args)
	})

	r.engine.
		With(mws...).
		MethodFunc(method, pattern, h)

	r.registerPreflight(pattern, method)
	r.registerConcrete(method, pattern)

	r.namedRoutes[rc.GetName()] = rc
}

// registerPreflight installs a 204 OPTIONS responder for pattern.
//
// Every route used to be registered under OPTIONS with its own handler:
//
//	r.engine.With(mws...).Options(pattern, h)
//
// so any mutating handler was reachable via `OPTIONS /route` and it ran — a DELETE
// handler would delete. It also made the CSRF middleware's OPTIONS exemption a
// blanket exemption for every mutating endpoint in the app.
//
// OPTIONS now answers 204 with an Allow header and never invokes the route
// handler. The responder is installed once per pattern and reads the method list
// at request time, so it reports methods registered after it too.
//
// This is a behaviour change: an app that relied on OPTIONS reaching a handler
// must declare an explicit OPTIONS route for it.
func (r *ChiRouterAdapter) registerPreflight(pattern string, method string) {
	if r.allowedMethods == nil {
		r.allowedMethods = make(map[string][]string)
	}
	if r.preflightRegistered == nil {
		r.preflightRegistered = make(map[string]bool)
	}

	if !util.InArray(method, r.allowedMethods[pattern]) {
		r.allowedMethods[pattern] = append(r.allowedMethods[pattern], method)
	}

	// An app that declares its own OPTIONS route keeps it. Claiming the pattern
	// here stops the generic responder from being installed later and overwriting
	// the explicit handler when another method is registered for the same pattern.
	if strings.EqualFold(method, http.MethodOptions) {
		r.preflightRegistered[pattern] = true
		return
	}

	if r.preflightRegistered[pattern] {
		return
	}
	r.preflightRegistered[pattern] = true

	r.engine.Options(pattern, func(w http.ResponseWriter, req *http.Request) {
		allowed := append([]string{}, r.allowedMethods[pattern]...)
		if !util.InArray(http.MethodOptions, allowed) {
			allowed = append(allowed, http.MethodOptions)
		}
		sort.Strings(allowed)

		w.Header().Set("Allow", strings.Join(allowed, ", "))
		w.WriteHeader(http.StatusNoContent)
	})
}

func (r *ChiRouterAdapter) adaptFilter(cfg core.IMiddlewareConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			path := req.URL.Path

			isAnyExcludeSuitable := util.ContainsByClosure(cfg.GetExcludePatterns(), func(exclude string) bool {
				return matches(exclude, path)
			})
			if matches(cfg.GetPattern(), path) && !isAnyExcludeSuitable {
				msgRaw := req.Context().Value(core.FullMessageInstanceContextKey)

				var msg core.HttpMessage
				var ok bool

				if msg, ok = msgRaw.(core.HttpMessage); !ok {
					err.HandleError(fmt.Errorf("route: %s, Message not found in context", req.URL.Path))
					w.WriteHeader(500)
					return
				}

				cfg.GetMiddleware().Handle(func(_ core.HttpMessage) {
					next.ServeHTTP(w, req)
				})(msg)
			} else {
				next.ServeHTTP(w, req)
			}
		})
	}
}

func (r *ChiRouterAdapter) adaptRouteMiddleware(cfg core.IMiddlewareConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			msgRaw := req.Context().Value(core.FullMessageInstanceContextKey)

			var msg core.HttpMessage
			var ok bool

			if msg, ok = msgRaw.(core.HttpMessage); !ok {
				err.HandleError(fmt.Errorf("route: %s, Message not found in context", req.URL.Path))
				w.WriteHeader(500)
				return
			}

			cfg.GetMiddleware().Handle(func(_ core.HttpMessage) {
				next.ServeHTTP(w, req)
			})(msg)
		})
	}
}

func normalizeRoutePattern(routePattern string) string {
	return placeholderRE.ReplaceAllStringFunc(routePattern, func(ph string) string {
		content := ph[1 : len(ph)-1]
		parts := strings.SplitN(content, ":", 2)
		if len(parts) == 2 {
			regex := parts[1]
			if alnumRE.MatchString(regex) {
				return regex
			}
			return "*"
		}
		return "*"
	})
}

func matchesPattern(pattern, routePattern string) bool {
	if pattern == "" || pattern == "/**" {
		return true
	}

	normalized := normalizeRoutePattern(routePattern)

	if strings.HasSuffix(pattern, "**") {

		prefix := strings.TrimSuffix(pattern[:len(pattern)-2], "/")
		return normalized == prefix || strings.HasPrefix(normalized, prefix+"/")
	}

	return pattern == normalized
}

func matches(pattern, path string) bool {
	if pattern == "" {
		return false
	}

	if pattern == "/**" {
		return true
	}

	if len(pattern) > 2 && strings.HasSuffix(pattern, "**") {
		return strings.HasPrefix(path, pattern[:len(pattern)-2])
	}
	return pattern == path
}

func (thiz *ChiRouterAdapter) Engine() http.Handler {
	return thiz.engine
}

func (thiz *ChiRouterAdapter) UrlByName(name string, params map[string]any) string {
	route := thiz.RouteByName(name)
	if route == nil {
		return "/"
	}

	return thiz.replaceRouteSegments(route.Pattern(), params)
}

func (thiz *ChiRouterAdapter) UrlByNameSequence(name string, params ...any) string {
	route := thiz.RouteByName(name)
	if route == nil {
		return "/"
	}

	return thiz.replaceRouteSegmentsSequence(route.Pattern(), params...)
}

func (thiz *ChiRouterAdapter) RouteByName(name string) core.IRouteConfig {
	return thiz.namedRoutes[name]
}

func (thiz *ChiRouterAdapter) replaceRouteSegments(routePattern string, params map[string]any) string {
	r := regexp.MustCompile(`{([^}]+)(:[^}]+)?}`)

	result := r.ReplaceAllStringFunc(routePattern, func(match string) string {
		index := strings.IndexByte(match, ':')
		defaultValue := ""
		if index == -1 {
			index = len(match) - 1
		} else {
			defaultValue = match[index+1 : len(match)-1]
		}
		paramName := match[1:index]
		if value, ok := params[paramName]; ok {
			return fmt.Sprintf("%v", value)
		} else if defaultValue != "" {
			return defaultValue
		}
		panic(fmt.Errorf("Expected parameter '%s' for pattern '%s' was not found", paramName, routePattern))
	})

	return result
}

func (thiz *ChiRouterAdapter) replaceRouteSegmentsSequence(routePattern string, params ...any) string {
	r := regexp.MustCompile(`{([^}]+)(:[^}]+)?}`)

	paramIndex := -1
	result := r.ReplaceAllStringFunc(routePattern, func(match string) string {
		paramIndex++
		index := strings.IndexByte(match, ':')
		defaultValue := ""
		if index == -1 {
			index = len(match) - 1
		} else {
			defaultValue = match[index+1 : len(match)-1]
		}
		paramName := match[1:index]
		if len(params) < paramIndex {
			panic(fmt.Errorf("Expected parameter '%s' for pattern '%s' was not found", paramName, routePattern))
		}

		p := params[paramIndex]
		if p == nil && defaultValue != "" {
			return defaultValue
		} else if p == nil {
			panic(fmt.Errorf("Expected parameter '%s' for pattern '%s' was not found", paramName, routePattern))
		}
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
