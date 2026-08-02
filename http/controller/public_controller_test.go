package controller

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// servePublic writes content into the served tree and returns the response the controller
// produced for it.
//
// The tree is model.PublicStorage — resource/public — and the URL is /public/<rel>, a single
// `public` segment. The harness used to write under resource/<rel> and request
// /public/<rel>, which is what let the controller be anchored at resource/ without any test
// noticing; and because MultipartFile.PublicPath produced "public/<path>/<name>", the URL
// that actually worked for a stored upload was /public/public/… . Both halves are fixed, and
// TestPublicPathRoundTripsThroughTheRouteExactlyOnce is what pins them together.
func servePublic(t *testing.T, relativePath string, content []byte) *spaRecorder {
	t.Helper()

	full := filepath.Join(model.PublicStorage, filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, content, 0o644))

	message := spaRequestFor("/public/" + relativePath)
	NewPublicController().load(message)
	return message.rec
}

// publicResult reads a response back regardless of whether it was streamed.
func publicResult(rec *spaRecorder) (int, []byte, http.Header) { return rec.result() }

const uploadedHTML = `<html><body><script>alert(document.cookie)</script></body></html>`

const uploadedSVG = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`

// TestUploadedContentIsNeverServedAsAScriptHost is the second half of the stored-XSS path.
// The extension decided the Content-Type — mime.TypeByExtension straight onto the header —
// so a file stored under the upload root as `.html` came back as text/html from the app's
// own origin, and `.svg` as image/svg+xml. Both execute script there.
func TestUploadedContentIsNeverServedAsAScriptHost(t *testing.T) {
	t.Chdir(t.TempDir())

	cases := map[string][]byte{
		"avatars/payload.html":  []byte(uploadedHTML),
		"avatars/payload.HTML":  []byte(uploadedHTML),
		"avatars/payload.htm":   []byte(uploadedHTML),
		"avatars/payload.xhtml": []byte(uploadedHTML),
		"avatars/payload.svg":   []byte(uploadedSVG),
		"avatars/payload.xml":   []byte(uploadedSVG),
	}

	for path, content := range cases {
		t.Run(path, func(t *testing.T) {
			status, _, headers := publicResult(servePublic(t, path, content))

			require.Equal(t, 200, status)
			contentType := headers.Get("Content-Type")
			assert.NotContains(t, contentType, "text/html")
			assert.NotContains(t, contentType, "svg")
			assert.NotContains(t, contentType, "xml")
			assert.Equal(t, "application/octet-stream", contentType,
				"anything outside the render-safe allowlist is inert")
		})
	}
}

// TestEveryPublicResponseIsNosniffAndAttachment: the Content-Type alone is not enough. A
// browser told to sniff will render inert bytes as HTML anyway, and a top-level navigation
// to an uploaded file becomes a document on this origin unless the disposition says
// otherwise. Neither header existed.
func TestEveryPublicResponseIsNosniffAndAttachment(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, path := range []string{
		"avatars/photo.png",
		"avatars/payload.html",
		"docs/report.pdf",
		"styles/app.css",
	} {
		t.Run(path, func(t *testing.T) {
			status, _, headers := publicResult(servePublic(t, path, []byte("some bytes")))

			require.Equal(t, 200, status)
			assert.Equal(t, "nosniff", headers.Get("X-Content-Type-Options"))
			assert.Contains(t, headers.Get("Content-Disposition"), "attachment")
		})
	}
}

// TestRenderSafeTypesAreStillNamed is the regression fence for static assets. Content-
// Disposition does not apply to a subresource load, so a stylesheet or an image served from
// here has to keep its real type or every page using one breaks.
func TestRenderSafeTypesAreStillNamed(t *testing.T) {
	t.Chdir(t.TempDir())

	expected := map[string]string{
		"photo.png":         "image/png",
		"photo.jpg":         "image/jpeg",
		"styles/app.css":    "text/css",
		"scripts/app.js":    "javascript",
		"docs/report.pdf":   "application/pdf",
		"data/export.json":  "application/json",
		"notes/readme.txt":  "text/plain",
		"media/clip.mp4":    "video/mp4",
		"fonts/inter.woff2": "font/woff2",
	}

	for path, wantType := range expected {
		t.Run(path, func(t *testing.T) {
			status, _, headers := publicResult(servePublic(t, path, []byte("some bytes")))

			require.Equal(t, 200, status)
			assert.Contains(t, headers.Get("Content-Type"), wantType)
		})
	}
}

// TestPublicContentIsReturnedByteIdentically: the point of the endpoint.
func TestPublicContentIsReturnedByteIdentically(t *testing.T) {
	t.Chdir(t.TempDir())

	content := make([]byte, 5000)
	for i := range content {
		content[i] = byte(i % 256)
	}

	status, body, _ := publicResult(servePublic(t, "avatars/photo.png", content))

	require.Equal(t, 200, status)
	assert.True(t, bytes.Equal(content, body))
}

// TestAFilenameCannotWriteItsOwnHeader. The disposition carries the stored name, and stored
// names come from user input; mime.FormatMediaType is what keeps a quote or a newline in one
// from ending the header early.
func TestAFilenameCannotWriteItsOwnHeader(t *testing.T) {
	for _, name := range []string{
		"plain.png",
		`quo"te.png`,
		"semi;colon.png",
		"space name.png",
		"unicode-ü.png",
		"bad\r\nSet-Cookie: x=1",
		"nul\x00byte.png",
	} {
		disposition := attachmentDisposition(name)
		assert.NotContains(t, disposition, "\r", "name %q", name)
		assert.NotContains(t, disposition, "\n", "name %q", name)
		assert.Contains(t, disposition, "attachment", "name %q", name)
	}

	assert.Equal(t, "attachment", attachmentDisposition(""))
}

// TestTheContentTypeAllowlistRefusesScriptHosts covers the mapping directly, including the
// inputs that reach it as nonsense.
func TestTheContentTypeAllowlistRefusesScriptHosts(t *testing.T) {
	assert.NotEqual(t, "application/octet-stream", inlineSafeContentType("image/png"),
		"guard against the allowlist accidentally collapsing to octet-stream")

	assert.Equal(t, "application/octet-stream", inlineSafeContentType("text/html; charset=utf-8"))
	assert.Equal(t, "application/octet-stream", inlineSafeContentType("image/svg+xml"))
	assert.Equal(t, "application/octet-stream", inlineSafeContentType(""))
	assert.Equal(t, "application/octet-stream", inlineSafeContentType("not a media type at all"))
}

// TestNothingOutsideTheResourceTreeIsServed. The containment check compares against
// "resource" plus the separator, so a sibling directory whose name merely starts with the
// same letters cannot satisfy it. The prefix and ".." checks earlier in the handler already
// refuse every request that could reach one, which is why this asserts the refusal rather
// than the comparison: the value of the separator is that it stays correct if those checks
// are ever reordered or relaxed.
func TestNothingOutsideTheResourceTreeIsServed(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "resource-backups"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "resource-backups", "secret.txt"),
		[]byte("credentials"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.txt"), []byte("credentials"), 0o644))

	for _, path := range []string{
		"/public/../resource-backups/secret.txt",
		"/public/../../secret.txt",
		"/public/..%2fresource-backups/secret.txt",
		"/resource-backups/secret.txt",
		"/public/",
	} {
		t.Run(path, func(t *testing.T) {
			message := spaRequestFor(path)
			NewPublicController().load(message)

			status, body, _ := publicResult(message.rec)
			assert.NotEqual(t, 200, status, "status %d for %s", status, path)
			assert.NotContains(t, string(body), "credentials")
		})
	}
}

// --- SEC-H10: the served root -------------------------------------------------------------

// TestPublicRoutingCannotReachTheTempUploadRoot.
//
// resource/temp holds uploads mid-request and whatever a crash left behind. It was one
// directory along from the served root, and the served root was their parent.
func TestPublicRoutingCannotReachTheTempUploadRoot(t *testing.T) {
	t.Chdir(t.TempDir())

	require.NoError(t, os.MkdirAll(model.TempStorage, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(model.TempStorage, "leaked.txt"), []byte("somebody's upload"), 0o644))

	message := spaRequestFor("/public/temp/leaked.txt")
	NewPublicController().load(message)

	status, body, _ := publicResult(message.rec)
	assert.NotEqual(t, 200, status)
	assert.NotContains(t, string(body), "somebody's upload")
}

// TestPublicRoutingCannotReachTemplateViewOrI18nSources. Same shape, different siblings: the
// view templates, the internal command templates and the translation files all live under
// resource/ and were all reachable.
func TestPublicRoutingCannotReachTemplateViewOrI18nSources(t *testing.T) {
	t.Chdir(t.TempDir())

	sources := map[string]string{
		"resource/view/auth/login.gohtml":        "{{ .Secret }}",
		"resource/template/command/db_diff.html": "internal template",
		"resource/i18n/en.yaml":                  "greeting: hello",
		"resource/config/database.yml":           "password: hunter2",
	}

	for full, content := range sources {
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	for full, content := range sources {
		url := "/public/" + strings.TrimPrefix(full, "resource/")
		t.Run(url, func(t *testing.T) {
			message := spaRequestFor(url)
			NewPublicController().load(message)

			status, body, _ := publicResult(message.rec)
			assert.NotEqual(t, 200, status)
			assert.NotContains(t, string(body), content)
		})
	}
}

// TestPublicPathRoundTripsThroughTheRouteExactlyOnce is the acceptance criterion, end to end:
// the URL a stored upload reports has to be the URL that serves it.
//
// It did not. PublicPath returned "public/<path>/<name>" and the controller mapped /public/X
// onto resource/X, so the value the framework handed to a template was a 404 and the URL that
// worked had `public` in it twice.
func TestPublicPathRoundTripsThroughTheRouteExactlyOnce(t *testing.T) {
	t.Chdir(t.TempDir())

	content := []byte("\x89PNG\r\n\x1a\nthe avatar bytes")
	file, err := model.NewMultipartFile("avatar.png", bytes.NewReader(content))
	require.NoError(t, err)
	defer file.Close()

	_, err = file.Write("avatars", bytes.NewReader(content))
	require.NoError(t, err)

	url := file.PublicPath()
	require.Equal(t, 1, strings.Count(url, "public/"), "url %q", url)

	message := spaRequestFor(url)
	NewPublicController().load(message)

	status, body, _ := publicResult(message.rec)
	require.Equal(t, 200, status, "PublicPath() must be usable as the request target: %s", url)
	assert.True(t, bytes.Equal(content, body))
}

// TestTheDoublePublicUrlNoLongerResolves pins the break.
func TestTheDoublePublicUrlNoLongerResolves(t *testing.T) {
	t.Chdir(t.TempDir())
	servePublic(t, "avatars/x.png", []byte("bytes"))

	message := spaRequestFor("/public/public/avatars/x.png")
	NewPublicController().load(message)

	status, _, _ := publicResult(message.rec)
	assert.NotEqual(t, 200, status)
}

// TestASymlinkBelowThePublicRootCannotEscapeIt. Containment was purely lexical —
// filepath.Abs and a prefix compare — and EvalSymlinks appears nowhere in this repository, so
// a link planted under the served root pointed wherever it liked.
func TestASymlinkBelowThePublicRootCannotEscapeIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need elevation on Windows")
	}

	root := t.TempDir()
	t.Chdir(root)

	require.NoError(t, os.WriteFile(filepath.Join(root, "secret.txt"), []byte("credentials"), 0o644))
	require.NoError(t, os.MkdirAll(model.PublicStorage, 0o755))
	require.NoError(t, os.Symlink(
		filepath.Join(root, "secret.txt"), filepath.Join(model.PublicStorage, "escape.txt")))

	message := spaRequestFor("/public/escape.txt")
	NewPublicController().load(message)

	status, body, _ := publicResult(message.rec)
	assert.NotEqual(t, 200, status)
	assert.NotContains(t, string(body), "credentials")
}

// TestARangeRequestIsAnsweredFromTheOpenFile is the observable proof the response is streamed
// rather than read into memory: os.ReadFile plus Bytes could not answer a range at all.
func TestARangeRequestIsAnsweredFromTheOpenFile(t *testing.T) {
	t.Chdir(t.TempDir())

	content := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	full := filepath.Join(model.PublicStorage, "data", "export.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, content, 0o644))

	message := spaRequestFor("/public/data/export.json")
	message.req.raw.Header.Set("Range", "bytes=10-19")
	NewPublicController().load(message)

	status, body, headers := publicResult(message.rec)
	require.Equal(t, 206, status)
	assert.Equal(t, content[10:20], body)
	assert.Contains(t, headers.Get("Content-Range"), "10-19")
}

// TestAConditionalRequestIsAnsweredWith304.
func TestAConditionalRequestIsAnsweredWith304(t *testing.T) {
	t.Chdir(t.TempDir())

	_, _, headers := publicResult(servePublic(t, "data/export.json", []byte("{}")))
	etag := headers.Get("ETag")
	require.NotEmpty(t, etag)

	message := spaRequestFor("/public/data/export.json")
	message.req.raw.Header.Set("If-None-Match", etag)
	NewPublicController().load(message)

	status, body, _ := publicResult(message.rec)
	assert.Equal(t, 304, status)
	assert.Empty(t, body)
}
