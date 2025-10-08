package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

type ResponseWriterWrapper struct {
	http.Flusher
	http.Hijacker
	io.ReaderFrom
	http.ResponseWriter
	io.StringWriter
	io.Writer

	StatusCode int
	Body       io.ReadCloser
	Headers    http.Header
}

func (thiz *ResponseWriterWrapper) WriteHeader(code int) {
	thiz.StatusCode = code
	thiz.ResponseWriter.WriteHeader(code)
}

func (thiz *ResponseWriterWrapper) Header() http.Header {
	thiz.Headers = thiz.ResponseWriter.Header()
	return thiz.ResponseWriter.Header()
}

func (thiz *ResponseWriterWrapper) Write(b []byte) (int, error) {
	thiz.Body = io.NopCloser(bytes.NewBuffer(b))
	return thiz.ResponseWriter.Write(b)
}

// HTTPResponseScope handles writing HTTP responses.
type HTTPResponseScope struct {
	Writer  http.ResponseWriter
	Request *http.Request
}

// NewHTTPResponseScope constructs a HTTPResponseScope.
func NewHTTPResponseScope(w http.ResponseWriter, r *http.Request) *HTTPResponseScope {
	return &HTTPResponseScope{Writer: w, Request: r}
}

// GetRawResponse returns the raw http.ResponseWriter.
func (rc *HTTPResponseScope) GetRawResponse() http.ResponseWriter {
	return rc.Writer
}

// Text writes a plain text response with status code.
func (rc *HTTPResponseScope) Text(body string, statusCode int) {
	rc.Writer.WriteHeader(statusCode)
	if _, err := rc.Writer.Write([]byte(body)); err != nil {
		panic(err)
	}
}

// JSON writes a JSON response with status code.
func (rc *HTTPResponseScope) JSON(data any, statusCode int) {
	payload, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	rc.Writer.Header().Set("Content-Type", "application/json")
	rc.Text(string(payload), statusCode)
}

// Redirect performs HTTP redirect using the original request for correct Location header.
func (rc *HTTPResponseScope) Redirect(url string, code int) {
	http.Redirect(rc.Writer, rc.Request, url, code)
}
