package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/osbits/gorgany/app/core"
	grghttp "github.com/osbits/gorgany/http"
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
