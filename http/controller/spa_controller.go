package controller

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/http/router"
)

// SpaController serves a built single-page app: real files where they exist, and the
// SPA's entry document for everything else so client-side routing works.
//
// PublicController serves /public/* and answers 400 for anything else, so a deep link —
// a path the server has no route for, which the client router would have handled — could
// not work at all. Reloading /settings/profile in a Gorgany-hosted SPA returned 400.
//
// Mount it last. It claims a catch-all pattern, so anything registered after it is
// unreachable; the duplicate-route check will tell you if you get that wrong.
//
//	spa := controller.NewSpaController("web/dist")
//	routeProvider.AddController(spa)
type SpaController struct {
	// Root is the directory holding the built app. Required.
	Root string

	// Pattern is the route to mount on. Empty means "/*".
	Pattern string

	// IndexFile is the entry document served for an unmatched path. Empty means
	// "index.html".
	IndexFile string

	// ExcludedPrefixes are paths the SPA must never answer for. A request matching one
	// gets a 404 rather than index.html.
	//
	// Empty means DefaultSpaExclusions. This matters more than it looks: without it, a
	// typo'd API call — GET /api/v1/widget instead of /widgets — returns 200 and an
	// HTML document, and the client's JSON parse fails somewhere far away from the
	// cause. An API 404 must stay a 404.
	ExcludedPrefixes []string

	// ImmutablePattern matches asset filenames that carry a content hash and can
	// therefore be cached forever. Nil means DefaultImmutableAssetPattern.
	ImmutablePattern *regexp.Regexp
}

// DefaultSpaExclusions are the prefixes that never fall back to index.html.
var DefaultSpaExclusions = []string{"/api/", "/api", "/public/", "/csrf"}

// DefaultImmutableAssetPattern matches a filename with an embedded content hash, which is
// what every modern bundler emits: app.4f3a91c2.js, main-a1b2c3d4e5.css,
// chunk.9f8e7d6c.mjs.
//
// Only hashed names get immutable caching. A hash-free /assets/logo.png would then be
// pinned in every visitor's browser for a year, and replacing it would be impossible.
var DefaultImmutableAssetPattern = regexp.MustCompile(`[.\-_][0-9a-fA-F]{8,}\.[a-zA-Z0-9]+$`)

// Cache-Control values. The entry document must never be cached: it is the one file that
// names the current asset hashes, so a stale copy points at assets that no longer exist.
const (
	SpaIndexCacheControl     = "no-store, must-revalidate"
	SpaImmutableCacheControl = "public, max-age=31536000, immutable"
	SpaAssetCacheControl     = "public, max-age=300"
)

func NewSpaController(root string) *SpaController {
	return &SpaController{Root: root}
}

var _ core.IController = (*SpaController)(nil)

func (thiz *SpaController) pattern() string {
	if thiz.Pattern != "" {
		return thiz.Pattern
	}
	return "/*"
}

func (thiz *SpaController) indexFile() string {
	if thiz.IndexFile != "" {
		return thiz.IndexFile
	}
	return "index.html"
}

func (thiz *SpaController) exclusions() []string {
	if thiz.ExcludedPrefixes != nil {
		return thiz.ExcludedPrefixes
	}
	return DefaultSpaExclusions
}

func (thiz *SpaController) immutablePattern() *regexp.Regexp {
	if thiz.ImmutablePattern != nil {
		return thiz.ImmutablePattern
	}
	return DefaultImmutableAssetPattern
}

// Serve answers one request.
func (thiz *SpaController) Serve(message core.HttpMessage) {
	if thiz.Root == "" {
		message.Response().Text("SPA controller has no Root configured", http.StatusInternalServerError)
		return
	}

	req := message.Request().RawRequest()
	if req == nil || req.URL == nil {
		message.Response().Text("Bad request", http.StatusBadRequest)
		return
	}

	requestPath := req.URL.Path

	if thiz.isExcluded(requestPath) {
		// Deliberately not index.html. See ExcludedPrefixes.
		message.Response().Text("Not found", http.StatusNotFound)
		return
	}

	resolved, ok := thiz.resolve(requestPath)
	if !ok {
		message.Response().Text("Invalid path", http.StatusBadRequest)
		return
	}

	if content, err := os.ReadFile(resolved); err == nil {
		thiz.writeAsset(message, resolved, content)
		return
	} else if !os.IsNotExist(err) {
		message.Response().Text("Internal server error", http.StatusInternalServerError)
		return
	}

	thiz.writeIndex(message)
}

// isExcluded reports whether this path must never be answered by the SPA.
func (thiz *SpaController) isExcluded(requestPath string) bool {
	for _, prefix := range thiz.exclusions() {
		if strings.HasSuffix(prefix, "/") {
			if strings.HasPrefix(requestPath, prefix) {
				return true
			}
			continue
		}
		// A prefix without a trailing slash matches the exact path or a path with a
		// slash after it, so "/api" excludes "/api" and "/api/v1" but not "/apiary".
		if requestPath == prefix || strings.HasPrefix(requestPath, prefix+"/") {
			return true
		}
	}
	return false
}

// resolve maps a request path to a file under Root, refusing anything that escapes it.
//
// The traversal guard is the same shape as PublicController's, plus the containment check
// that one only approximates: filepath.Clean on a path already rooted at "/" collapses
// "..", but a request can reach the handler with an unrooted or encoded path, so the
// absolute-prefix check is what actually enforces the boundary.
func (thiz *SpaController) resolve(requestPath string) (string, bool) {
	cleaned := filepath.Clean("/" + strings.TrimPrefix(requestPath, "/"))
	if strings.Contains(cleaned, "..") {
		return "", false
	}

	candidate := filepath.Join(thiz.Root, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))

	absCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", false
	}
	absRoot, err := filepath.Abs(thiz.Root)
	if err != nil {
		return "", false
	}

	// Comparing with the separator appended, because a plain prefix test would accept
	// "/srv/web-dist-backup" as being inside "/srv/web-dist".
	if absCandidate != absRoot && !strings.HasPrefix(absCandidate, absRoot+string(filepath.Separator)) {
		return "", false
	}

	// A directory is not a file to serve; fall through to the index.
	if info, err := os.Stat(absCandidate); err == nil && info.IsDir() {
		return "", true
	}

	return absCandidate, true
}

// writeAsset sends a real file with a content type and a cache policy.
func (thiz *SpaController) writeAsset(message core.HttpMessage, path string, content []byte) {
	message.Response().SetHeader("Content-Type", contentTypeFor(path))

	base := filepath.Base(path)
	switch {
	case base == thiz.indexFile():
		// Reached when the client asks for /index.html by name. Same policy as the
		// fallback: it names the current asset hashes.
		message.Response().SetHeader("Cache-Control", SpaIndexCacheControl)
	case thiz.immutablePattern().MatchString(base):
		message.Response().SetHeader("Cache-Control", SpaImmutableCacheControl)
	default:
		message.Response().SetHeader("Cache-Control", SpaAssetCacheControl)
	}

	message.Response().Bytes(content, http.StatusOK)
}

// writeIndex sends the entry document, which is what makes client-side routing work.
func (thiz *SpaController) writeIndex(message core.HttpMessage) {
	indexPath := filepath.Join(thiz.Root, thiz.indexFile())

	content, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Say which file is missing. "404" here means the app was never built, or
			// Root points at the wrong directory, and that is worth naming.
			message.Response().Text(
				fmt.Sprintf("SPA entry document not found at %s; is the app built?", indexPath),
				http.StatusNotFound)
			return
		}
		message.Response().Text("Internal server error", http.StatusInternalServerError)
		return
	}

	message.Response().SetHeader("Content-Type", "text/html; charset=utf-8")
	message.Response().SetHeader("Cache-Control", SpaIndexCacheControl)
	message.Response().Bytes(content, http.StatusOK)
}

// contentTypeFor resolves a content type from the extension.
func contentTypeFor(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return "application/octet-stream"
	}

	if kind := mime.TypeByExtension(ext); kind != "" {
		return kind
	}

	// mime's table misses a few things a bundler emits routinely, and serving an ES
	// module as application/octet-stream makes the browser refuse to execute it.
	switch strings.ToLower(ext) {
	case ".mjs", ".cjs":
		return "text/javascript; charset=utf-8"
	case ".webmanifest":
		return "application/manifest+json"
	case ".map":
		return "application/json"
	}

	return "application/octet-stream"
}

func (thiz *SpaController) GetRoutes() []core.IRouteConfig {
	return []core.IRouteConfig{
		&router.RouteConfig{
			Name:    "gorgany.spa",
			Path:    thiz.pattern(),
			Method:  core.GET,
			Handler: thiz.Serve,
		},
	}
}
