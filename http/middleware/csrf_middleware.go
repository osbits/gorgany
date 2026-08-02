package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/service/dto"
)

// CSRF token key in the session
// csrfTokenKey is core.CSRFSessionKey, not a second literal — see the note on the
// matching constant in auth/csrf_service.go.
const csrfTokenKey = core.CSRFSessionKey

// CSRFMiddleware provides protection against Cross-Site Request Forgery attacks.
//
// Two independent bypasses were closed in v2:
//
//  1. OPTIONS was on the exempt list, and the router registers every route under
//     OPTIONS as well as its declared method (http/router/gorgany.go). Any mutating
//     handler was therefore reachable via OPTIONS with no token, and it ran. The
//     exempt list is now the safe-method set {GET, HEAD, TRACE}, and OPTIONS is
//     answered here with 204 without the handler ever being invoked.
//  2. The check was skipped entirely when the auth strategy reported the request
//     was not made with it — "no cookie, no check". A request arriving without a
//     session is exactly the shape a cross-site forgery has, so the absence of a
//     session can never be grounds for skipping.
//
// Rejections now use the framework's standard response envelope rather than a bare
// {"error": "..."}, and the token comparison is constant-time.
type CSRFMiddleware struct {
	// ExemptMethods contains HTTP methods that are exempt from CSRF protection.
	// It must only ever hold methods that are safe by definition — ones that do
	// not change server state.
	ExemptMethods []string
	// TokenHeaderName is the name of the header that contains the CSRF token
	TokenHeaderName string
	// TokenFormName is the name of the form field that contains the CSRF token
	TokenFormName string
	// AuthContext is used to access the authentication strategy
	AuthContext core.IAuthContext `container:"inject"`
}

// NewCSRFMiddleware creates a new CSRF middleware with default settings.
func NewCSRFMiddleware() *CSRFMiddleware {
	return &CSRFMiddleware{
		// OPTIONS is deliberately absent: the router registers every route under
		// OPTIONS too, so exempting it exempted every mutating handler. It is
		// handled explicitly in Handle instead.
		ExemptMethods:   []string{http.MethodGet, http.MethodHead, http.MethodTrace},
		TokenHeaderName: core.CSRFTokenHeader,
		TokenFormName:   core.CSRFFormFieldName,
	}
}

var _ core.IMiddleware = (*CSRFMiddleware)(nil)

// Handle implements the middleware handler.
func (thiz CSRFMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		req := message.Request().RawRequest()

		// Answer preflight here and never reach the handler. The router registers
		// every route under OPTIONS as well as its declared method, so letting an
		// OPTIONS request through would run the mutating handler untokened.
		if req.Method == http.MethodOptions {
			message.Response().Bytes(nil, http.StatusNoContent)
			return
		}

		// Skip the CSRF check for safe methods only.
		for _, method := range thiz.ExemptMethods {
			if strings.EqualFold(req.Method, method) {
				next(message)
				return
			}
		}

		// Get the authentication strategy
		authStrategy := thiz.AuthContext.ResolveAuthStrategyByContext(message.Context())
		if authStrategy == nil {
			thiz.reject(message, core.InternalErrorHttpStatus, "Authentication strategy not found")
			return
		}

		// NOTE: there is deliberately no IsRequestMadeWithStrategy() short-circuit
		// here. A request with no session is precisely the shape a cross-site
		// forgery has, so its absence cannot excuse the check.

		// Get the CSRF token from the request
		token := thiz.getTokenFromRequest(message)
		if token == "" {
			thiz.reject(message, core.ForbiddenHttpStatus, "CSRF token missing")
			return
		}

		// Get the session from the auth strategy
		session := authStrategy.CurrentSession(message.Context())
		if session == nil {
			thiz.reject(message, core.ForbiddenHttpStatus, "No active session")
			return
		}

		// Get the token from the session
		sessionToken := session.GetItem(csrfTokenKey)
		if sessionToken == "" {
			thiz.reject(message, core.ForbiddenHttpStatus, "CSRF protection not initialized")
			return
		}

		// Constant-time comparison to prevent timing attacks. The previous `!=`
		// leaked the length of the matching prefix.
		if subtle.ConstantTimeCompare([]byte(sessionToken), []byte(token)) != 1 {
			thiz.reject(message, core.ForbiddenHttpStatus, "Invalid CSRF token")
			return
		}

		// Token is valid, proceed with the request
		next(message)
	}
}

// reject writes the framework's standard response envelope. The rejections used to
// emit a bare {"error": "..."} that no client parsing the standard shape could read.
func (thiz CSRFMiddleware) reject(message core.HttpMessage, status core.HttpStatus, reason string) {
	message.Response().JSON(dto.ReturnObject(nil, status, reason), status.Status)
}

// getTokenFromRequest extracts the CSRF token from the request
func (thiz *CSRFMiddleware) getTokenFromRequest(message core.HttpMessage) string {
	req := message.Request().RawRequest()

	// Check for token in header
	token := req.Header.Get(thiz.TokenHeaderName)
	if token != "" {
		return token
	}

	// Check the multipart form before anything else: for a multipart request ParseForm
	// consumes the body without populating PostForm.
	if req.MultipartForm != nil {
		if values, ok := req.MultipartForm.Value[thiz.TokenFormName]; ok && len(values) > 0 {
			return values[0]
		}
	}

	// Through the shared seam, not req.ParseForm.
	//
	// ParseForm reads the body and does not put it back, so looking for the token here used to
	// consume the credentials the login handler reads afterwards — which is why mounting this
	// middleware on /login made every login fail with "unable to find a user". See
	// grghttp.PostFormValues.
	values, err := grghttp.PostFormValues(message)
	if err == nil {
		if token = values.Get(thiz.TokenFormName); token != "" {
			return token
		}
	}

	return ""
}

func matches(pattern, path string) bool {
	if pattern == "" {
		return false
	}

	if pattern == "/**" {
		return true
	}

	if len(pattern) > 2 && strings.HasSuffix(pattern, "**") {
		return strings.HasPrefix(path, pattern[:len(pattern)-2])
	}
	return pattern == path
}
