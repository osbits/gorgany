package multipart

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type uploadDto struct {
	File core.IFile `scheme:"file"`
}

func formFilesFor(t *testing.T, field, filename, declaredType string, content []byte) map[string][]*multipart.FileHeader {
	t.Helper()

	const boundary = "decodefilesboundary"
	var body bytes.Buffer
	body.WriteString("--" + boundary + "\r\n")
	body.WriteString(`Content-Disposition: form-data; name="` + field + `"; filename="` + filename + "\"\r\n")
	body.WriteString("Content-Type: " + declaredType + "\r\n\r\n")
	body.Write(content)
	body.WriteString("\r\n--" + boundary + "--\r\n")

	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	require.NoError(t, req.ParseMultipartForm(32<<20))
	t.Cleanup(func() { _ = req.MultipartForm.RemoveAll() })

	return req.MultipartForm.File
}

func pngContent(size int) []byte {
	out := make([]byte, size)
	copy(out, "\x89PNG\r\n\x1a\n")
	for i := 8; i < size; i++ {
		out[i] = byte(i % 251)
	}
	return out
}

// TestDecodeFilesRejectsADisallowedType. This is the documented upload path — a core.IFile
// field on the DTO — and it applied no content check at all: the allowlist had two callers,
// both on the hand-rolled FormFile/GetFiles path.
func TestDecodeFilesRejectsADisallowedType(t *testing.T) {
	t.Chdir(t.TempDir())

	files := formFilesFor(t, "file", "payload.html", "image/png",
		[]byte(`<html><body><script>alert(1)</script></body></html>`))

	var dto uploadDto
	stored, err := DecodeFiles(files, &dto)

	require.Error(t, err, "html content must not be storable through the DTO path")
	var typeError *model.UploadTypeError
	require.ErrorAs(t, err, &typeError)
	assert.Equal(t, "text/html", typeError.MediaType)
	assert.Nil(t, dto.File, "and nothing must be bound onto the DTO")
	assert.Empty(t, stored)
}

// TestDecodeFilesBindsAnAllowedUpload is the round-trip fence: the part reader is closed
// inside DecodeFiles, so if the content were not fully copied out first the file bound onto
// the DTO would be short.
func TestDecodeFilesBindsAnAllowedUpload(t *testing.T) {
	t.Chdir(t.TempDir())

	content := pngContent(9000)
	files := formFilesFor(t, "file", "photo.svg", "image/svg+xml", content)

	var dto uploadDto
	stored, err := DecodeFiles(files, &dto)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.NotNil(t, dto.File)
	t.Cleanup(func() { _ = stored[0].Close() })

	assert.Equal(t, ".png", filepath.Ext(dto.File.GetName()),
		"the extension comes from the content, not from the part's filename")

	var out bytes.Buffer
	_, err = dto.File.Read(&out)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(content, out.Bytes()))
}

// TestDecodeFilesSkipsFieldsTheDtoDoesNotHave pins existing behaviour: an unmatched part is
// ignored rather than being an error.
func TestDecodeFilesSkipsFieldsTheDtoDoesNotHave(t *testing.T) {
	t.Chdir(t.TempDir())

	files := formFilesFor(t, "attachment", "photo.png", "image/png", pngContent(600))

	var dto uploadDto
	stored, err := DecodeFiles(files, &dto)
	require.NoError(t, err)
	assert.Empty(t, stored)
	assert.Nil(t, dto.File)
}
