// http/httprequest_scope.go
package http

import (
	"context"
	"github.com/go-chi/chi"
	"github.com/osbits/gorgany/v2/app/core"
	"net/http"
)

// HttpMessage aggregates all HTTP scopes for a single request.
type HttpMessage struct {
	Req  *HTTPRequestScope
	Res  *HTTPResponseScope
	Sess *HTTPSessionScope
	View *HTTPViewScope
	ctx  context.Context
}

// NewHttpMessage builds a new HttpMessage with its scopes.
func NewHttpMessage(w http.ResponseWriter, r *http.Request) *HttpMessage {
	req := NewHTTPRequestScope(r)
	res := NewHTTPResponseScope(w, r)
	sess := NewHTTPSessionScope(w, r)
	view := NewHTTPViewScope()

	msg := &HttpMessage{Req: req, Res: res, Sess: sess, View: view}
	// embed Message in context for downstream access
	msg.ctx = context.WithValue(r.Context(), core.MessageContextKey, msg)
	return msg
}

// Context returns the request-scoped context containing IMessageContext.
func (m *HttpMessage) Context() context.Context {
	return m.ctx
}

// Param retrieves a URL path parameter via chi.
func (m *HttpMessage) Param(key string) string {
	return chi.URLParam(m.Req.Request, key)
}

// Close performs any cleanup. Currently no-op.
func (m *HttpMessage) Close() error {
	m.Sess.ClearOneTime()
	return nil
}
