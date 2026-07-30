package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C3: PublicController serves /public/* and answers 400 for anything else, so a deep
// link — a path the server has no route for, which the client router would have handled —
// could not work. Reloading /settings/profile in a Gorgany-hosted SPA returned 400.

// ---------------------------------------------------------------- test harness

type spaRecorder struct {
	status  int
	body    []byte
	text    string
	json    any
	headers http.Header
}

type spaResponse struct {
	core.IResponseScope
	rec *spaRecorder
}

func (r *spaResponse) Bytes(b []byte, code int) { r.rec.status, r.rec.body = code, b }
func (r *spaResponse) Text(s string, code int)  { r.rec.status, r.rec.text = code, s }
func (r *spaResponse) JSON(v any, code int)     { r.rec.status, r.rec.json = code, v }
func (r *spaResponse) SetHeader(key, value string) {
	if r.rec.headers == nil {
		r.rec.headers = http.Header{}
	}
	r.rec.headers.Set(key, value)
}

type spaRequest struct {
	core.IRequestScope
	raw *http.Request
}

func (r *spaRequest) RawRequest() *http.Request { return r.raw }

// PathParam and Header exist because http.WantsJSON consults both. The harness embeds a nil
// core.IRequestScope, so an unimplemented method on the negotiation path is a nil panic
// rather than a compile error — hence implementing them here rather than discovering it.
func (r *spaRequest) PathParam(string) string { return "" }
func (r *spaRequest) Header() http.Header     { return r.raw.Header }

type spaMessage struct {
	core.HttpMessage
	req *spaRequest
	res *spaResponse
	rec *spaRecorder
}

func (m *spaMessage) Request() core.IRequestScope   { return m.req }
func (m *spaMessage) Response() core.IResponseScope { return m.res }

func spaRequestFor(path string) *spaMessage {
	rec := &spaRecorder{}
	return &spaMessage{
		req: &spaRequest{raw: httptest.NewRequest(http.MethodGet, path, nil)},
		res: &spaResponse{rec: rec},
		rec: rec,
	}
}

// spaAPIRequestFor is the same request an API client makes: it asks for JSON.
func spaAPIRequestFor(path string) *spaMessage {
	message := spaRequestFor(path)
	message.req.raw.Header.Set("Accept", core.ApplicationJson.String())
	return message
}

// builtApp writes a plausible bundler output tree and returns its root.
func builtApp(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"index.html":                "<!doctype html><div id=app></div>",
		"assets/app.4f3a91c2.js":    "console.log('app')",
		"assets/main.a1b2c3d4.css":  "body{}",
		"assets/logo.png":           "not-really-a-png",
		"assets/chunk.9f8e7d6c.mjs": "export default 1",
		"favicon.ico":               "icon",
		"manifest.webmanifest":      `{"name":"app"}`,
	}

	for name, content := range files {
		full := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
	}
	return root
}

// -------------------------------------------------------------------- fallback

// TestADeepLinkGetsTheIndexDocument is the headline: this is what returned 400.
func TestADeepLinkGetsTheIndexDocument(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	for _, path := range []string{
		"/settings/profile",
		"/widgets/17/edit",
		"/",
		"/anything/at/all",
	} {
		t.Run(path, func(t *testing.T) {
			message := spaRequestFor(path)
			controller.Serve(message)

			require.Equal(t, http.StatusOK, message.rec.status)
			assert.Contains(t, string(message.rec.body), `id=app`)
			assert.Equal(t, "text/html; charset=utf-8", message.rec.headers.Get("Content-Type"))
		})
	}
}

// TestARealFileIsServedNotTheIndex: the fallback must not shadow actual assets.
func TestARealFileIsServedNotTheIndex(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	message := spaRequestFor("/assets/app.4f3a91c2.js")
	controller.Serve(message)

	require.Equal(t, http.StatusOK, message.rec.status)
	assert.Equal(t, "console.log('app')", string(message.rec.body))
	assert.Contains(t, message.rec.headers.Get("Content-Type"), "javascript")
}

// TestADirectoryFallsBackToTheIndex rather than serving a listing or erroring.
func TestADirectoryFallsBackToTheIndex(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	message := spaRequestFor("/assets")
	controller.Serve(message)

	require.Equal(t, http.StatusOK, message.rec.status)
	assert.Contains(t, string(message.rec.body), `id=app`)
}

// ------------------------------------------------------------------- exclusions

// TestApiPathsDoNotFallBackToTheIndex. Without this, a typo'd API call returns 200 and an
// HTML document, and the client's JSON parse fails far from the cause.
func TestApiPathsDoNotFallBackToTheIndex(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	for _, path := range []string{"/api", "/api/v1/widget", "/public/missing.css", "/csrf"} {
		t.Run(path, func(t *testing.T) {
			message := spaRequestFor(path)
			controller.Serve(message)

			assert.Equal(t, http.StatusNotFound, message.rec.status)
			assert.Empty(t, message.rec.body, "an API 404 must stay a 404")
		})
	}
}

// TestAnExclusionPrefixDoesNotOverreach: "/api" must not exclude "/apiary".
func TestAnExclusionPrefixDoesNotOverreach(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	message := spaRequestFor("/apiary/bees")
	controller.Serve(message)

	require.Equal(t, http.StatusOK, message.rec.status)
	assert.Contains(t, string(message.rec.body), `id=app`)
}

// TestExclusionsAreConfigurable, for an app whose API lives elsewhere.
func TestExclusionsAreConfigurable(t *testing.T) {
	controller := NewSpaController(builtApp(t))
	controller.ExcludedPrefixes = []string{"/graphql"}

	excluded := spaRequestFor("/graphql")
	controller.Serve(excluded)
	assert.Equal(t, http.StatusNotFound, excluded.rec.status)

	// And the defaults no longer apply, since the app replaced them.
	nowIncluded := spaRequestFor("/api/v1/widgets")
	controller.Serve(nowIncluded)
	assert.Equal(t, http.StatusOK, nowIncluded.rec.status)
}

// TestAnEmptyExclusionSliceIsNotTheDefault: an app that deliberately wants no exclusions
// must be able to say so, distinctly from not configuring them.
func TestAnEmptyExclusionSliceIsNotTheDefault(t *testing.T) {
	controller := NewSpaController(builtApp(t))
	controller.ExcludedPrefixes = []string{}

	message := spaRequestFor("/api/v1/widgets")
	controller.Serve(message)
	assert.Equal(t, http.StatusOK, message.rec.status)
}

// ----------------------------------------------------------------------- caching

// TestTheIndexIsNeverCached. It is the one file naming the current asset hashes, so a
// stale copy points at assets that no longer exist.
func TestTheIndexIsNeverCached(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	for _, path := range []string{"/settings/profile", "/index.html"} {
		t.Run(path, func(t *testing.T) {
			message := spaRequestFor(path)
			controller.Serve(message)

			require.Equal(t, http.StatusOK, message.rec.status)
			assert.Equal(t, SpaIndexCacheControl, message.rec.headers.Get("Cache-Control"))
			assert.Contains(t, message.rec.headers.Get("Cache-Control"), "no-store")
		})
	}
}

// TestHashedAssetsAreImmutable, which is the whole point of a content hash.
func TestHashedAssetsAreImmutable(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	for _, path := range []string{
		"/assets/app.4f3a91c2.js",
		"/assets/main.a1b2c3d4.css",
		"/assets/chunk.9f8e7d6c.mjs",
	} {
		t.Run(path, func(t *testing.T) {
			message := spaRequestFor(path)
			controller.Serve(message)

			require.Equal(t, http.StatusOK, message.rec.status)
			assert.Equal(t, SpaImmutableCacheControl, message.rec.headers.Get("Cache-Control"))
		})
	}
}

// TestUnhashedAssetsAreNotImmutable. Pinning a hash-free /assets/logo.png in every
// visitor's browser for a year would make replacing it impossible.
func TestUnhashedAssetsAreNotImmutable(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	for _, path := range []string{"/assets/logo.png", "/favicon.ico"} {
		t.Run(path, func(t *testing.T) {
			message := spaRequestFor(path)
			controller.Serve(message)

			require.Equal(t, http.StatusOK, message.rec.status)
			assert.Equal(t, SpaAssetCacheControl, message.rec.headers.Get("Cache-Control"))
			assert.NotContains(t, message.rec.headers.Get("Cache-Control"), "immutable")
		})
	}
}

// TestTheImmutablePatternIsConfigurable, for a bundler naming things differently.
func TestTheImmutablePatternIsConfigurable(t *testing.T) {
	controller := NewSpaController(builtApp(t))
	controller.ImmutablePattern = regexp.MustCompile(`^logo\.png$`)

	logo := spaRequestFor("/assets/logo.png")
	controller.Serve(logo)
	assert.Equal(t, SpaImmutableCacheControl, logo.rec.headers.Get("Cache-Control"))

	hashed := spaRequestFor("/assets/app.4f3a91c2.js")
	controller.Serve(hashed)
	assert.Equal(t, SpaAssetCacheControl, hashed.rec.headers.Get("Cache-Control"))
}

// TestTheDefaultImmutablePatternOnRealBundlerNames pins the regex directly, since a
// mistake in it silently changes every visitor's caching.
func TestTheDefaultImmutablePatternOnRealBundlerNames(t *testing.T) {
	hashed := []string{
		"app.4f3a91c2.js",
		"main-a1b2c3d4e5.css",
		"chunk.9f8e7d6c.mjs",
		"vendor_0123456789abcdef.js",
		"index.abcdef01.html",
	}
	plain := []string{
		"app.js",
		"logo.png",
		"index.html",
		"style.min.css",
		"favicon.ico",
		"app.1234.js", // only four hex digits: too short to be a content hash
	}

	for _, name := range hashed {
		assert.Truef(t, DefaultImmutableAssetPattern.MatchString(name), "%s should be immutable", name)
	}
	for _, name := range plain {
		assert.Falsef(t, DefaultImmutableAssetPattern.MatchString(name), "%s should not be immutable", name)
	}
}

// ------------------------------------------------------------------- traversal

// TestPathTraversalIsRefused. The controller reads from disk on a caller-supplied path,
// so this is the one thing it must get right.
func TestPathTraversalIsRefused(t *testing.T) {
	root := builtApp(t)

	// A file the SPA must never be able to reach.
	outside := filepath.Join(filepath.Dir(root), "secret.txt")
	require.NoError(t, os.WriteFile(outside, []byte("SECRET"), 0o600))

	controller := NewSpaController(root)

	for _, path := range []string{
		"/../secret.txt",
		"/../../secret.txt",
		"/assets/../../secret.txt",
		"/./../secret.txt",
	} {
		t.Run(path, func(t *testing.T) {
			message := spaRequestFor(path)
			require.NotPanics(t, func() { controller.Serve(message) })

			assert.NotContains(t, string(message.rec.body), "SECRET")
			assert.NotContains(t, message.rec.text, "SECRET")
		})
	}
}

// TestASiblingDirectoryIsNotInsideTheRoot. A plain string-prefix containment check would
// accept /srv/web-dist-backup as being inside /srv/web-dist.
func TestASiblingDirectoryIsNotInsideTheRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "dist")
	sibling := filepath.Join(base, "dist-backup")

	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.MkdirAll(sibling, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.html"), []byte("<html>"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(sibling, "secret.txt"), []byte("SECRET"), 0o600))

	controller := NewSpaController(root)

	message := spaRequestFor("/../dist-backup/secret.txt")
	controller.Serve(message)

	assert.NotContains(t, string(message.rec.body), "SECRET")
}

// ------------------------------------------------------------- misconfiguration

// TestAMissingIndexSaysWhatIsWrong. "404" here means the app was never built or Root
// points at the wrong directory, and that is worth naming.
func TestAMissingIndexSaysWhatIsWrong(t *testing.T) {
	controller := NewSpaController(t.TempDir())

	message := spaRequestFor("/settings")
	controller.Serve(message)

	assert.Equal(t, http.StatusNotFound, message.rec.status)
	assert.Contains(t, message.rec.text, "index.html")
	assert.Contains(t, message.rec.text, "is the app built?")
}

// TestAnUnconfiguredRootIsAnInternalError rather than serving from the process's working
// directory, which is how a whole source tree gets published.
func TestAnUnconfiguredRootIsAnInternalError(t *testing.T) {
	controller := &SpaController{}

	message := spaRequestFor("/settings")
	controller.Serve(message)

	assert.Equal(t, http.StatusInternalServerError, message.rec.status)
	assert.Contains(t, message.rec.text, "Root")
}

// ------------------------------------------------------------------ the route

func TestTheDefaultRouteIsACatchAllGet(t *testing.T) {
	routes := NewSpaController("web/dist").GetRoutes()
	require.Len(t, routes, 1)

	assert.Equal(t, "/*", routes[0].GetPath())
	assert.Equal(t, core.GET, routes[0].GetMethod())
	assert.Equal(t, "gorgany.spa", routes[0].GetName())
}

func TestThePatternIsConfigurable(t *testing.T) {
	controller := NewSpaController("web/dist")
	controller.Pattern = "/app/*"

	routes := controller.GetRoutes()
	require.Len(t, routes, 1)
	assert.Equal(t, "/app/*", routes[0].GetPath())
}

// TestTheIndexFileIsConfigurable, for a build that emits something other than index.html.
func TestTheIndexFileIsConfigurable(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.html"), []byte("<app>"), 0o600))

	controller := NewSpaController(root)
	controller.IndexFile = "app.html"

	message := spaRequestFor("/settings")
	controller.Serve(message)

	require.Equal(t, http.StatusOK, message.rec.status)
	assert.Equal(t, "<app>", string(message.rec.body))
}

// TestBundlerContentTypes: mime's table misses .mjs and .webmanifest, and serving an ES
// module as application/octet-stream makes the browser refuse to execute it.
func TestBundlerContentTypes(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	module := spaRequestFor("/assets/chunk.9f8e7d6c.mjs")
	controller.Serve(module)
	assert.Contains(t, module.rec.headers.Get("Content-Type"), "javascript")

	manifest := spaRequestFor("/manifest.webmanifest")
	controller.Serve(manifest)
	assert.Contains(t, manifest.rec.headers.Get("Content-Type"), "manifest+json")
}

// ------------------------------------------------------- H1: the shadowed 404

// H1. SpaController registers a `/*` catch-all, and chi matches a catch-all in preference to
// falling through to NotFound — so mounting the SPA replaces the router's negotiated 404
// (C2) for every GET the app has no route for.
//
// The observed symptom in flow8-be: `GET /api/v1/widgetz` with `Accept: application/json`
// answered **200 with an HTML document**. A client checking the status code saw success and
// then failed parsing JSON, which is the worst of both. `DELETE /api/v1/widgetz` still
// answered the 405 envelope, because the SPA registers GET only — so the shape of a
// not-found depended on the verb.
//
// The fix is not "exclude more paths": an excluded path answered text/plain where the router
// answers the envelope, so excluding merely traded one divergence for another. Both of the
// SPA's refusals now go through the same http.WriteNegotiatedError the router uses, which is
// the property worth pinning — an app's 404 must not depend on whether a SPA is mounted.

func TestAnExcludedApiPathAnswersTheNegotiatedEnvelope(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	message := spaAPIRequestFor("/api/v1/widgetz")
	controller.Serve(message)

	// Not 200, and not HTML: this is the assertion the defect failed.
	assert.Equal(t, http.StatusNotFound, message.rec.status)
	assert.Empty(t, message.rec.body, "an API client must not receive index.html")
	assert.Empty(t, message.rec.text, "an API client must not receive text/plain")

	require.NotNil(t, message.rec.json, "expected the standard envelope")
	assert.Contains(t, envelopeStatusCode(t, message.rec.json), "NOT_FOUND")
}

// TestAnExcludedPathStillAnswersTextForABrowser — negotiation cuts both ways, and a browser
// following a stale link should not be handed a JSON body.
//
// The path is /csrf rather than an /api one on purpose: WantsJSON treats an /api/ prefix as
// an API client whatever the Accept header says (T3.4), so every /api path here would be
// JSON and the test would prove nothing about negotiation. /csrf is the excluded prefix that
// is not under /api.
func TestAnExcludedPathStillAnswersTextForABrowser(t *testing.T) {
	controller := NewSpaController(builtApp(t))

	message := spaRequestFor("/csrf")
	// A browser's Accept lists text/html and then */*; matching the wildcard would turn
	// every one of these into JSON, which is why WantsJSON looks for JSON specifically.
	message.req.raw.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	controller.Serve(message)

	assert.Equal(t, http.StatusNotFound, message.rec.status)
	assert.Nil(t, message.rec.json)
	assert.NotEmpty(t, message.rec.text)
}

// TestTheSpaDoesNotShadowTheRoutersAnswer is the invariant behind H1, stated directly: for a
// path the SPA refuses, its response must be byte-identical to the one the router produces
// with no SPA mounted. Asserting the envelope's shape in the test above would drift the
// moment the router's changed; comparing against the router itself cannot.
func TestTheSpaDoesNotShadowTheRoutersAnswer(t *testing.T) {
	// Both an /api path (always JSON) and /csrf under a browser Accept (text), so the
	// comparison covers each branch of the negotiation rather than only the one.
	for _, probe := range []struct{ path, accept string }{
		{"/api/v1/widgetz", core.ApplicationJson.String()},
		{"/csrf", "text/html,application/xhtml+xml,*/*;q=0.8"},
	} {
		t.Run(probe.path, func(t *testing.T) {
			viaSpa := spaRequestFor(probe.path)
			viaSpa.req.raw.Header.Set("Accept", probe.accept)
			NewSpaController(builtApp(t)).Serve(viaSpa)

			viaRouter := spaRequestFor(probe.path)
			viaRouter.req.raw.Header.Set("Accept", probe.accept)
			grghttp.WriteNegotiatedError(viaRouter, core.NotFoundHttpStatus,
				"No route matches this request")

			assert.Equal(t, viaRouter.rec.status, viaSpa.rec.status)
			assert.Equal(t, viaRouter.rec.text, viaSpa.rec.text)
			assert.Equal(t, viaRouter.rec.json, viaSpa.rec.json)
		})
	}
}

// TestAMissingIndexIsNegotiatedToo. The misconfiguration paths matter less, but an API client
// that gets text/plain from one branch and an envelope from another has to handle both.
func TestAMissingIndexIsNegotiatedToo(t *testing.T) {
	controller := NewSpaController(t.TempDir())

	message := spaAPIRequestFor("/settings")
	controller.Serve(message)

	assert.Equal(t, http.StatusNotFound, message.rec.status)
	require.NotNil(t, message.rec.json)
	assert.Contains(t, envelopeError(t, message.rec.json), "is the app built?")
}

// envelopeStatusCode and envelopeError read the envelope back through encoding/json, so the
// assertions above are about the wire shape a client sees rather than dto's field names.

func envelopeStatusCode(t *testing.T, envelope any) string {
	t.Helper()
	return envelopeField(t, envelope, "status_code")
}

func envelopeError(t *testing.T, envelope any) string {
	t.Helper()
	return envelopeField(t, envelope, "errors")
}

func envelopeField(t *testing.T, envelope any, field string) string {
	t.Helper()

	encoded, err := json.Marshal(envelope)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Contains(t, decoded, field, "envelope was %s", encoded)

	return fmt.Sprint(decoded[field])
}
