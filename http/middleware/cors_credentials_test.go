package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C5: the middleware emitted `Access-Control-Allow-Origin: *` from its origins branch
// and `Access-Control-Allow-Credentials: true` from AllowCredentials, independently. The
// Fetch spec forbids that pair, so every credentialed cross-origin request failed in the
// browser with no signal on the server side and no diagnostic for the developer.

// TestWildcardWithCredentialsIsRefused covers every way to ask for "all origins".
func TestWildcardWithCredentialsIsRefused(t *testing.T) {
	cases := map[string]Options{
		"explicit star": {
			AllowedOrigins:   []string{"*"},
			AllowCredentials: true,
		},
		"star among others": {
			AllowedOrigins:   []string{"https://app.example.com", "*"},
			AllowCredentials: true,
		},
		"star with surrounding space": {
			AllowedOrigins:   []string{" * "},
			AllowCredentials: true,
		},
		// The one that surprises: an empty list with no AllowOriginFunc defaults to
		// allowing every origin, so this is the same misconfiguration written implicitly.
		"origins omitted entirely": {
			AllowCredentials: true,
		},
	}

	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewCorsMiddlewareChecked(options)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrWildcardOriginWithCredentials))
			assert.Contains(t, err.Error(), "AllowCredentials")

			assert.PanicsWithError(t, ErrWildcardOriginWithCredentials.Error(), func() {
				NewCorsMiddleware(options)
			}, "the panicking constructor must refuse it at boot")
		})
	}
}

// TestLegitimateConfigurationsAreAccepted. Refusing too much would be its own bug: a
// wildcard *within* an origin is fine with credentials, and so is a wildcard origin
// without them.
func TestLegitimateConfigurationsAreAccepted(t *testing.T) {
	cases := map[string]Options{
		"explicit origins with credentials": {
			AllowedOrigins:   []string{"https://app.example.com"},
			AllowCredentials: true,
		},
		"subdomain wildcard with credentials": {
			AllowedOrigins:   []string{"https://*.example.com"},
			AllowCredentials: true,
		},
		"wildcard origin without credentials": {
			AllowedOrigins: []string{"*"},
		},
		"origins omitted without credentials": {},
		"origin func with credentials": {
			AllowCredentials: true,
			AllowOriginFunc: func(*http.Request, string) bool {
				return true
			},
		},
	}

	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := NewCorsMiddlewareChecked(options)
			require.NoError(t, err)
			require.NotNil(t, c)
			assert.NotPanics(t, func() { NewCorsMiddleware(options) })
		})
	}
}

// TestAllowAllStaysConstructible: it is the framework's own permissive preset, and it
// must not trip the new refusal.
func TestAllowAllStaysConstructible(t *testing.T) {
	assert.NotPanics(t, func() { AllowAll() })
}

// TestCredentialedResponsesReflectTheSpecificOrigin proves the surviving configurations
// emit a pair a browser accepts, rather than only that construction succeeded.
func TestCredentialedResponsesReflectTheSpecificOrigin(t *testing.T) {
	c := NewCorsMiddleware(Options{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowedMethods:   []string{http.MethodGet, http.MethodPost},
		AllowCredentials: true,
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://api.example.com/widgets", nil)
	req.Header.Set("Origin", "https://app.example.com")

	require.True(t, c.handleActualRequest(recorder, req))

	headers := recorder.Header()
	assert.Equal(t, "https://app.example.com", headers.Get("Access-Control-Allow-Origin"),
		"a credentialed response names the origin; * would be rejected by the browser")
	assert.Equal(t, "true", headers.Get("Access-Control-Allow-Credentials"))
	assert.Contains(t, headers.Values("Vary"), "Origin",
		"reflecting the origin makes the response origin-dependent, so Vary is required")
}

// TestAWildcardOriginWithoutCredentialsStillEmitsAStar: the refusal must not have
// changed what the permissive-but-valid configuration does.
func TestAWildcardOriginWithoutCredentialsStillEmitsAStar(t *testing.T) {
	c := NewCorsMiddleware(Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{http.MethodGet},
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://api.example.com/widgets", nil)
	req.Header.Set("Origin", "https://anywhere.example.com")

	require.True(t, c.handleActualRequest(recorder, req))

	assert.Equal(t, "*", recorder.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, recorder.Header().Get("Access-Control-Allow-Credentials"))
}

// TestNoResponseEverPairsStarWithCredentials is the invariant, asserted over the
// preflight path as well as the simple-request path.
func TestNoResponseEverPairsStarWithCredentials(t *testing.T) {
	configs := []Options{
		{AllowedOrigins: []string{"*"}, AllowedMethods: []string{http.MethodGet, http.MethodPost}},
		{AllowedOrigins: []string{"https://app.example.com"}, AllowedMethods: []string{http.MethodGet, http.MethodPost}, AllowCredentials: true},
		{AllowedOrigins: []string{"https://*.example.com"}, AllowedMethods: []string{http.MethodGet, http.MethodPost}, AllowCredentials: true},
	}

	for _, options := range configs {
		c := NewCorsMiddleware(options)

		for _, handle := range []func(http.ResponseWriter, *http.Request) bool{
			c.handleActualRequest,
			c.handlePreflight,
		} {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodOptions, "https://api.example.com/widgets", nil)
			req.Header.Set("Origin", "https://app.example.com")
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)

			handle(recorder, req)

			headers := recorder.Header()
			if headers.Get("Access-Control-Allow-Origin") == "*" {
				assert.Empty(t, headers.Get("Access-Control-Allow-Credentials"),
					"a wildcard origin must never be paired with credentials")
			}
		}
	}
}
