package middleware

import (
	"net/http"
	"strings"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/log"
	"github.com/osbits/gorgany/service/dto"
	"github.com/spf13/viper"
)

// AuthMiddleware authenticates a request against one or more auth strategies and,
// when Roles is set, authorises it.
//
// Three defects were fixed in v2:
//
//   - CurrentUser can return (nil, nil) on several paths, and the code checked only
//     err before calling user.GetRole() — a nil dereference that took the
//     connection down.
//   - A role mismatch produced 401, telling an authenticated user with the wrong
//     role that they were unauthenticated. A role mismatch is now 403; 401 is
//     reserved for a missing or invalid session.
//   - The JSON-vs-HTML decision keyed on Content-Type == application/json or
//     PathParam("namespace") == "api". A GET carries no Content-Type, so an app was
//     forced to put every route in an `api` namespace just to get a JSON 401. The
//     Accept header and an /api/ path prefix are now honoured too.
type AuthMiddleware struct {
	Roles          []core.UserRole
	AuthStrategies []string
	AuthContext    core.IAuthContext `container:"inject"`
}

var _ core.IMiddleware = (*AuthMiddleware)(nil)

func (thiz AuthMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		strategies := thiz.AuthStrategies
		if len(strategies) == 0 {
			strategies = []string{core.DefaultKeyInRegistrar}
		}

		// authenticated records whether any strategy recognised the caller. It is
		// what separates "who are you?" (401) from "not allowed" (403).
		authenticated := false

		for _, strategyName := range strategies {
			strategy := thiz.AuthContext.Strategy(strategyName)
			if strategy == nil {
				log.Log().Warnf("auth middleware: no strategy registered under %q", strategyName)
				continue
			}

			if !strategy.IsLoggedIn(message.Context()) {
				continue
			}

			authenticated = true

			if len(thiz.Roles) == 0 {
				next(message)
				return
			}

			user, err := strategy.CurrentUser(message.Context())
			if err != nil {
				// A broken user lookup is not the caller's fault and is not a
				// credential problem, so it must not be reported as one. Let the
				// registered error handlers decide; RecoveryMiddleware re-dispatches
				// this through them.
				panic(err)
			}
			if user == nil {
				// IUserService.Get/GetByUsername are documented as never returning
				// (nil, nil), but an app supplies that implementation. A session
				// pointing at a user who no longer exists is not authenticated.
				log.Log().Warnf(
					"auth middleware: strategy %q reports a logged-in session but no user; treating as unauthenticated",
					strategyName)
				authenticated = false
				continue
			}

			for _, role := range thiz.Roles {
				if role == user.GetRole() {
					next(message)
					return
				}
			}
		}

		if authenticated {
			// Authenticated, wrong role. 403, not 401.
			thiz.deny(message, core.ForbiddenHttpStatus)
			return
		}

		thiz.deny(message, core.NotAuthorizedHttpStatus)
	}
}

// deny answers with the standard envelope for an API client, or redirects a browser
// to the login form.
func (thiz AuthMiddleware) deny(message core.HttpMessage, status core.HttpStatus) {
	if wantsJSON(message) {
		message.Response().JSON(dto.ReturnObject(nil, status, nil), status.Status)
		return
	}

	// A forbidden browser request is not fixed by logging in again, so it is not
	// worth a redirect to the login form.
	if status == core.ForbiddenHttpStatus {
		message.Response().Text("Forbidden", http.StatusForbidden)
		return
	}

	loginFormUrl := viper.GetString("auth.login.formUrl")
	if loginFormUrl == "" {
		loginFormUrl = core.DefaultLoginUrl
	}
	message.Response().Redirect(loginFormUrl, http.StatusFound)
}

// wantsJSON decides whether the caller is an API client.
//
// It used to test only `Content-Type == application/json` or
// `PathParam("namespace") == "api"`. A GET request carries no Content-Type, so the
// first test could never fire for the most common case and apps were pushed into an
// `api` namespace purely to get a JSON 401.
func wantsJSON(message core.HttpMessage) bool {
	if message.Request().PathParam("namespace") == "api" {
		return true
	}

	req := message.Request().RawRequest()
	if req == nil {
		return strings.Contains(
			message.Request().Header().Get("Content-Type"),
			core.ApplicationJson.String(),
		)
	}

	if strings.Contains(req.Header.Get("Content-Type"), core.ApplicationJson.String()) {
		return true
	}

	// Honour Accept, but only when JSON is asked for specifically: a browser sends
	// `Accept: text/html,...,*/*`, and matching the wildcard there would turn every
	// browser redirect into a JSON body.
	if strings.Contains(req.Header.Get("Accept"), core.ApplicationJson.String()) {
		return true
	}

	if req.URL != nil {
		path := req.URL.Path
		if strings.HasPrefix(path, "/api/") || path == "/api" {
			return true
		}
	}

	return false
}
