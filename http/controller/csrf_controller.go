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
//
// It is deliberately self-sufficient: it starts a session itself when the request carries
// none, so it does not depend on being covered by the session middleware. GetRoutes mounts
// it at the root with no middleware, and an app's session middleware is normally scoped to
// the namespace it protects — so requiring the middleware would have meant the framework's
// own default registration could never work (H2).
type CsrfController struct {
	AuthContext core.IAuthContext `container:"inject"`
	CsrfService *auth.CsrfService `container:"inject"`

	// SessionStorage persists the session the token is bound to.
	//
	// It is needed because ISession.SetItem is in-memory only: DbSessionEntity.SetItem
	// writes the Attributes map and nothing else, so a token minted here would be lost on
	// the next request unless AddSession upserts the row. SessionMiddleware does exactly
	// this after GenerateCSRFToken, for the same reason.
	SessionStorage core.ISessionStorage `container:"inject"`

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
		// H2. This used to answer 403 "the CSRF endpoint must be covered by the session
		// middleware" — advice the app could not act on. GetRoutes registers the endpoint at
		// the root (/csrf) with no middleware, and an app's session middleware is normally
		// scoped to the namespace it protects (`/api/**`), so the framework's own default
		// registration put the endpoint outside its own filter. A client's very first call —
		// before any session exists — got a permanent 403, and the only escape was
		// DisableCsrfController() plus a hand-registered copy, which made an endpoint that
		// exists to spare apps that work mandatory to reimplement.
		//
		// Creating the session here is not a shortcut around the middleware: it is the same
		// call SessionMiddleware makes for any request arriving without one
		// (session_middleware.go:84), including the Set-Cookie that binds it. The endpoint
		// now works wherever it is mounted, which is what "registered by default" has to mean.
		created, err := strategy.NewSessionWithoutUser(message.Context())
		if err != nil {
			message.Response().JSON(
				dto.ReturnObject(nil, core.InternalErrorHttpStatus, "Could not start a session"),
				core.InternalErrorHttpStatus.Status)
			return
		}
		if created == nil {
			// JwtAuthStrategy.NewSessionWithoutUser returns (nil, nil) by design: a bearer
			// token is not sent automatically by the browser, so there is no CSRF exposure
			// and no token to issue. Request-dependent rather than a misconfiguration —
			// ResolveAuthStrategyByContext picks per request — so it is reported as a client
			// error naming the reason, not as a 500.
			message.Response().JSON(
				dto.ReturnObject(nil, core.BadRequestHttpStatus,
					"The authentication strategy for this request does not use sessions, so "+
						"there is no CSRF token to issue; CSRF protection applies to "+
						"cookie-based sessions"),
				core.BadRequestHttpStatus.Status)
			return
		}

		session = created
		if editable, ok := message.Session().(core.IEditableSessionScope); ok {
			// So the rest of this request sees the session, as it would have had the
			// middleware created it.
			editable.Set(session)
		}
	}

	token, err := thiz.CsrfService.GetCSRFToken(message.Context(), session)
	if err != nil {
		message.Response().JSON(
			dto.ReturnObject(nil, core.InternalErrorHttpStatus, "Could not issue a CSRF token"),
			core.InternalErrorHttpStatus.Status)
		return
	}

	// Unconditionally, not only on the freshly created path: GetCSRFToken mints a token for
	// an existing session that has none, and that SetItem is in-memory too.
	if thiz.SessionStorage != nil {
		thiz.SessionStorage.AddSession(session)
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
