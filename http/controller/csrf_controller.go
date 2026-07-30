package controller

import (
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	"github.com/osbits/gorgany/v2/http/router"
	"github.com/osbits/gorgany/v2/service/dto"
)

// CsrfController hands a CSRF token to a client that asks for one.
//
// Before v2.0 there was no way to get a token at all: SessionMiddleware set
// X-CSRF-Token on the single response that created a session and nowhere else,
// CsrfService.GetCSRFToken had zero call sites, and no route exposed a token. A SPA
// that missed that one response — a reload, a second tab, a session that already
// existed — could not recover, and once v2 closed the CSRF middleware's two bypasses
// every mutating request such a client made was reliably rejected.
//
// This controller is registered by default (see RouteProvider.DisableCsrfController).
// The companion half of the fix is SessionMiddleware publishing the token on *every*
// session-carrying response, so a client that reads the header as it goes rarely needs
// this endpoint after boot.
type CsrfController struct {
	AuthContext core.IAuthContext `container:"inject"`
	CsrfService *auth.CsrfService `container:"inject"`

	// Path is where the endpoint is mounted. Empty means core.DefaultCSRFTokenPath.
	Path string
}

func NewCsrfController() *CsrfController {
	return &CsrfController{}
}

var _ core.IController = (*CsrfController)(nil)

// Token returns the session's CSRF token, in the response body and in the header.
//
// The body carries it because a SPA calling this on boot wants to read it without
// worrying about whether the header is exposed — a cross-origin fetch cannot see
// X-CSRF-Token unless the server lists it in Access-Control-Expose-Headers. The
// header is set as well so a same-origin client can use one uniform code path for
// this response and every other one.
func (thiz CsrfController) Token(message core.HttpMessage) {
	if thiz.CsrfService == nil {
		message.Response().JSON(
			dto.ReturnObject(nil, core.InternalErrorHttpStatus, "CSRF service is not available"),
			core.InternalErrorHttpStatus.Status)
		return
	}

	strategy := thiz.AuthContext.ResolveAuthStrategyByContext(message.Context())
	if strategy == nil {
		message.Response().JSON(
			dto.ReturnObject(nil, core.InternalErrorHttpStatus, "Authentication strategy not found"),
			core.InternalErrorHttpStatus.Status)
		return
	}

	session := strategy.CurrentSession(message.Context())
	if session == nil {
		// SessionMiddleware creates a session for any request that lacks one, so
		// reaching this means the endpoint was mounted outside that filter. Say so,
		// rather than returning a token bound to nothing.
		message.Response().JSON(
			dto.ReturnObject(nil, core.ForbiddenHttpStatus,
				"No active session; the CSRF endpoint must be covered by the session middleware"),
			core.ForbiddenHttpStatus.Status)
		return
	}

	token, err := thiz.CsrfService.GetCSRFToken(message.Context(), session)
	if err != nil {
		message.Response().JSON(
			dto.ReturnObject(nil, core.InternalErrorHttpStatus, "Could not issue a CSRF token"),
			core.InternalErrorHttpStatus.Status)
		return
	}

	message.Response().Header().Set(core.CSRFTokenHeader, token)
	message.Response().JSON(
		dto.ReturnObject(map[string]string{
			"csrf_token": token,
			"header":     core.CSRFTokenHeader,
		}, core.SuccessHttpStatus, nil),
		core.SuccessHttpStatus.Status)
}

func (thiz CsrfController) GetRoutes() []core.IRouteConfig {
	path := thiz.Path
	if path == "" {
		path = core.DefaultCSRFTokenPath
	}

	return []core.IRouteConfig{
		&router.RouteConfig{
			Name:    "gorgany.csrf.token",
			Path:    path,
			Method:  core.GET,
			Handler: thiz.Token,
		},
	}
}
