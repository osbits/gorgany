package provider

import (
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/http/controller"
	"github.com/osbits/gorgany/v2/http/middleware"
	"github.com/osbits/gorgany/v2/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingMiddleware stands in for an app's own middleware.
type countingMiddleware struct{}

func (countingMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return next
}

// TestRecoveryIsRegisteredFirstByDefault is the T3.1 requirement the initial work
// implemented but never asserted: "Register it by default as the first /** filter in
// the standard setup — an app should not have to remember." Without this test the
// automatic registration could be dropped and every shipped app would silently go
// back to dropping connections on a panic.
func TestRecoveryIsRegisteredFirstByDefault(t *testing.T) {
	p := NewRouteProvider()
	p.AddMiddleware(newFilter(countingMiddleware{}))

	configs := p.standardMiddlewares()

	require.Len(t, configs, 2, "the app's middleware plus the recovery filter")

	first := configs[0]
	assert.IsType(t, &middleware.RecoveryMiddleware{}, first.GetMiddleware(),
		"recovery must be the FIRST filter so it wraps everything else")
	assert.Equal(t, "/**", first.GetPattern())
	assert.True(t, first.IsFilter(), "it must be a filter, not a route middleware")

	// The app's own middleware is still there, after it.
	assert.IsType(t, countingMiddleware{}, configs[1].GetMiddleware())
}

// TestRecoveryIsRegisteredEvenWithNoAppMiddleware covers the common case of an app
// that adds none of its own.
func TestRecoveryIsRegisteredEvenWithNoAppMiddleware(t *testing.T) {
	configs := NewRouteProvider().standardMiddlewares()

	require.Len(t, configs, 1)
	assert.IsType(t, &middleware.RecoveryMiddleware{}, configs[0].GetMiddleware())
}

// TestDisableRecoveryMiddlewareOptsOut pins the escape hatch for an app that
// installs its own recovery filter.
func TestDisableRecoveryMiddlewareOptsOut(t *testing.T) {
	p := NewRouteProvider()
	p.AddMiddleware(newFilter(countingMiddleware{}))
	p.DisableRecoveryMiddleware()

	configs := p.standardMiddlewares()

	require.Len(t, configs, 1, "only the app's own middleware remains")
	assert.IsType(t, countingMiddleware{}, configs[0].GetMiddleware())
}

// TestStandardMiddlewaresDoesNotMutateTheAppsSlice guards against the prepend
// aliasing the caller's backing array, which would make a second call return a
// different list.
func TestStandardMiddlewaresDoesNotMutateTheAppsSlice(t *testing.T) {
	p := NewRouteProvider()
	p.AddMiddleware(newFilter(countingMiddleware{}))

	first := p.standardMiddlewares()
	second := p.standardMiddlewares()

	require.Len(t, p.middlewares, 1, "the provider's own slice must be untouched")
	require.Len(t, first, 2)
	require.Len(t, second, 2)
	assert.IsType(t, &middleware.RecoveryMiddleware{}, second[0].GetMiddleware())
}

func newFilter(mw core.IMiddleware) core.IMiddlewareConfig {
	return http.NewMiddlewareConfigBuilder().
		WithPattern("/**").
		AsFilter().
		WithMiddleware(mw).
		Build()
}

// stubController stands in for an app's own controller.
type stubController struct {
	path string
}

func (c stubController) GetRoutes() []core.IRouteConfig {
	return []core.IRouteConfig{&router.RouteConfig{Path: c.path, Method: core.GET, Handler: func(core.HttpMessage) {}}}
}

// TestTheCsrfEndpointIsRegisteredByDefault is the A4 requirement: "Ship a token
// endpoint ... and register it in the standard setup." Before v2.0 a client had no way
// to obtain a token at all, so this must not be something an app has to remember.
func TestTheCsrfEndpointIsRegisteredByDefault(t *testing.T) {
	p := NewRouteProvider()
	p.AddController(stubController{path: "/widgets"})

	controllers := p.standardControllers()
	require.Len(t, controllers, 2)

	assert.IsType(t, stubController{}, controllers[0], "the app's own controllers come first")

	csrf, ok := controllers[1].(*controller.CsrfController)
	require.True(t, ok, "the framework appends its CSRF controller")

	routes := csrf.GetRoutes()
	require.Len(t, routes, 1)
	assert.Equal(t, core.DefaultCSRFTokenPath, routes[0].GetPath())
}

// TestTheCsrfEndpointIsRegisteredWithNoAppControllers: an app with no controllers of
// its own still gets the endpoint.
func TestTheCsrfEndpointIsRegisteredWithNoAppControllers(t *testing.T) {
	controllers := NewRouteProvider().standardControllers()
	require.Len(t, controllers, 1)
	assert.IsType(t, &controller.CsrfController{}, controllers[0])
}

// TestDisableCsrfControllerRemovesIt covers the opt-out, for an app mounting its own
// token endpoint.
func TestDisableCsrfControllerRemovesIt(t *testing.T) {
	p := NewRouteProvider()
	p.AddController(stubController{path: "/widgets"})
	p.DisableCsrfController()

	controllers := p.standardControllers()
	require.Len(t, controllers, 1)
	assert.IsType(t, stubController{}, controllers[0])
}

// TestStandardControllersDoesNotAccumulate: standardControllers is called once per
// Boot, but appending onto p.controllers in place would grow the list on every call
// and register the endpoint twice — which the duplicate-route check would then reject
// at boot. This is the same defect standardMiddlewares is guarded against above.
func TestStandardControllersDoesNotAccumulate(t *testing.T) {
	p := NewRouteProvider()
	p.AddController(stubController{path: "/widgets"})

	first := p.standardControllers()
	second := p.standardControllers()

	assert.Len(t, first, 2)
	assert.Len(t, second, 2)
	assert.Len(t, p.controllers, 1, "the app's own list is not mutated")
}
