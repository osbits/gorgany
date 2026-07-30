package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C2: the router answered an unknown route with Bytes(nil, 404) — an empty body with no
// content type — and left chi's bare 405 in place. An API client got nothing it could
// parse and never the standard envelope. T3.4 had already fixed negotiation for the auth
// middleware; this applies the same rule here, through the same helper.

// negotiationRouter builds a router with one GET route, so 404 and 405 are both
// reachable.
func negotiationRouter(t *testing.T) *ChiRouterAdapter {
	t.Helper()

	adapter := newTestRouter(t)
	adapter.RegisterRoute(&RouteConfig{
		Path:    "/widgets",
		Method:  core.GET,
		Name:    "widgets.list",
		Handler: func(message core.HttpMessage) { message.Response().Text("ok", http.StatusOK) },
	})
	return adapter
}

func serve(t *testing.T, adapter *ChiRouterAdapter, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	recorder := httptest.NewRecorder()
	adapter.ServeHTTP(recorder, req)
	return recorder
}

// envelopeOf decodes the standard response envelope.
func envelopeOf(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var out map[string]any
	require.NoErrorf(t, json.Unmarshal(body, &out), "body was not JSON: %s", string(body))
	return out
}

// TestAnApiClientGetsAJsonNotFound is the headline.
func TestAnApiClientGetsAJsonNotFound(t *testing.T) {
	adapter := negotiationRouter(t)

	for name, headers := range map[string]map[string]string{
		"accept json":       {"Accept": "application/json"},
		"content-type json": {"Content-Type": "application/json"},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := serve(t, adapter, http.MethodGet, "/no-such-route", headers)

			require.Equal(t, http.StatusNotFound, recorder.Code)
			require.NotEmpty(t, recorder.Body.Bytes(), "the body used to be empty")

			body := envelopeOf(t, recorder.Body.Bytes())
			assert.Equal(t, float64(404), body["status"])
			assert.Equal(t, "NOT_FOUND", body["status_code"])
			assert.NotEmpty(t, body["errors"])
		})
	}
}

// TestAnApiPathPrefixIsEnoughForJson covers the /api/ rule, for a client that sends no
// Accept header at all.
func TestAnApiPathPrefixIsEnoughForJson(t *testing.T) {
	recorder := serve(t, negotiationRouter(t), http.MethodGet, "/api/no-such-route", nil)

	require.Equal(t, http.StatusNotFound, recorder.Code)
	body := envelopeOf(t, recorder.Body.Bytes())
	assert.Equal(t, "NOT_FOUND", body["status_code"])
}

// TestABrowserGetsTextNotJson. A browser sends `Accept: text/html,...,*/*`; matching the
// wildcard would turn every 404 into a JSON body.
func TestABrowserGetsTextNotJson(t *testing.T) {
	recorder := serve(t, negotiationRouter(t), http.MethodGet, "/no-such-route", map[string]string{
		"Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
	})

	require.Equal(t, http.StatusNotFound, recorder.Code)
	assert.NotEmpty(t, recorder.Body.String(), "even the text response says something")

	var discard map[string]any
	assert.Error(t, json.Unmarshal(recorder.Body.Bytes(), &discard),
		"a browser must not be handed a JSON envelope")
}

// TestAMethodMismatchIsANegotiated405. chi's default was a bare 405 with no body, and
// nothing registered a handler for it.
func TestAMethodMismatchIsANegotiated405(t *testing.T) {
	recorder := serve(t, negotiationRouter(t), http.MethodDelete, "/widgets", map[string]string{
		"Accept": "application/json",
	})

	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	require.NotEmpty(t, recorder.Body.Bytes())

	body := envelopeOf(t, recorder.Body.Bytes())
	assert.Equal(t, float64(405), body["status"])
	assert.Equal(t, "METHOD_NOT_ALLOWED", body["status_code"])

	errors, ok := body["errors"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, errors)
	assert.Contains(t, errors[0], http.MethodDelete, "the response names the offending method")
}

// TestAMethodMismatchIsTextForABrowser, same rule as the 404.
func TestAMethodMismatchIsTextForABrowser(t *testing.T) {
	recorder := serve(t, negotiationRouter(t), http.MethodDelete, "/widgets", map[string]string{
		"Accept": "text/html",
	})

	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	assert.Contains(t, recorder.Body.String(), http.MethodDelete)
}

// TestAnAppsOwnNotFoundHandlerStillWins: negotiation is the default, not an override.
func TestAnAppsOwnNotFoundHandlerStillWins(t *testing.T) {
	adapter := negotiationRouter(t)

	called := false
	adapter.webCtx.SetNotFound(func(message core.HttpMessage) {
		called = true
		message.Response().Text("app 404", http.StatusNotFound)
	})

	recorder := serve(t, adapter, http.MethodGet, "/no-such-route", map[string]string{
		"Accept": "application/json",
	})

	assert.True(t, called, "the app's handler must still be preferred")
	assert.Equal(t, "app 404", recorder.Body.String())
}

// TestAMatchedRouteIsUnaffected guards against the new handlers shadowing real routes.
func TestAMatchedRouteIsUnaffected(t *testing.T) {
	recorder := serve(t, negotiationRouter(t), http.MethodGet, "/widgets", nil)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "ok", recorder.Body.String())
}

// TestTheRouterUsesTheSharedNegotiationHelper. Three places make this decision; the
// point of exporting it was that they cannot answer differently for the same request.
func TestTheRouterUsesTheSharedNegotiationHelper(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/widgets", nil)
	recorder := httptest.NewRecorder()

	adapter := negotiationRouter(t)
	msg, err := adapter.webCtx.GetNewMessage()(recorder, req)
	require.NoError(t, err)

	assert.True(t, grghttp.WantsJSON(msg))
}

// ------------------------- I1: a catch-all must not manufacture a 405

// I1. A catch-all claims every path beneath it for the one method it declares, so chi reports
// 405 for every *other* method on every path that has no real route. Mounting
// SpaController's `GET /*` therefore turned the whole app's `DELETE /api/nope` into
// "the resource exists, wrong verb" — measured, before the fix:
//
//	GET     /api/nope -> 200 (H1 fixed this one)
//	DELETE  /api/nope -> 405
//	POST    /api/nope -> 405
//
// H1 could not reach these: the SPA registers GET only, so a DELETE never arrives at the
// controller at all. It has to be settled in the router, which is the only place that knows
// whether a real route exists at the path.

// spaMountedRouter is an app with one real API route and a SPA catch-all, which is the shape
// that produced the bug.
func spaMountedRouter(t *testing.T) *ChiRouterAdapter {
	t.Helper()

	adapter := newTestRouter(t)
	adapter.RegisterRoute(&RouteConfig{
		Path:    "/widgets",
		Method:  core.GET,
		Name:    "widgets.list",
		Handler: func(message core.HttpMessage) { message.Response().Text("ok", http.StatusOK) },
	})
	adapter.RegisterRoute(&RouteConfig{
		Path:    "/*",
		Method:  core.GET,
		Name:    "gorgany.spa",
		Handler: func(message core.HttpMessage) { message.Response().Text("SPA", http.StatusOK) },
	})
	return adapter
}

func TestACatchAllDoesNotTurnAnUnroutedPathInto405(t *testing.T) {
	adapter := spaMountedRouter(t)

	for _, method := range []string{
		http.MethodDelete, http.MethodPost, http.MethodPut, http.MethodPatch,
	} {
		t.Run(method, func(t *testing.T) {
			recorder := serve(t, adapter, method, "/api/nope", map[string]string{
				"Accept": "application/json",
			})

			require.Equal(t, http.StatusNotFound, recorder.Code,
				"nothing is registered at this path; 405 would say the resource exists")

			body := envelopeOf(t, recorder.Body.Bytes())
			assert.Equal(t, "NOT_FOUND", body["status_code"])
		})
	}
}

// TestAGenuineMethodMismatchIsStill405 is the constraint. /widgets really does serve GET, so
// a DELETE to it is a method error and must not be flattened into a 404 — that would lose the
// distinction the fix exists to make.
func TestAGenuineMethodMismatchIsStill405(t *testing.T) {
	recorder := serve(t, spaMountedRouter(t), http.MethodDelete, "/widgets", map[string]string{
		"Accept": "application/json",
	})

	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	assert.Equal(t, "METHOD_NOT_ALLOWED", envelopeOf(t, recorder.Body.Bytes())["status_code"])
}

// TestAGenuine405NamesTheAllowedMethods. RFC 9110: "A 405 response MUST generate an Allow
// header field containing the supported methods." The previous implementation sent none at
// all, so a client could not discover the right verb from the response.
func TestAGenuine405NamesTheAllowedMethods(t *testing.T) {
	recorder := serve(t, spaMountedRouter(t), http.MethodDelete, "/widgets", nil)

	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)

	allow := recorder.Header().Get("Allow")
	assert.Contains(t, allow, http.MethodGet, "GET is what /widgets actually serves")
	assert.Contains(t, allow, http.MethodOptions)
	assert.NotContains(t, allow, http.MethodDelete, "the method that was refused")
}

// TestAllowNamesEveryRegisteredMethodNotJustTheFirstProbed. methodsForPath reads the pattern
// Match resolved and looks the methods up under it, rather than reporting whichever method
// happened to probe first.
func TestAllowNamesEveryRegisteredMethodNotJustTheFirstProbed(t *testing.T) {
	adapter := newTestRouter(t)
	for _, m := range []core.Method{core.GET, core.POST, core.PUT} {
		adapter.RegisterRoute(&RouteConfig{
			Path:    "/widgets",
			Method:  m,
			Name:    "widgets." + string(m),
			Handler: func(message core.HttpMessage) { message.Response().Text("ok", 200) },
		})
	}

	recorder := serve(t, adapter, http.MethodDelete, "/widgets", nil)
	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)

	allow := recorder.Header().Get("Allow")
	for _, expected := range []string{"GET", "POST", "PUT", "OPTIONS"} {
		assert.Contains(t, allow, expected)
	}
}

// TestTheSpaCatchAllStillServesItsOwnMethod — the fix must not disturb the SPA doing its job.
func TestTheSpaCatchAllStillServesItsOwnMethod(t *testing.T) {
	recorder := serve(t, spaMountedRouter(t), http.MethodGet, "/settings/profile", nil)

	require.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "SPA", recorder.Body.String())
}

// TestAParameterisedRouteIsNotACatchAll. `/users/{id}` matches exactly one segment, so it is a
// real route for a real resource and a wrong verb on it is a genuine 405. Treating every
// pattern with a placeholder as a catch-all would have turned those into 404s.
func TestAParameterisedRouteIsNotACatchAll(t *testing.T) {
	adapter := newTestRouter(t)
	adapter.RegisterRoute(&RouteConfig{
		Path:    "/users/{id}",
		Method:  core.GET,
		Name:    "users.show",
		Handler: func(message core.HttpMessage, id string) { message.Response().Text(id, 200) },
	})

	recorder := serve(t, adapter, http.MethodDelete, "/users/7", nil)
	assert.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Allow"), http.MethodGet)
}

// TestWithoutAnyCatchAllNothingChanges: an app with no SPA must behave exactly as it did.
func TestWithoutAnyCatchAllNothingChanges(t *testing.T) {
	adapter := negotiationRouter(t)

	notFound := serve(t, adapter, http.MethodDelete, "/api/nope", map[string]string{
		"Accept": "application/json",
	})
	assert.Equal(t, http.StatusNotFound, notFound.Code)

	mismatch := serve(t, adapter, http.MethodDelete, "/widgets", map[string]string{
		"Accept": "application/json",
	})
	assert.Equal(t, http.StatusMethodNotAllowed, mismatch.Code)
}

// TestTheCatchAllFallbackHonoursTheAppsNotFoundHandler. The 405→404 fallback goes through
// writeNotFound, so an app that registered SetNotFoundHandler gets its own handler for this
// 404 as well. Without that, one app would answer its own 404 for a GET and the framework's
// for a DELETE — the same class of inconsistency I1 exists to remove.
func TestTheCatchAllFallbackHonoursTheAppsNotFoundHandler(t *testing.T) {
	adapter := spaMountedRouter(t)

	called := false
	adapter.webCtx.SetNotFound(func(message core.HttpMessage) {
		called = true
		message.Response().Text("app-404", http.StatusNotFound)
	})

	recorder := serve(t, adapter, http.MethodDelete, "/api/nope", map[string]string{
		"Accept": "application/json",
	})

	assert.True(t, called, "the app's own 404 handler must run")
	assert.Equal(t, http.StatusNotFound, recorder.Code)
	assert.Equal(t, "app-404", recorder.Body.String())
}
