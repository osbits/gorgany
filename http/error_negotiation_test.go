package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C2: every framework error handler wrote text/plain, several of them with an *empty*
// body, so an API client got nothing it could parse and never the standard envelope.
// T3.4 had fixed negotiation for the auth middleware only.

type negotiationRecorder struct {
	status      int
	body        any
	text        string
	written     bool
	headers     http.Header
	redirectURL string
	flash       map[string]any
}

type negotiationResponse struct {
	core.IResponseScope
	rec *negotiationRecorder
}

func (r *negotiationResponse) JSON(v any, code int) {
	r.rec.written, r.rec.status, r.rec.body = true, code, v
}

func (r *negotiationResponse) Text(body string, code int) {
	r.rec.written, r.rec.status, r.rec.text = true, code, body
}

func (r *negotiationResponse) Header() http.Header {
	if r.rec.headers == nil {
		r.rec.headers = http.Header{}
	}
	return r.rec.headers
}

type negotiationRequest struct {
	core.IRequestScope
	raw *http.Request
}

func (r *negotiationRequest) RawRequest() *http.Request { return r.raw }
func (r *negotiationRequest) Header() http.Header       { return r.raw.Header }
func (r *negotiationRequest) PathParam(string) string   { return "" }

type negotiationMessage struct {
	core.HttpMessage
	req *negotiationRequest
	res *negotiationResponse
	rec *negotiationRecorder
}

func (m *negotiationMessage) Request() core.IRequestScope   { return m.req }
func (m *negotiationMessage) Response() core.IResponseScope { return m.res }
func (m *negotiationMessage) Context() context.Context      { return context.Background() }

// RedirectWithFlash records the browser branch of processValidationErrors. The real one
// also writes the flash into the session; the status and the target are what this file
// asserts on.
func (m *negotiationMessage) RedirectWithFlash(url string, code int, data map[string]any) {
	m.rec.written, m.rec.status, m.rec.redirectURL, m.rec.flash = true, code, url, data
}

func negotiationMessageFor(target string, headers map[string]string) *negotiationMessage {
	raw := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		raw.Header.Set(k, v)
	}

	rec := &negotiationRecorder{}
	return &negotiationMessage{
		req: &negotiationRequest{raw: raw},
		res: &negotiationResponse{rec: rec},
		rec: rec,
	}
}

// jsonHeaders and browserHeaders are the two shapes every handler is checked against.
var (
	jsonHeaders    = map[string]string{"Accept": "application/json"}
	browserHeaders = map[string]string{"Accept": "text/html,application/xhtml+xml,*/*;q=0.8"}
)

func envelope(t *testing.T, body any) map[string]any {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	return out
}

// TestEveryHandlerNegotiates walks all four handlers that used to be text-only.
func TestEveryHandlerNegotiates(t *testing.T) {
	tests := []struct {
		name    string
		handler core.ErrorHandler
		err     error
		status  int
		code    string
	}{
		{
			name:    "body parse error",
			handler: processBodyParsingError,
			err:     error2.NewInputBodyParseError(`{"a":`, "application/json", errors.New("unexpected end of JSON input")),
			status:  http.StatusBadRequest,
			code:    "BAD_REQUEST",
		},
		{
			name:    "param parse error",
			handler: processInputParsingError,
			err:     error2.NewInputParamParseError("abc", "int"),
			status:  http.StatusNotFound,
			code:    "NOT_FOUND",
		},
		{
			name:    "jwt auth error",
			handler: processJwtAuthError,
			err:     error2.NewJwtAuthError(),
			status:  http.StatusUnauthorized,
			code:    "NOT_AUTHORIZED",
		},
		{
			name:    "default error",
			handler: processDefaultError,
			err:     errors.New("something went wrong"),
			status:  http.StatusInternalServerError,
			code:    "INTERNAL_ERROR",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name+" for an api client", func(t *testing.T) {
			message := negotiationMessageFor("/widgets", jsonHeaders)
			tc.handler(tc.err, message)

			require.True(t, message.rec.written)
			assert.Equal(t, tc.status, message.rec.status)
			require.NotNil(t, message.rec.body, "the JSON branch must have been taken")

			body := envelope(t, message.rec.body)
			assert.Equal(t, float64(tc.status), body["status"])
			assert.Equal(t, tc.code, body["status_code"])
			assert.NotEmpty(t, body["errors"], "an empty errors list tells a client nothing")
		})

		t.Run(tc.name+" for a browser", func(t *testing.T) {
			message := negotiationMessageFor("/widgets", browserHeaders)
			tc.handler(tc.err, message)

			require.True(t, message.rec.written)
			assert.Equal(t, tc.status, message.rec.status)
			assert.Nil(t, message.rec.body, "a browser must not get a JSON envelope")
			assert.NotEmpty(t, message.rec.text, "the text body used to be empty")
		})
	}
}

// TestTheBodyParseResponseNeverEchoesTheBody. InputBodyParseError.Error() includes the
// raw body for the log's benefit, and a body that failed to parse is exactly the kind
// that might carry a secret halfway through.
func TestTheBodyParseResponseNeverEchoesTheBody(t *testing.T) {
	const secretBody = `{"password": "hunter2", "token": "abc`
	err := error2.NewInputBodyParseError(secretBody, "application/json", errors.New("unexpected end of JSON input"))

	for _, headers := range []map[string]string{jsonHeaders, browserHeaders} {
		message := negotiationMessageFor("/widgets", headers)
		processBodyParsingError(err, message)

		rendered := message.rec.text
		if message.rec.body != nil {
			raw, marshalErr := json.Marshal(message.rec.body)
			require.NoError(t, marshalErr)
			rendered = string(raw)
		}

		assert.NotContains(t, rendered, "hunter2")
		assert.NotContains(t, rendered, secretBody)
		assert.Contains(t, rendered, "unexpected end of JSON input", "the reason still gets through")
	}
}

// TestABodyParseErrorWithoutAReasonStillSaysSomething: the handler renders RawError, so a
// nil one must not produce the empty 400 this fix was about.
func TestABodyParseErrorWithoutAReasonStillSaysSomething(t *testing.T) {
	message := negotiationMessageFor("/widgets", jsonHeaders)
	processBodyParsingError(error2.NewInputBodyParseError("x", "application/json", nil), message)

	body := envelope(t, message.rec.body)
	errorsList, ok := body["errors"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, errorsList)
	assert.Contains(t, errorsList[0], "could not be parsed")
}

// TestAnUnrelatedErrorTypeDoesNotBreakTheHandler: Catch dispatches by type name, and a
// custom handler can be registered under a name whose error is something else entirely.
func TestAnUnrelatedErrorTypeDoesNotBreakTheHandler(t *testing.T) {
	message := negotiationMessageFor("/widgets", jsonHeaders)

	require.NotPanics(t, func() {
		processBodyParsingError(errors.New("not an InputBodyParseError"), message)
	})
	assert.Equal(t, http.StatusBadRequest, message.rec.status)
}

// TestWantsJSONOnTheApiPrefix pins the shared helper's third rule, for a client sending
// no Accept header.
func TestWantsJSONOnTheApiPrefix(t *testing.T) {
	assert.True(t, WantsJSON(negotiationMessageFor("/api/widgets", nil)))
	assert.True(t, WantsJSON(negotiationMessageFor("/api", nil)))
	assert.False(t, WantsJSON(negotiationMessageFor("/widgets", nil)))
	assert.False(t, WantsJSON(negotiationMessageFor("/apiary/widgets", nil)),
		"the prefix must not match a path that merely starts with the letters")
}

// TestWantsJSONWithNoMessage: the handlers are reachable from paths where a message
// could not be built.
func TestWantsJSONWithNoMessage(t *testing.T) {
	assert.False(t, WantsJSON(nil))
}

// --------------------------------------------------- the fifth handler (F3)

// processValidationErrors was the one default handler C2 did not negotiate, and the one
// this file did not cover — which is not a coincidence: the four it covers are the four
// C2 changed. It answered every caller with a 301 redirect to the Referer, so B2's
// reshaped payload never reached an API client through the framework default.

// validationFixture is a realistic pair of failures in the v2.0 shape.
func validationFixture() *error2.ValidationErrors {
	return &error2.ValidationErrors{
		{Field: "email", Err: "email is required", Rule: "required", Path: "email"},
		{Field: "postal_code", Err: "postal_code must be exactly 4", Rule: "len",
			Param: "4", Path: "address.postal_code"},
	}
}

// messageWithReferer builds a message carrying a Referer, since the browser branch keys
// off it.
func messageWithReferer(target, referer string) *negotiationMessage {
	message := negotiationMessageFor(target, nil)
	if referer != "" {
		message.req.raw.Header.Set("Referer", referer)
	}
	return message
}

// TestAnApiClientGetsA422Envelope is the F3 headline.
func TestAnApiClientGetsA422Envelope(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"accept json":       jsonHeaders,
		"content-type json": {"Content-Type": "application/json"},
	} {
		t.Run(name, func(t *testing.T) {
			message := negotiationMessageFor("/widgets", headers)
			message.req.raw.Header.Set("Referer", "http://example.invalid/form")

			processValidationErrors(validationFixture(), message)

			require.Equal(t, http.StatusUnprocessableEntity, message.rec.status,
				"an API client must get 422, not a redirect")
			require.NotNil(t, message.rec.body, "the JSON branch must have been taken")

			body := envelope(t, message.rec.body)
			assert.Equal(t, float64(422), body["status"])
			assert.Equal(t, "VALIDATION", body["status_code"])

			// The reshaped payload from B2 has to survive into the envelope: this is
			// what the whole item was for.
			errors, ok := body["errors"].([]any)
			require.True(t, ok)
			require.Len(t, errors, 2)

			first, ok := errors[0].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "email", first["field"], "the wire name, not the Go name")
			assert.Equal(t, "required", first["rule"])
		})
	}
}

// TestTheApiPathPrefixIsEnoughForA422, for a client that sends no Accept header.
func TestTheApiPathPrefixIsEnoughForA422(t *testing.T) {
	message := negotiationMessageFor("/api/v1/widgets", nil)
	message.req.raw.Header.Set("Referer", "http://example.invalid/form")

	processValidationErrors(validationFixture(), message)

	assert.Equal(t, http.StatusUnprocessableEntity, message.rec.status)
}

// TestABrowserFormGetsA303Redirect. The status matters: a 301 is permanently cacheable and
// browsers rewrite it to a GET, so a browser could cache "POST this URL -> GET that one"
// for good. 303 See Other is the post-redirect-get status.
func TestABrowserFormGetsA303Redirect(t *testing.T) {
	message := messageWithReferer("/widgets", "http://example.invalid/form")

	processValidationErrors(validationFixture(), message)

	assert.Equal(t, http.StatusSeeOther, message.rec.status)
	assert.Equal(t, "http://example.invalid/form", message.rec.redirectURL)
	assert.NotEqual(t, http.StatusMovedPermanently, message.rec.status,
		"301 is permanently cacheable and must not be used for a validation bounce")
}

// TestNoRefererFallsBackTo422 rather than redirecting nowhere. Passing an empty Referer
// through produced Location: "".
func TestNoRefererFallsBackTo422(t *testing.T) {
	message := messageWithReferer("/widgets", "")

	processValidationErrors(validationFixture(), message)

	assert.Equal(t, http.StatusUnprocessableEntity, message.rec.status,
		"with nowhere to go back to, showing the errors beats an empty Location")
	assert.Empty(t, message.rec.redirectURL)
	require.NotNil(t, message.rec.body)
}

// TestASingleValidationErrorTakesTheSamePath: processValidationError wraps and delegates,
// so it must negotiate identically.
func TestASingleValidationErrorTakesTheSamePath(t *testing.T) {
	single := &error2.ValidationError{
		Field: "email", Err: "email is required", Rule: "required", Path: "email",
	}

	api := negotiationMessageFor("/api/widgets", jsonHeaders)
	processValidationError(single, api)
	assert.Equal(t, http.StatusUnprocessableEntity, api.rec.status)

	browser := messageWithReferer("/widgets", "http://example.invalid/form")
	processValidationError(single, browser)
	assert.Equal(t, http.StatusSeeOther, browser.rec.status)
}

// TestAForeignTypeNamedValidationErrorsDoesNotPanic covers 7b. Catch dispatches on the
// error's *bare type name* (see Catch), so a type called ValidationErrors from any package
// reaches this handler — and the assertion it replaces was unchecked.
func TestAForeignTypeNamedValidationErrorsDoesNotPanic(t *testing.T) {
	message := negotiationMessageFor("/api/widgets", jsonHeaders)

	require.NotPanics(t, func() {
		processValidationErrors(&ValidationErrors{}, message)
	})
	assert.Equal(t, http.StatusInternalServerError, message.rec.status,
		"an unrecognised error falls through to the default handler")

	single := negotiationMessageFor("/api/widgets", jsonHeaders)
	require.NotPanics(t, func() {
		processValidationError(&ValidationError{}, single)
	})
	assert.Equal(t, http.StatusInternalServerError, single.rec.status)
}

// TestCatchRoutesAForeignTypeHere proves the premise rather than asserting it in prose:
// Catch really does dispatch a same-named type from another package to this handler.
func TestCatchRoutesAForeignTypeHere(t *testing.T) {
	message := negotiationMessageFor("/api/widgets", jsonHeaders)

	require.NotPanics(t, func() { Catch(&ValidationErrors{}, message) })
	assert.True(t, message.rec.written)
}

// ValidationErrors and ValidationError are deliberately named to collide with
// err.ValidationErrors / err.ValidationError under Catch's name-based dispatch.
type ValidationErrors struct{}

func (ValidationErrors) Error() string { return "a different package's ValidationErrors" }

type ValidationError struct{}

func (ValidationError) Error() string { return "a different package's ValidationError" }

// TestTheFlashPayloadStillCarriesTheErrors: the browser branch is the one that survives
// unchanged in spirit, and a redirect that drops the errors would leave the form with
// nothing to render.
func TestTheFlashPayloadStillCarriesTheErrors(t *testing.T) {
	message := messageWithReferer("/widgets", "http://example.invalid/form")

	processValidationErrors(validationFixture(), message)

	require.NotNil(t, message.rec.flash)
	flashed, ok := message.rec.flash["validation"].(*error2.ValidationErrors)
	require.True(t, ok, "the flash must carry the errors under the key a view reads")
	require.Len(t, *flashed, 2)
	assert.Equal(t, "email", (*flashed)[0].Field)
}
