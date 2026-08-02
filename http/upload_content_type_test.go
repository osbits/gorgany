package http

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const scriptedHTML = `<html><body><script>fetch('/csrf')</script></body></html>`

// TestFormFileIgnoresTheClientsDeclaredType is the exploit as posted: the part is named
// payload.html and declares Content-Type: image/png, which is a header the client writes.
// The allowlist read exactly that header, so the part passed and was stored as
// `…-payload.html`.
func TestFormFileIgnoresTheClientsDeclaredType(t *testing.T) {
	t.Chdir(t.TempDir())

	req := multipartRequestWithDeclaredType(t, "avatar", "payload.html", "image/png", []byte(scriptedHTML))
	message, _ := newTestMessage(t, req)

	file, err := message.Request().FormFile("avatar")
	if err == nil {
		t.Cleanup(func() { _ = file.Close() })
		assert.NotContains(t, strings.ToLower(file.GetName()), ".htm")
	}
	require.Error(t, err, "html content must be refused whatever the part declares")
}

// TestFormFileHoldsUploadsToTheSniffedType covers the allowedMimes knob, which is still
// honoured — it is just applied to what the bytes are rather than to what the client said
// they were.
func TestFormFileHoldsUploadsToTheSniffedType(t *testing.T) {
	t.Chdir(t.TempDir())
	withViper(t, map[string]any{"http.security.file.allowedMimes": []string{"image/png"}})

	t.Run("a gif declared as png is refused", func(t *testing.T) {
		req := multipartRequestWithDeclaredType(t, "avatar", "anim.png", "image/png",
			[]byte("GIF89a"+strings.Repeat("x", 600)))
		message, _ := newTestMessage(t, req)

		_, err := message.Request().FormFile("avatar")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "image/gif")
	})

	t.Run("a real png is accepted and stored as png", func(t *testing.T) {
		req := multipartRequestWithDeclaredType(t, "avatar", "photo.jpeg", "text/plain", pngContent(2048))
		message, _ := newTestMessage(t, req)

		file, err := message.Request().FormFile("avatar")
		require.NoError(t, err)
		t.Cleanup(func() { _ = file.Close() })
		assert.Equal(t, ".png", filepath.Ext(file.GetName()))
	})
}

// TestARefusedUploadLeavesNothingBehind: the temp copy is written before the allowlist is
// consulted, so failing to delete it would turn every rejected upload into disk growth.
func TestARefusedUploadLeavesNothingBehind(t *testing.T) {
	t.Chdir(t.TempDir())
	withViper(t, map[string]any{"http.security.file.allowedMimes": []string{"image/gif"}})

	req := multipartRequestWithDeclaredType(t, "avatar", "photo.png", "image/gif", pngContent(2048))
	message, _ := newTestMessage(t, req)

	_, err := message.Request().FormFile("avatar")
	require.Error(t, err)
	assert.Empty(t, frameworkTempFiles(t))
}

// TestTheDtoBindingPathRejectsADisallowedType. This is the documented way to receive an
// upload — a core.IFile field on the DTO — and it applied no content check whatsoever:
// isAllowedMime had two callers, both on the hand-rolled FormFile/GetFiles path.
func TestTheDtoBindingPathRejectsADisallowedType(t *testing.T) {
	t.Chdir(t.TempDir())

	req := multipartRequestWithDeclaredType(t, "file", "payload.html", "image/png", []byte(scriptedHTML))
	message, _ := newTestMessage(t, req)
	parser := &MultipartParser{message: message}

	var dto TestStruct
	err := parser.Parse(&dto)

	require.Error(t, err, "the DTO path must refuse html content too")
	var validationErrors *error2.ValidationErrors
	require.ErrorAs(t, err, &validationErrors)
	assert.Contains(t, err.Error(), "text/html")
	assert.Nil(t, dto.File, "and must not bind a file it refused")
	assert.Empty(t, frameworkTempFiles(t))
}

// TestTheDtoBindingPathStoresBySniffedExtension is the same request with real image bytes:
// the upload is accepted, and named for what it is rather than for what it was called.
func TestTheDtoBindingPathStoresBySniffedExtension(t *testing.T) {
	t.Chdir(t.TempDir())

	content := pngContent(4096)
	req := multipartRequestWithDeclaredType(t, "file", "payload.svg", "image/svg+xml", content)
	message, _ := newTestMessage(t, req)
	parser := &MultipartParser{message: message}

	var dto TestStruct
	require.NoError(t, parser.Parse(&dto))
	require.NotNil(t, dto.File)

	assert.Equal(t, ".png", filepath.Ext(dto.File.GetName()))

	var out bytes.Buffer
	_, err := dto.File.Read(&out)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, out.Bytes()), "an accepted upload must round-trip byte-identically")
}

func multipartRequestWithDeclaredType(t *testing.T, field, filename, declared string, content []byte) *http.Request {
	t.Helper()

	const boundary = "declaredtypeboundary"
	var body bytes.Buffer
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(`Content-Disposition: form-data; name="` + field + `"; filename="` + filename + "\"\r\n")
	body.WriteString("Content-Type: " + declared + "\r\n\r\n")
	body.Write(content)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req, err := http.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body.Bytes()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	return req
}
