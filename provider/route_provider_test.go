package provider

import (
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/http"
	"github.com/osbits/gorgany/http/middleware"
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
