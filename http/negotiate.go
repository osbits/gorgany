package http

import (
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
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
