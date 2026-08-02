package controller

import (
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/http/middleware"
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
// none, so it does not depend on being covered by the session middleware. GetRoutes mounts it
// at the root outside an app's middleware patterns, and an app's session middleware is
// normally scoped to the namespace it protects — so requiring the middleware would have meant
// the framework's own default registration could never work (H2).
//
// Because that makes it a public route that writes to `sessions`, it carries one middleware of
// its own: a rate limiter. See Rate and DefaultCsrfRate.
type CsrfController struct {
	AuthContext core.IAuthContext `container:"inject"`
	CsrfService *auth.CsrfService `container:"inject"`

	// SessionStorage is injected but deliberately not written to.
	//
	// Token() used to end with an unconditional AddSession, on the belief that
	// ISession.SetItem is in-memory only. It is not: the session a DB-backed store hands
	// out writes through to its row on every SetItem, and a memory store hands out the
	// very object it holds, so the upsert was redundant on both backends. What it was not
	// is harmless. This endpoint is registered by default, needs no authentication, and
	// resolves the session at the top of the handler — so a request whose session was
	// revoked in between (a concurrent logout, or a logout already handled by another
	// replica) asked storage to write that session back, user id and all. Reproduced on
	// both backends: a plain map insert for memory, an INSERT for the database.
	//
	// The field stays because it is part of the controller's wired shape and an app may
	// have set it; nothing here uses it.
	SessionStorage core.ISessionStorage `container:"inject"`

	// Path is where the endpoint is mounted. Empty means core.DefaultCSRFTokenPath.
	Path string

	// Rate caps how often one client may call this endpoint. Zero means
	// DefaultCsrfRate; DisableRateLimit turns it off.
	//
	// I2. H2 made this endpoint start a session when the request carries none, which turned
	// it into the one public route in a typical app that writes a row to `sessions`: only
	// RecoveryMiddleware is global by default, rate limiting is opt-in, this route is
	// registered with no middleware, and an app's session middleware is scoped to the
	// namespace it protects. Before H2 it answered 403 and wrote nothing; after, a curl loop
	// grew the table without bound.
	//
	// Together with H4's sweep this makes the exposure bounded rather than merely slower:
	// steady-state rows are at most rate × session lifetime, because the GC collects each
	// row once it expires. Neither half is sufficient alone — the limit without the sweep
	// only slows the growth, and the sweep without the limit collects rows that arrive
	// faster than a lifetime.
	Rate middleware.RateLimit

	// DisableRateLimit removes the limiter, for an app that meters at the edge instead.
	DisableRateLimit bool

	// TrustRateLimitForwardedFor keys the limit on X-Forwarded-For / X-Real-IP.
	//
	// Read this before deploying behind a proxy. Off by default, because those headers are
	// caller-supplied and trusting them when there is no proxy lets any client pick its own
	// bucket and rotate through unlimited ones — which is the same as no limit at all. But
	// left off *behind* a proxy, every request appears to come from the proxy and the whole
	// app shares one bucket, so the limit becomes global rather than per-client. Set it when
	// a trusted proxy sets the header, and note that DefaultCsrfRate is deliberately loose
	// enough to survive the misconfiguration either way.
	//
	// Not routed through message.Request().IP(), which trusts both headers unconditionally
	// and so cannot be used for anything an attacker should not be able to choose.
	TrustRateLimitForwardedFor bool
}

// DefaultCsrfRate is the per-client limit on the CSRF endpoint.
//
// Deliberately loose. Legitimate use is one call per client per session — SessionMiddleware
// publishes the token on every session-carrying response, so this endpoint is the fallback,
// not the steady state. The number therefore has to clear the worst *plausible* legitimate
// burst rather than the average: an office behind one NAT whose staff all open the app at
// 09:00, or an app behind a proxy with TrustRateLimitForwardedFor left off, where every
// request shares a single bucket.
//
// 300/minute clears both by a wide margin while still capping an abusive loop four orders of
// magnitude below what it could otherwise do, and — with H4's sweep — bounding the table at
// roughly 300 × session-lifetime rows. Breaking a real deployment to slow an attacker who
// can simply use more IP addresses would be the wrong trade.
var DefaultCsrfRate = middleware.PerMinute(300)

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

		// So the rest of this request sees the session, as it would have had the middleware
		// created it — the session scope and the message context both cache it, and the
		// strategy consults the second one before the cookie. See http.PublishSession.
		session = created
		grghttp.PublishSession(message, session)
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
			Name:        "gorgany.csrf.token",
			Path:        path,
			Method:      core.GET,
			Handler:     thiz.Token,
			Middlewares: thiz.middlewares(),
		},
	}
}

// middlewares returns the limiter, or nothing when it is disabled.
//
// Attached to the route rather than consulted inside Token, so it inherits the middleware's
// own client-IP derivation — which defaults to RemoteAddr and only reads X-Forwarded-For when
// asked. Deriving the key here instead would mean a second copy of security-sensitive code,
// and the obvious shortcut, message.Request().IP(), trusts the header unconditionally.
//
// It meters every call, not only the ones that create a session. Metering just the creation
// branch would spare a client that already holds a session, but the burst that matters — a
// crowd of first-time loads from one address — creates a session every time, so it would not
// help the case the limit has to survive. One mechanism, in one place, is worth more.
func (thiz CsrfController) middlewares() []core.IMiddleware {
	if thiz.DisableRateLimit {
		return nil
	}

	rate := thiz.Rate
	if rate.Validate() != nil {
		rate = DefaultCsrfRate
	}

	limiter := middleware.NewRateLimitMiddleware(rate)
	limiter.TrustForwardedFor = thiz.TrustRateLimitForwardedFor

	return []core.IMiddleware{limiter}
}
