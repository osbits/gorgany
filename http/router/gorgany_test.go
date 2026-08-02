package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/osbits/gorgany/v2/app/core"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRouter wires a ChiRouterAdapter over a real WebContext and message
// factory, so requests travel the same path they do in a running server.
func newTestRouter(t *testing.T) *ChiRouterAdapter {
	t.Helper()

	wc := &grghttp.WebContext{}
	wc.SetNewMessage(func(w http.ResponseWriter, r *http.Request) (core.HttpMessage, error) {
		return &routerTestMessage{w: w, r: r}, nil
	})
	wc.SetNewInputResolver(func(h core.HandlerFunc, m core.HttpMessage) (any, error) {
		return &grghttp.InputResolver{ReflectedHandler: reflect.ValueOf(h), Message: m}, nil
	})

	r := &ChiRouterAdapter{webCtx: wc}
	r.Init()
	return r
}

// routerTestMessage is the minimum HttpMessage the router and InputResolver need.
type routerTestMessage struct {
	core.HttpMessage
	w http.ResponseWriter
	r *http.Request
}

func (m *routerTestMessage) Close() error { return nil }

func (m *routerTestMessage) Request() core.IRequestScope {
	return &routerTestRequest{raw: m.r}
}

func (m *routerTestMessage) Response() core.IResponseScope {
	return &routerTestResponse{w: m.w}
}

type routerTestResponse struct {
	core.IResponseScope
	w http.ResponseWriter
}

func (r *routerTestResponse) Header() http.Header            { return r.w.Header() }
func (r *routerTestResponse) SetHeader(key, value string)    { r.w.Header().Set(key, value) }
func (r *routerTestResponse) RawWriter() http.ResponseWriter { return r.w }

// Text, JSON and Bytes write through to the recorder, because the router's negotiated
// 404 and 405 (C2) produce real bodies now and a test has to be able to read them.
func (r *routerTestResponse) Text(body string, code int) {
	r.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.w.WriteHeader(code)
	_, _ = r.w.Write([]byte(body))
}

func (r *routerTestResponse) JSON(v any, code int) {
	r.w.Header().Set("Content-Type", "application/json")
	r.w.WriteHeader(code)
	encoded, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = r.w.Write(encoded)
}

func (r *routerTestResponse) Bytes(b []byte, code int) {
	r.w.WriteHeader(code)
	_, _ = r.w.Write(b)
}

type routerTestRequest struct {
	core.IRequestScope
	raw *http.Request
}

func (r *routerTestRequest) RawRequest() *http.Request { return r.raw }
func (r *routerTestRequest) Header() http.Header       { return r.raw.Header }

// PathParam reads chi's route context, which is how the real request scope resolves the
// `namespace` parameter that grghttp.WantsJSON consults.
func (r *routerTestRequest) PathParam(name string) string {
	return chi.URLParamFromCtx(r.raw.Context(), name)
}

// handlerRoute is a route whose handler records that it ran.
type handlerRoute struct {
	pattern     string
	method      core.Method
	name        string
	handler     core.HandlerFunc
	middlewares []core.IMiddleware
}

func (rc handlerRoute) Pattern() string                    { return rc.pattern }
func (rc handlerRoute) GetPath() string                    { return rc.pattern }
func (rc handlerRoute) GetMethod() core.Method             { return rc.method }
func (rc handlerRoute) GetHandler() core.HandlerFunc       { return rc.handler }
func (rc handlerRoute) GetName() string                    { return rc.name }
func (rc handlerRoute) GetNamespace() string               { return "" }
func (rc handlerRoute) GetMiddlewares() []core.IMiddleware { return rc.middlewares }

// countingMiddleware records how many times it ran.
type countingMiddleware struct {
	calls *int
}

func (m countingMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		*m.calls++
		next(message)
	}
}

// -------------------------------------------------------- OPTIONS side effects

// TestOptionsDoesNotInvokeTheRouteHandler is the T3.2 router-side regression. Every
// route used to be registered under OPTIONS with its own handler:
//
//	r.engine.With(mws...).Options(pattern, h)
//
// so `OPTIONS /widgets/1` ran the DELETE handler and the row was deleted. Combined
// with the CSRF middleware's OPTIONS exemption, that was a token-free path to every
// mutating endpoint in the app.
func TestOptionsDoesNotInvokeTheRouteHandler(t *testing.T) {
	r := newTestRouter(t)

	sideEffect := false
	r.RegisterRoute(handlerRoute{
		pattern: "/widgets/{id}",
		method:  core.Method(http.MethodDelete),
		name:    "widgets.delete",
		handler: func(core.HttpMessage) { sideEffect = true },
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/widgets/1", nil))

	assert.False(t, sideEffect, "OPTIONS must produce no side effect")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// TestOptionsReportsAnAllowHeaderCoveringEveryMethod
func TestOptionsReportsAnAllowHeaderCoveringEveryMethod(t *testing.T) {
	r := newTestRouter(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		r.RegisterRoute(handlerRoute{
			pattern: "/widgets/{id}",
			method:  core.Method(method),
			name:    "widgets." + method,
			handler: func(core.HttpMessage) {},
		})
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/widgets/1", nil))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "DELETE, GET, OPTIONS, PUT", rec.Header().Get("Allow"))
}

// TestDeclaredOptionsRouteIsKept: an app that declares its own OPTIONS handler must
// keep it rather than have the generic responder overwrite it.
func TestDeclaredOptionsRouteIsKept(t *testing.T) {
	r := newTestRouter(t)

	reached := false
	r.RegisterRoute(handlerRoute{
		pattern: "/widgets",
		method:  core.Method(http.MethodOptions),
		name:    "widgets.options",
		handler: func(message core.HttpMessage) { reached = true },
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/widgets", nil))

	assert.True(t, reached, "an explicitly declared OPTIONS route must be honoured")
}

// TestDeclaredOptionsRouteSurvivesAnotherMethodRegisteredLater
func TestDeclaredOptionsRouteSurvivesAnotherMethodRegisteredLater(t *testing.T) {
	r := newTestRouter(t)

	reached := false
	r.RegisterRoute(handlerRoute{
		pattern: "/widgets",
		method:  core.Method(http.MethodOptions),
		name:    "widgets.options",
		handler: func(core.HttpMessage) { reached = true },
	})
	r.RegisterRoute(handlerRoute{
		pattern: "/widgets",
		method:  core.Method(http.MethodPost),
		name:    "widgets.create",
		handler: func(core.HttpMessage) {},
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/widgets", nil))

	assert.True(t, reached)
}

// TestTheDeclaredMethodStillReachesItsHandler guards against the preflight change
// breaking normal routing.
func TestTheDeclaredMethodStillReachesItsHandler(t *testing.T) {
	r := newTestRouter(t)

	reached := false
	r.RegisterRoute(handlerRoute{
		pattern: "/widgets/{id}",
		method:  core.Method(http.MethodDelete),
		name:    "widgets.delete",
		handler: func(core.HttpMessage) { reached = true },
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/widgets/1", nil))

	assert.True(t, reached)
}

// ------------------------------------------------- duplicate middleware (T4.3)

// TestRouteMiddlewareFiresOncePerRequest is the T4.3 regression. Route-scoped
// middleware configs were published into the shared webCtx list keyed by the route's
// pattern, so the next RegisterRoute for the same method-agnostic pattern re-matched
// and re-attached them. Register GET /x then PUT /x and a side-effecting middleware
// fired twice on one request.
func TestRouteMiddlewareFiresOncePerRequest(t *testing.T) {
	r := newTestRouter(t)

	calls := 0
	mw := countingMiddleware{calls: &calls}

	r.RegisterRoute(handlerRoute{
		pattern:     "/x",
		method:      core.Method(http.MethodGet),
		name:        "x.get",
		handler:     func(core.HttpMessage) {},
		middlewares: []core.IMiddleware{mw},
	})
	r.RegisterRoute(handlerRoute{
		pattern:     "/x",
		method:      core.Method(http.MethodPut),
		name:        "x.put",
		handler:     func(core.HttpMessage) {},
		middlewares: []core.IMiddleware{mw},
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/x", nil))

	assert.Equal(t, 1, calls, "a route middleware must fire exactly once per request")
}

// TestRouteMiddlewareDoesNotLeakOntoAnotherMethod is the same defect stated as the
// worse symptom: middleware declared only on GET used to run on PUT too.
func TestRouteMiddlewareDoesNotLeakOntoAnotherMethod(t *testing.T) {
	r := newTestRouter(t)

	getCalls := 0

	r.RegisterRoute(handlerRoute{
		pattern:     "/x",
		method:      core.Method(http.MethodGet),
		name:        "x.get",
		handler:     func(core.HttpMessage) {},
		middlewares: []core.IMiddleware{countingMiddleware{calls: &getCalls}},
	})
	r.RegisterRoute(handlerRoute{
		pattern: "/x",
		method:  core.Method(http.MethodPut),
		name:    "x.put",
		handler: func(core.HttpMessage) {},
		// No middleware declared on PUT at all.
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/x", nil))

	assert.Equal(t, 0, getCalls,
		"middleware declared on GET must not run on PUT")

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Equal(t, 1, getCalls, "it must still run on its own route")
}

// TestSharedMiddlewareStillMatchesByPattern keeps the intended feature working: a
// middleware registered globally with a matching pattern attaches to the route.
func TestSharedMiddlewareStillMatchesByPattern(t *testing.T) {
	r := newTestRouter(t)

	calls := 0
	r.RegisterMiddleware(grghttp.NewMiddlewareConfigBuilder().
		WithPattern("/**").
		WithMiddleware(countingMiddleware{calls: &calls}).
		Build())

	r.RegisterRoute(handlerRoute{
		pattern: "/x",
		method:  core.Method(http.MethodGet),
		name:    "x.get",
		handler: func(core.HttpMessage) {},
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))

	assert.Equal(t, 1, calls)
}

// TestSharedMiddlewareFiresOnceAcrossTwoMethodsOfOneRoute
func TestSharedMiddlewareFiresOnceAcrossTwoMethodsOfOneRoute(t *testing.T) {
	r := newTestRouter(t)

	calls := 0
	r.RegisterMiddleware(grghttp.NewMiddlewareConfigBuilder().
		WithPattern("/**").
		WithMiddleware(countingMiddleware{calls: &calls}).
		Build())

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		r.RegisterRoute(handlerRoute{
			pattern: "/x",
			method:  core.Method(method),
			name:    "x." + method,
			handler: func(core.HttpMessage) {},
		})
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/x", nil))

	assert.Equal(t, 1, calls)
}

// TestExcludedPatternStillExcludes
func TestExcludedPatternStillExcludes(t *testing.T) {
	r := newTestRouter(t)

	calls := 0
	r.RegisterMiddleware(grghttp.NewMiddlewareConfigBuilder().
		WithPattern("/**").
		WithExcludePatterns([]string{"/health"}).
		WithMiddleware(countingMiddleware{calls: &calls}).
		Build())

	r.RegisterRoute(handlerRoute{
		pattern: "/health",
		method:  core.Method(http.MethodGet),
		name:    "health",
		handler: func(core.HttpMessage) {},
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	assert.Equal(t, 0, calls)
}

// TestNamedRoutesAreStillRegistered
func TestNamedRoutesAreStillRegistered(t *testing.T) {
	r := newTestRouter(t)

	r.RegisterRoute(handlerRoute{
		pattern: "/widgets/{id}",
		method:  core.Method(http.MethodGet),
		name:    "widgets.show",
		handler: func(core.HttpMessage) {},
	})

	require.NotNil(t, r.RouteByName("widgets.show"))
	assert.Equal(t, "/widgets/1", r.UrlByName("widgets.show", map[string]any{"id": 1}))
}

// corsHeaderMiddleware stands in for CorsMiddleware: it sets a header and calls
// through, which is what a preflight handler has to cooperate with.
type corsHeaderMiddleware struct{ ran *bool }

func (m corsHeaderMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		*m.ran = true
		message.Response().SetHeader("Access-Control-Allow-Origin", "*")
		next(message)
	}
}

// TestGlobalFilterStillRunsOnOptions pins the claim MIGRATION_v2.md and
// docs/DIALECTS.md both make to app authors: now that OPTIONS is answered by a
// generic 204 responder rather than the route handler, CORS headers must be set by
// a /** *filter*, which still runs. If chi ordered the preflight responder ahead of
// the filter chain, that advice would be wrong and every migrated app's preflight
// would lose its CORS headers.
func TestGlobalFilterStillRunsOnOptions(t *testing.T) {
	r := newTestRouter(t)

	ran := false
	r.RegisterMiddleware(grghttp.NewMiddlewareConfigBuilder().
		WithPattern("/**").
		AsFilter().
		WithMiddleware(corsHeaderMiddleware{ran: &ran}).
		Build())

	r.RegisterRoute(handlerRoute{
		pattern: "/widgets/{id}",
		method:  core.Method(http.MethodDelete),
		name:    "widgets.delete",
		handler: func(core.HttpMessage) { t.Fatal("the route handler must not run on OPTIONS") },
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/widgets/1", nil))

	assert.True(t, ran, "a /** filter must still run on an OPTIONS request")
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"),
		"headers set by the filter must survive onto the 204")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Allow"))
}

// brokenDto forgets to declare a ContentType, so no body parser can be built for it.
type brokenDto struct{ Name string }

func (brokenDto) ContentType() core.ContentType { return "" }

// TestAMisdeclaredDtoFailsAtRouteRegistration is the A3 requirement at the boundary
// the brief specified: "A misdeclared DTO should stop the server starting, not serve
// 500s."
//
// Previously such a route registered happily and then panicked per request — on a nil
// parser dereference inside the resolver, and again via reflect.Call with too few
// arguments once `continue` had skipped the argument append. v2's RecoveryMiddleware
// turned both into a 500, which is better than a dropped connection and also why they
// were easy to miss.
func TestAMisdeclaredDtoFailsAtRouteRegistration(t *testing.T) {
	recovered := func() (r any) {
		defer func() { r = recover() }()
		newTestRouter(t).RegisterRoute(handlerRoute{
			pattern: "/widgets",
			method:  core.Method(http.MethodPost),
			name:    "widgets.create",
			handler: func(msg core.HttpMessage, dto brokenDto) {},
		})
		return nil
	}()

	require.NotNil(t, recovered,
		"a route whose DTO has no parser must fail at registration")

	err, ok := recovered.(error)
	require.Truef(t, ok, "expected an error, got %T", recovered)

	// The message must locate the route AND name the DTO and the fix.
	assert.Contains(t, err.Error(), "route POST /widgets")
	assert.Contains(t, err.Error(), "widgets.create")
	assert.Contains(t, err.Error(), "brokenDto")
	assert.Contains(t, err.Error(), "empty ContentType()")
	assert.Contains(t, err.Error(), string(core.ApplicationJson))
	t.Logf("registration refused with: %v", err)
}

// TestAWellFormedRouteStillRegisters guards against the new check rejecting valid
// handlers.
func TestAWellFormedRouteStillRegisters(t *testing.T) {
	r := newTestRouter(t)

	require.NotPanics(t, func() {
		r.RegisterRoute(handlerRoute{
			pattern: "/widgets/{id}",
			method:  core.Method(http.MethodGet),
			name:    "widgets.show",
			handler: func(msg core.HttpMessage, id string) {},
		})
	})
	require.NotNil(t, r.RouteByName("widgets.show"))
}
