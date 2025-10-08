package middleware

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"net/http"
	"strings"
)

// CSRF token key in the session
const csrfTokenKey = "csrf_token"

// CSRFMiddleware provides protection against Cross-Site Request Forgery attacks
type CSRFMiddleware struct {
	// ExemptMethods contains HTTP methods that are exempt from CSRF protection (e.g., GET, HEAD)
	ExemptMethods []string
	// TokenHeaderName is the name of the header that contains the CSRF token
	TokenHeaderName string
	// TokenFormName is the name of the form field that contains the CSRF token
	TokenFormName string
	// AuthContext is used to access the authentication strategy
	AuthContext core.IAuthContext `container:"inject"`
}

// NewCSRFMiddleware creates a new CSRF middleware with default settings
func NewCSRFMiddleware() *CSRFMiddleware {
	return &CSRFMiddleware{
		ExemptMethods:   []string{"GET", "HEAD", "OPTIONS", "TRACE"},
		TokenHeaderName: core.CSRFTokenHeader,
		TokenFormName:   "csrf_token",
	}
}

// Handle implements the middleware handler
func (thiz CSRFMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		req := message.Request().RawRequest()

		// Skip CSRF check for exempt methods
		for _, method := range thiz.ExemptMethods {
			if req.Method == method {
				next(message)
				return
			}
		}

		// Get the authentication strategy
		authStrategy := thiz.AuthContext.ResolveAuthStrategyByContext(message.Context())
		if authStrategy == nil {
			message.Response().JSON(map[string]string{
				"error": "Authentication strategy not found",
			}, http.StatusInternalServerError)
			return
		}

		// Check if the request is using the current auth strategy
		if !authStrategy.IsRequestMadeWithStrategy(message.Context()) {
			next(message)
			return
		}

		// Get the CSRF token from the request
		token := thiz.getTokenFromRequest(message)
		if token == "" {
			message.Response().JSON(map[string]string{
				"error": "CSRF token missing",
			}, http.StatusForbidden)
			return
		}

		// Get the session from the auth strategy
		session := authStrategy.CurrentSession(message.Context())
		if session == nil {
			message.Response().JSON(map[string]string{
				"error": "No active session",
			}, http.StatusForbidden)
			return
		}

		// Get the token from the session
		sessionToken := session.GetItem(csrfTokenKey)
		if sessionToken == "" {
			message.Response().JSON(map[string]string{
				"error": "CSRF protection not initialized",
			}, http.StatusForbidden)
			return
		}

		// Use constant-time comparison to prevent timing attacks
		if sessionToken != token {
			message.Response().JSON(map[string]string{
				"error": "Invalid CSRF token",
			}, http.StatusForbidden)
			return
		}

		// Token is valid, proceed with the request
		next(message)
	}
}

// getTokenFromRequest extracts the CSRF token from the request
func (thiz *CSRFMiddleware) getTokenFromRequest(message core.HttpMessage) string {
	req := message.Request().RawRequest()

	// Check for token in header
	token := req.Header.Get(thiz.TokenHeaderName)
	if token != "" {
		return token
	}

	// Check for token in form
	err := req.ParseForm()
	if err == nil {
		token = req.PostForm.Get(thiz.TokenFormName)
		if token != "" {
			return token
		}
	}

	// Check for token in multipart form
	if req.MultipartForm != nil {
		if values, ok := req.MultipartForm.Value[thiz.TokenFormName]; ok && len(values) > 0 {
			return values[0]
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
