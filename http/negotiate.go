package http

import (
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/service/dto"
)

// WantsJSON decides whether the caller is an API client, and therefore whether an error
// should be the standard JSON envelope or an HTML-shaped response.
//
// It used to test only `Content-Type == application/json` or
// `PathParam("namespace") == "api"`. A GET request carries no Content-Type, so the first
// test could never fire for the most common case and apps were pushed into an `api`
// namespace purely to get a JSON 401 (T3.4).
//
// It lives here, exported, because three separate places need the same decision — the
// auth middleware's 401/403, the router's 404/405, and the framework's error handlers.
// The alternative was three copies that would answer differently for the same request.
func WantsJSON(message core.HttpMessage) bool {
	if message == nil {
		return false
	}

	request := message.Request()
	if request == nil {
		return false
	}

	if request.PathParam("namespace") == "api" {
		return true
	}

	if ClientAskedForJSON(message) {
		return true
	}

	if req := request.RawRequest(); req != nil && req.URL != nil {
		path := req.URL.Path
		if strings.HasPrefix(path, "/api/") || path == "/api" {
			return true
		}
	}

	return false
}

// ClientAskedForJSON reports whether the *client* named JSON, through Accept or Content-Type.
//
// It is the header-only half of WantsJSON, split out because one caller must not use the
// other half. WantsJSON also answers true for anything under `/api/`, which is right for
// choosing an error's shape — an app's API namespace should get envelopes — but wrong for
// SpaController's deep-link fallback, where the *path* question is already owned by
// ExcludedPrefixes. An app that sets `ExcludedPrefixes = []string{}` is explicitly asking for
// /api/** to reach the SPA, and a path rule buried in a negotiation helper must not overrule
// that. What the client put in its Accept header is orthogonal to any of it.
//
// Accept is matched on JSON specifically, never on the wildcard: a browser navigation sends
// `text/html,application/xhtml+xml,...,*/*;q=0.8`, and matching `*/*` there would treat every
// page load as an API call.
func ClientAskedForJSON(message core.HttpMessage) bool {
	if message == nil {
		return false
	}

	request := message.Request()
	if request == nil {
		return false
	}

	req := request.RawRequest()
	if req == nil {
		return strings.Contains(
			request.Header().Get("Content-Type"),
			core.ApplicationJson.String(),
		)
	}

	if strings.Contains(req.Header.Get("Content-Type"), core.ApplicationJson.String()) {
		return true
	}

	return strings.Contains(req.Header.Get("Accept"), core.ApplicationJson.String())
}

// WriteNegotiatedError answers with the standard envelope for an API client and plain text
// for everyone else.
//
// It lives beside WantsJSON for the same reason WantsJSON does: four places need to answer a
// framework-level failure and they must answer it identically — the auth middleware's
// 401/403, the router's 404/405, the error handlers, and SpaController's excluded paths.
//
// That last one is why this moved out of http/router. SpaController registers a `/*`
// catch-all, and chi matches a catch-all before falling through to NotFound, so mounting the
// SPA shadowed the router's negotiated 404 for *every* GET: a mistyped `/api/v1/widget`
// returned 200 with an HTML document, and an excluded path returned plain text. Routing the
// SPA's own refusals through this makes the response identical whether the SPA is mounted or
// not, which is the property that matters — an app should not have to know.
func WriteNegotiatedError(message core.HttpMessage, status core.HttpStatus, reason string) {
	if WantsJSON(message) {
		message.Response().JSON(dto.ReturnObject(nil, status, reason), status.Status)
		return
	}

	message.Response().Text(reason, status.Status)
}
