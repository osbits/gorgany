package provider

import (
	"net/http"

	"github.com/osbits/gorgany/v2/app/core"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/http/middleware"
	grgprovider "github.com/osbits/gorgany/v2/provider"
	"github.com/osbits/gorgany/v2/service/dto"
	"github.com/spf13/viper"
)

// probePaths are hit every few seconds by orchestrators. They must not create
// sessions, so they are excluded from the session filter.
var probePaths = []string{"/healthz", "/readyz"}

func newRouteProvider() *grgprovider.RouteProvider {
	p := grgprovider.NewRouteProvider()
	p.SetNotFoundHandler(notFound)

	// Refuse state-changing requests from another origin, sibling subdomains included,
	// which the session cookie's SameSite=Lax does not stop. A client that sends no
	// origin headers at all (curl, a native app) is let through to the CSRF check.
	// Reading app.server.url here works only because the bootstrapper is deferred, and
	// an empty value panics rather than let the check fall back to trusting Host.
	serverURL := viper.GetString("app.server.url")
	if serverURL == "" {
		panic("app.server.url is empty: set APP_URL to the app's own origin, which the same-origin check compares against")
	}
	p.EnableSameOriginProtection(middleware.SameOriginOptions{ServerURL: serverURL})

	p.AddMiddleware(grghttp.NewMiddlewareConfigBuilder().
		WithPattern("/**").
		WithExcludePatterns(probePaths).
		AsFilter().
		WithMiddleware(middleware.NewSessionMiddleware()).
		Build())

	// The API is authenticated by the session cookie, so every mutating API request
	// carries the token from GET /csrf (docs/CSRF.md in the framework). An API called
	// with bearer tokens instead uses the "api" (JWT) strategy and drops this filter.
	p.AddMiddleware(grghttp.NewMiddlewareConfigBuilder().
		WithPattern("/api/**").
		AsFilter().
		WithMiddleware(middleware.NewCSRFMiddleware()).
		Build())

	for _, c := range controllers() {
		p.AddController(c)
	}
	return p
}

func notFound(message core.HttpMessage) {
	message.Response().JSON(dto.ReturnObject(nil, core.NotFoundHttpStatus, nil), http.StatusNotFound)
}
