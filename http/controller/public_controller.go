package controller

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/http/router"
	"github.com/osbits/gorgany/v2/model"
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

// NewPublicControllerAt mounts a second tree at a pattern of the app's choosing.
//
// The default controller is anchored at model.PublicStorage and only there. An app that
// deliberately serves something else — a legacy directory, a separate media root — says so
// here, in one reviewable line in its own provider, rather than by the framework's default
// being wide enough to cover it.
func NewPublicControllerAt(pattern, root string) *PublicController {
	return &PublicController{Root: root, Pattern: pattern}
}

type PublicController struct {
	// Root is the directory served. Empty means model.PublicStorage.
	Root string
	// Pattern is the route. Empty means "/public/*".
	Pattern string
}

// DefaultPublicPattern is where the built-in controller mounts.
const DefaultPublicPattern = "/public/*"

// root is the directory this controller serves.
//
// model.PublicStorage — `resource/public` — and not `resource`, which is what it used to
// join against. The upload path is the only thing that writes into the served tree and it
// writes exclusively under resource/public, so anchoring at resource made the write root a
// strict subset of the read root and everything else in there collateral: `/public/temp/…`
// reached in-flight and crash-orphaned uploads, `/public/view/…` the template sources,
// `/public/i18n/…` the translation files.
func (thiz PublicController) root() string {
	if thiz.Root != "" {
		return thiz.Root
	}
	return model.PublicStorage
}

// urlPrefix is the part of the request path that names this controller rather than the file.
func (thiz PublicController) urlPrefix() string {
	pattern := thiz.Pattern
	if pattern == "" {
		pattern = DefaultPublicPattern
	}
	return strings.TrimSuffix(pattern, "*")
}

func (thiz PublicController) load(message core.HttpMessage) {
	r := message.Request().RawRequest()

	// The lexical guards stay, all of them. os.Root below is defence in depth and not a
	// replacement: these produce the 400s the existing tests pin, and they refuse before any
	// syscall happens at all.
	cleanPath := filepath.Clean(r.URL.Path)
	if strings.Contains(cleanPath, "..") {
		message.Response().Text("Invalid path", 400)
		return
	}

	prefix := thiz.urlPrefix()
	if !strings.HasPrefix(cleanPath, prefix) {
		message.Response().Text("Invalid path", 400)
		return
	}

	filePath := strings.TrimPrefix(cleanPath, prefix)
	if filePath == "" {
		message.Response().Text("Invalid path", 400)
		return
	}

	// Containment and opening are one operation.
	//
	// os.Root holds a directory file descriptor and resolves every path component relative to
	// it, refusing any that escapes — including through a symlink, which the previous purely
	// lexical filepath.Abs comparison could not see at all (EvalSymlinks appears nowhere in
	// this repository). Resolving and then opening would also leave a window between the check
	// and the open; here there is none.
	//
	// Opened per request rather than once at construction: it is a single openat, it keeps the
	// CWD-relative semantics the tests and the deployment layout both depend on, and it
	// survives the directory not existing until the first upload.
	root, err := os.OpenRoot(thiz.root())
	if err != nil {
		if os.IsNotExist(err) {
			message.Response().Text("File not found", 404)
		} else {
			message.Response().Text("Internal server error", 500)
		}
		return
	}
	defer root.Close()

	file, err := root.Open(filepath.FromSlash(filePath))
	if err != nil {
		if os.IsNotExist(err) {
			message.Response().Text("File not found", 404)
		} else {
			// An escape, a symlink leaving the root, an embedded NUL: all refusals rather
			// than server errors, and none of them distinguishable to the caller.
			message.Response().Text("Invalid path", 400)
		}
		return
	}
	defer file.Close()

	// Stat on the open descriptor, so there is no second lookup to disagree with the first.
	info, err := file.Stat()
	if err != nil {
		message.Response().Text("Internal server error", 500)
		return
	}
	if info.IsDir() {
		message.Response().Text("File not found", 404)
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
	message.Response().SetHeader("ETag", etagFor(info))

	// Streamed rather than read into memory. os.ReadFile buffered the whole asset per request,
	// so a large upload cost its own size in heap on every download and a handful of
	// concurrent requests for it cost that many copies. ServeContent also brings range
	// requests and conditional responses, which is what makes a video or a large PDF usable at
	// all — and it honours the Content-Type set above rather than sniffing its own, so the
	// allowlist that keeps uploaded content inert is untouched.
	http.ServeContent(
		message.Response().RawWriter(), r, filepath.Base(filePath), info.ModTime(), file)
}

// etagFor is a strong validator built from what a stat can see.
//
// Size and modification time, which is what net/http's own file server uses when it has
// nothing better. Not a content hash: that would mean reading the file to decide whether to
// send it, which is the cost this endpoint just stopped paying.
func etagFor(info os.FileInfo) string {
	return fmt.Sprintf(`"%x-%x"`, info.Size(), info.ModTime().UnixNano())
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
	pattern := thiz.Pattern
	if pattern == "" {
		pattern = DefaultPublicPattern
	}

	return []core.IRouteConfig{
		&router.RouteConfig{
			Path:    pattern,
			Method:  core.GET,
			Handler: thiz.load,
		},
		// HEAD is what a client uses to ask how big something is before fetching it, and
		// ServeContent answers it correctly for free. It used to be a 405.
		&router.RouteConfig{
			Path:    pattern,
			Method:  core.HEAD,
			Handler: thiz.load,
		},
	}
}
