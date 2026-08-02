package controller

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// servePublic writes content into the served tree and returns the response the controller
// produced for it.
func servePublic(t *testing.T, relativePath string, content []byte) *spaRecorder {
	t.Helper()

	full := filepath.Join("resource", filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, content, 0o644))

	message := spaRequestFor("/public/" + relativePath)
	NewPublicController().load(message)
	return message.rec
}

const uploadedHTML = `<html><body><script>alert(document.cookie)</script></body></html>`

const uploadedSVG = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`

// TestUploadedContentIsNeverServedAsAScriptHost is the second half of the stored-XSS path.
// The extension decided the Content-Type — mime.TypeByExtension straight onto the header —
// so a file stored under the upload root as `.html` came back as text/html from the app's
// own origin, and `.svg` as image/svg+xml. Both execute script there.
func TestUploadedContentIsNeverServedAsAScriptHost(t *testing.T) {
	t.Chdir(t.TempDir())

	cases := map[string][]byte{
		"public/avatars/payload.html":  []byte(uploadedHTML),
		"public/avatars/payload.HTML":  []byte(uploadedHTML),
		"public/avatars/payload.htm":   []byte(uploadedHTML),
		"public/avatars/payload.xhtml": []byte(uploadedHTML),
		"public/avatars/payload.svg":   []byte(uploadedSVG),
		"public/avatars/payload.xml":   []byte(uploadedSVG),
	}

	for path, content := range cases {
		t.Run(path, func(t *testing.T) {
			rec := servePublic(t, path, content)

			require.Equal(t, 200, rec.status)
			contentType := rec.headers.Get("Content-Type")
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
		"public/avatars/photo.png",
		"public/avatars/payload.html",
		"public/docs/report.pdf",
		"styles/app.css",
	} {
		t.Run(path, func(t *testing.T) {
			rec := servePublic(t, path, []byte("some bytes"))

			require.Equal(t, 200, rec.status)
			assert.Equal(t, "nosniff", rec.headers.Get("X-Content-Type-Options"))
			assert.Contains(t, rec.headers.Get("Content-Disposition"), "attachment")
		})
	}
}

// TestRenderSafeTypesAreStillNamed is the regression fence for static assets. Content-
// Disposition does not apply to a subresource load, so a stylesheet or an image served from
// here has to keep its real type or every page using one breaks.
func TestRenderSafeTypesAreStillNamed(t *testing.T) {
	t.Chdir(t.TempDir())

	expected := map[string]string{
		"public/photo.png":  "image/png",
		"public/photo.jpg":  "image/jpeg",
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
			rec := servePublic(t, path, []byte("some bytes"))

			require.Equal(t, 200, rec.status)
			assert.Contains(t, rec.headers.Get("Content-Type"), wantType)
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

	rec := servePublic(t, "public/avatars/photo.png", content)

	require.Equal(t, 200, rec.status)
	assert.True(t, bytes.Equal(content, rec.body))
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

			assert.NotEqual(t, 200, message.rec.status, "status %d for %s", message.rec.status, path)
			assert.NotContains(t, string(message.rec.body), "credentials")
		})
	}
}
