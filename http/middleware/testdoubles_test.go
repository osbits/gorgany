package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/osbits/gorgany/v2/app/core"
)

// The doubles below embed the framework interface they stand in for, so they
// satisfy the whole interface while implementing only the parts the middlewares
// under test actually touch. A middleware reaching for anything else fails the test
// loudly with a nil-method panic instead of being silently accommodated.

// ------------------------------------------------------------------- request

type fakeRequest struct {
	core.IRequestScope

	raw        *http.Request
	pathParams map[string]string
}

func (r *fakeRequest) RawRequest() *http.Request { return r.raw }

// Body exists because the CSRF middleware reads the form through grghttp.PostFormValues now
// rather than calling req.ParseForm itself — ParseForm consumed the body the handler needed,
// which is why mounting the middleware on a form POST broke it. This double does not
// implement the optional PostForm, so it exercises the fallback, and the fallback reads the
// body. Without this the embedded nil core.IRequestScope is what answers.
func (r *fakeRequest) Body() ([]byte, error) {
	if r.raw == nil || r.raw.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.raw.Body)
	r.raw.Body.Close()
	r.raw.Body = io.NopCloser(bytes.NewReader(body))
	return body, err
}

func (r *fakeRequest) Header() http.Header {
	if r.raw == nil {
		return http.Header{}
	}
	return r.raw.Header
}

func (r *fakeRequest) PathParam(name string) string { return r.pathParams[name] }

// ------------------------------------------------------------------ response

// recordedResponse captures whatever a middleware wrote.
type recordedResponse struct {
	Status      int
	Body        any
	Text        string
	RedirectURL string
	Bytes       []byte
	Written     bool
	Headers     http.Header
}

type fakeResponse struct {
	core.IResponseScope

	recorded *recordedResponse
}

func (w *fakeResponse) JSON(v any, code int) {
	w.recorded.Written = true
	w.recorded.Status = code
	w.recorded.Body = v
}

func (w *fakeResponse) Text(body string, code int) {
	w.recorded.Written = true
	w.recorded.Status = code
	w.recorded.Text = body
}

func (w *fakeResponse) Bytes(b []byte, code int) {
	w.recorded.Written = true
	w.recorded.Status = code
	w.recorded.Bytes = b
}

func (w *fakeResponse) Redirect(url string, code int) {
	w.recorded.Written = true
	w.recorded.Status = code
	w.recorded.RedirectURL = url
}

func (w *fakeResponse) Header() http.Header {
	if w.recorded.Headers == nil {
		w.recorded.Headers = http.Header{}
	}
	return w.recorded.Headers
}

func (w *fakeResponse) SetHeader(key, value string) { w.Header().Set(key, value) }

// ------------------------------------------------------------------- message

type fakeMessage struct {
	core.HttpMessage

	req      *fakeRequest
	res      *fakeResponse
	ctx      context.Context
	recorded *recordedResponse

	// session is only consulted by the middlewares that install one. Left nil, a
	// middleware reaching for it fails loudly rather than being accommodated.
	session core.ISessionScope
}

func (m *fakeMessage) Request() core.IRequestScope   { return m.req }
func (m *fakeMessage) Response() core.IResponseScope { return m.res }
func (m *fakeMessage) Context() context.Context      { return m.ctx }
func (m *fakeMessage) Session() core.ISessionScope   { return m.session }

// fakeSessionScope records whichever session a middleware installs.
type fakeSessionScope struct {
	core.IEditableSessionScope

	current core.ISession
}

func (s *fakeSessionScope) Get() core.ISession  { return s.current }
func (s *fakeSessionScope) Set(v core.ISession) { s.current = v }

// newMessage builds a message around a synthetic request.
func newMessage(method, target string, headers map[string]string) *fakeMessage {
	raw := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		raw.Header.Set(k, v)
	}

	recorded := &recordedResponse{}
	return &fakeMessage{
		req:      &fakeRequest{raw: raw, pathParams: map[string]string{}},
		res:      &fakeResponse{recorded: recorded},
		ctx:      context.Background(),
		recorded: recorded,
	}
}

// envelope decodes a recorded JSON body through the framework's marshaller, so a
// test asserts the shape a client would actually receive.
func envelope(body any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
