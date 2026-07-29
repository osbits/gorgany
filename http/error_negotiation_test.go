package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/osbits/gorgany/app/core"
	error2 "github.com/osbits/gorgany/err"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C2: every framework error handler wrote text/plain, several of them with an *empty*
// body, so an API client got nothing it could parse and never the standard envelope.
// T3.4 had fixed negotiation for the auth middleware only.

type negotiationRecorder struct {
	status  int
	body    any
	text    string
	written bool
	headers http.Header
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
