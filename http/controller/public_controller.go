package controller

import (
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/http/router"
)

// inlineSafeContentTypes are the types this controller is willing to name on a response.
//
// Everything else is answered as application/octet-stream, and the two that matter are the
// two that used to get through: text/html and image/svg+xml. `mime.TypeByExtension` was
// asked what the file's extension meant and the answer was sent verbatim, so an upload
// stored as `…-payload.html` came back as `text/html; charset=utf-8` from the application's
// own origin — script in it same-origin with the app, able to read the CSRF token the
// session middleware publishes on every response and to call any endpoint the visitor is
// authenticated for. `.svg` reached the same place through `image/svg+xml`, which browsers
// also execute script in.
//
// The allowlist is small on purpose: this is the endpoint that serves whatever an app's
// users have uploaded, and a type that is not here loses nothing but an inline preview. The
// image/, audio/, video/ and font/ families are admitted wholesale below, minus SVG.
var inlineSafeContentTypes = map[string]bool{
	"text/plain":             true,
	"text/css":               true,
	"text/csv":               true,
	"text/javascript":        true,
	"application/javascript": true,
	"application/json":       true,
	"application/pdf":        true,
}

const octetStream = "application/octet-stream"

func NewPublicController() *PublicController {
	return &PublicController{}
}

type PublicController struct {
}

func (thiz PublicController) load(message core.HttpMessage) {
	r := message.Request().RawRequest()
	url := r.URL
	path := url.Path

	// Clean and validate the path
	cleanPath := filepath.Clean(path)
	if strings.HasPrefix(cleanPath, "..") || strings.Contains(cleanPath, "..") {
		message.Response().Text("Invalid path", 400)
		return
	}

	// Ensure path starts with /public/
	if !strings.HasPrefix(cleanPath, "/public/") {
		message.Response().Text("Invalid path", 400)
		return
	}

	// Remove /public/ prefix to get the actual file path
	filePath := strings.TrimPrefix(cleanPath, "/public/")

	// Join with resource directory
	fullPath := filepath.Join("resource", filePath)

	// Verify the file exists and is within the resource directory
	absPath, err := filepath.Abs(fullPath)
	if err != nil {
		message.Response().Text("Internal server error", 500)
		return
	}

	absResource, err := filepath.Abs("resource")
	if err != nil {
		message.Response().Text("Internal server error", 500)
		return
	}

	// The separator is part of the prefix: without it a sibling directory whose name merely
	// starts with "resource" — resource-backups, resources — satisfies the containment
	// check.
	if absPath != absResource && !strings.HasPrefix(absPath, absResource+string(filepath.Separator)) {
		message.Response().Text("Invalid path", 400)
		return
	}

	// Read the file
	file, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			message.Response().Text("File not found", 404)
		} else {
			message.Response().Text("Internal server error", 500)
		}
		return
	}

	// Get file extension and mime type
	ext := filepath.Ext(filePath)
	if ext == "" {
		message.Response().Text("Invalid file type", 400)
		return
	}

	// Two headers alongside the type, and both are load-bearing for content this app did
	// not author. nosniff stops the browser from second-guessing a deliberately inert
	// Content-Type and rendering the bytes as something else; attachment keeps a top-level
	// navigation to the file from becoming a page on this origin at all.
	//
	// Content-Disposition does not apply to a subresource load, so stylesheets, scripts,
	// images and fonts referenced from a page keep loading. The retyping does apply to them,
	// though, and there is one casualty worth naming: an SVG is now answered as
	// application/octet-stream, and nosniff means a browser will refuse to render it — an
	// <img src="logo.svg"> served from here stops working. There is no way to keep it that
	// does not also serve an uploaded SVG as image/svg+xml, which is a document that runs
	// script on this origin. An app with SVG assets should serve them from its own handler,
	// or from somewhere that is not also the upload root.
	message.Response().SetHeader("X-Content-Type-Options", "nosniff")
	message.Response().SetHeader("Content-Disposition", attachmentDisposition(filepath.Base(filePath)))
	message.Response().SetHeader("Content-Type", inlineSafeContentType(mime.TypeByExtension(ext)))
	message.Response().Bytes(file, 200)
}

// inlineSafeContentType maps a type derived from a file extension onto one this controller
// will name, falling back to application/octet-stream.
func inlineSafeContentType(kind string) string {
	if kind == "" {
		return octetStream
	}

	base, params, err := mime.ParseMediaType(kind)
	if err != nil {
		return octetStream
	}

	if base == "image/svg+xml" {
		return octetStream
	}

	switch {
	case strings.HasPrefix(base, "image/"),
		strings.HasPrefix(base, "audio/"),
		strings.HasPrefix(base, "video/"),
		strings.HasPrefix(base, "font/"):
		return formatContentType(base, params)
	}

	if inlineSafeContentTypes[base] {
		return formatContentType(base, params)
	}

	return octetStream
}

func formatContentType(base string, params map[string]string) string {
	if formatted := mime.FormatMediaType(base, params); formatted != "" {
		return formatted
	}
	return octetStream
}

// attachmentDisposition builds the Content-Disposition value.
//
// mime.FormatMediaType does the quoting and the RFC 2231 encoding, and returns "" for
// anything it cannot express — which is also what keeps a filename with a newline or a
// quote in it from writing a header of its own choosing.
func attachmentDisposition(name string) string {
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "attachment"
	}

	if formatted := mime.FormatMediaType("attachment", map[string]string{"filename": name}); formatted != "" {
		return formatted
	}
	return "attachment"
}

func (thiz PublicController) GetRoutes() []core.IRouteConfig {
	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:    "/public/*",
			Method:  core.GET,
			Handler: thiz.load,
		},
	}
}
