package model

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pngBytes returns a payload that http.DetectContentType reports as image/png, padded
// past the 512-byte sniff window so the rewind path is exercised rather than the
// "whole file fits in the head buffer" shortcut.
func pngBytes(size int) []byte {
	magic := []byte("\x89PNG\r\n\x1a\n")
	if size < len(magic) {
		size = len(magic)
	}
	out := make([]byte, size)
	copy(out, magic)
	for i := len(magic); i < size; i++ {
		out[i] = byte(i % 251)
	}
	return out
}

const htmlPayload = `<html><body><script>fetch('/me')</script></body></html>`

const svgPayload = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`

const xmlSvgPayload = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`

func inIsolatedStorage(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

// TestHtmlUploadIsNotStoredWithAnHtmlExtension is the stored-XSS case: the part is named
// payload.html, the client declares image/png, and nothing in the upload path used to
// look at the bytes.
func TestHtmlUploadIsNotStoredWithAnHtmlExtension(t *testing.T) {
	inIsolatedStorage(t)

	for _, name := range []string{"payload.html", "payload.HTML", "payload.htm", "payload.xhtml"} {
		t.Run(name, func(t *testing.T) {
			file, err := NewMultipartFile(name, bytes.NewReader([]byte(htmlPayload)))
			if err == nil {
				t.Cleanup(func() { _ = file.Close() })
				assert.NotContains(t, strings.ToLower(file.GetName()), ".htm",
					"html content must never be stored under an html extension")
			}
			require.Error(t, err, "html content is not an allowed upload type")
		})
	}
}

// TestSvgUploadIsNotStoredWithAnSvgExtension — an SVG is a script host too, and
// image/svg+xml is what the extension would have earned it on the way back out.
func TestSvgUploadIsNotStoredWithAnSvgExtension(t *testing.T) {
	inIsolatedStorage(t)

	for name, payload := range map[string]string{
		"payload.svg":       svgPayload,
		"payload.SVG":       svgPayload,
		"declared-xml.svg":  xmlSvgPayload,
		"png-named-svg.svg": string(pngBytes(600)),
	} {
		t.Run(name, func(t *testing.T) {
			file, err := NewMultipartFile(name, bytes.NewReader([]byte(payload)))
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = file.Close() })

			assert.NotEqual(t, ".svg", strings.ToLower(filepath.Ext(file.GetName())),
				"the stored extension must come from the sniffed type, not the filename")
		})
	}
}

// TestStoredExtensionComesFromTheSniffedType: the client's extension is advisory, the
// bytes decide.
func TestStoredExtensionComesFromTheSniffedType(t *testing.T) {
	inIsolatedStorage(t)

	cases := []struct {
		name    string
		payload []byte
		wantExt string
	}{
		{"avatar.txt", pngBytes(600), ".png"},
		{"avatar", pngBytes(600), ".png"},
		{"doc.png", []byte("%PDF-1.7\nnot really a pdf but the magic is what counts"), ".pdf"},
		{"notes.png", []byte("just some plain text, nothing executable here"), ".txt"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			file, err := NewMultipartFile(c.name, bytes.NewReader(c.payload))
			require.NoError(t, err)
			t.Cleanup(func() { _ = file.Close() })

			assert.Equal(t, c.wantExt, filepath.Ext(file.GetName()))
		})
	}
}

// TestAllowedUploadRoundTripsByteIdentically is the regression fence. Sniffing means the
// first 512 bytes are read before the copy starts, so a mistake here silently truncates
// or duplicates content.
func TestAllowedUploadRoundTripsByteIdentically(t *testing.T) {
	inIsolatedStorage(t)

	for _, size := range []int{8, 511, 512, 513, 4096, 200000} {
		content := pngBytes(size)

		file, err := NewMultipartFile("photo.png", bytes.NewReader(content))
		require.NoError(t, err)

		written, err := file.Write("gallery", nil)
		require.NoError(t, err)
		assert.Equal(t, int64(len(content)), written, "size %d: byte count changed", size)

		var out bytes.Buffer
		_, err = file.Read(&out)
		require.NoError(t, err)
		assert.True(t, bytes.Equal(content, out.Bytes()), "size %d: content changed", size)

		require.NoError(t, file.Close())
	}
}

// TestClosingAfterWriteKeepsThePermanentFile pins the reason the temp-file cleanup was
// commented out: Close() resolved its own path through FullPath() once Write had set one,
// so the cleanup deleted the file it had just published.
func TestClosingAfterWriteKeepsThePermanentFile(t *testing.T) {
	inIsolatedStorage(t)

	content := pngBytes(600)
	file, err := NewMultipartFile("photo.png", bytes.NewReader(content))
	require.NoError(t, err)

	_, err = file.Write("gallery", nil)
	require.NoError(t, err)
	stored := file.FullPath()

	require.NoError(t, file.Close())

	assert.True(t, file.IsExists(), "Close must not remove the published file at %s", stored)
	assert.Empty(t, tempStorageEntries(t), "the temp copy must be gone")

	// Closing twice is what a request-scoped cleanup will do after a handler already
	// closed the file itself; it must not turn into an error.
	assert.NoError(t, file.Close())
}

// TestCloseRemovesTheTempCopyWhenNothingWasPublished covers the other half: an upload the
// handler never stored must not be left behind in resource/temp.
func TestCloseRemovesTheTempCopyWhenNothingWasPublished(t *testing.T) {
	inIsolatedStorage(t)

	file, err := NewMultipartFile("photo.png", bytes.NewReader(pngBytes(600)))
	require.NoError(t, err)
	require.NotEmpty(t, tempStorageEntries(t))

	require.NoError(t, file.Close())
	assert.Empty(t, tempStorageEntries(t))
}

func TestUploadTypeAllowlistIsConfigurable(t *testing.T) {
	inIsolatedStorage(t)

	previous := viper.Get(ConfigAllowedUploadTypes)
	t.Cleanup(func() { viper.Set(ConfigAllowedUploadTypes, previous) })

	viper.Set(ConfigAllowedUploadTypes, map[string]any{"image/gif": ".gif"})

	_, err := NewMultipartFile("photo.png", bytes.NewReader(pngBytes(600)))
	require.Error(t, err, "png is not on the configured allowlist")
	var typeErr *UploadTypeError
	require.True(t, errors.As(err, &typeErr))
	assert.Equal(t, "image/png", typeErr.MediaType)

	file, err := NewMultipartFile("anim.png", bytes.NewReader([]byte("GIF89a"+strings.Repeat("x", 600))))
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })
	assert.Equal(t, ".gif", filepath.Ext(file.GetName()))
}

func TestEmptyUploadIsRejectedRatherThanGuessed(t *testing.T) {
	inIsolatedStorage(t)

	_, err := NewMultipartFile("empty.png", bytes.NewReader(nil))
	require.Error(t, err)
}

// TestAConfiguredExtensionCannotShapeTheStoredPath. The extension is the one part of the
// stored name this framework now vouches for, and with the allowlist configurable it is a
// value that arrives from outside the code. An entry of "../../pwned.html" was taken
// verbatim, so the temp copy landed a directory above resource/temp under an extension
// nothing had approved — and MultipartFile.Write joins the same name onto the public root.
// A configured entry that is not a plain extension has to be refused, not obeyed.
func TestAConfiguredExtensionCannotShapeTheStoredPath(t *testing.T) {
	inIsolatedStorage(t)

	previous := viper.Get(ConfigAllowedUploadTypes)
	t.Cleanup(func() { viper.Set(ConfigAllowedUploadTypes, previous) })

	tempRoot, err := filepath.Abs(TempStorage)
	require.NoError(t, err)

	for _, configured := range []string{
		"../../pwned.html",
		"./../pwned.png",
		".png/../../x.png",
		".png\r\nX-Evil: 1",
		".png\x00.html",
		`.png\..\x.png`,
		".",
		"..",
	} {
		t.Run(configured, func(t *testing.T) {
			viper.Set(ConfigAllowedUploadTypes, map[string]any{"image/png": configured})

			file, err := NewMultipartFile("avatar.png", bytes.NewReader(pngBytes(600)))
			if err != nil {
				// Refusing the entry outright is the other acceptable answer.
				return
			}
			t.Cleanup(func() { _ = file.Close() })

			name := file.GetName()
			assert.NotContains(t, name, "/", "a path separator must never reach the stored name")
			assert.NotContains(t, name, `\`, "a path separator must never reach the stored name")
			assert.NotContains(t, name, "\x00")
			assert.False(t, strings.ContainsAny(name, "\r\n"),
				"a newline in the stored name would be a header-injection primitive: %q", name)

			stored, err := filepath.Abs(filepath.Join(TempStorage, name))
			require.NoError(t, err)
			assert.Equal(t, tempRoot, filepath.Dir(stored),
				"the upload must stay inside %s, got %s", tempRoot, stored)
		})
	}
}

// TestAnUnusableAllowlistFallsBackToTheBuiltInTable. The config key replaces the built-in
// table wholesale, so a value nothing usable can be read out of would otherwise mean "refuse
// every upload" — a typo in a config file taking the whole upload feature down. Falling back
// is safe because the built-in table is the conservative one: it is what excludes the script
// hosts in the first place.
func TestAnUnusableAllowlistFallsBackToTheBuiltInTable(t *testing.T) {
	inIsolatedStorage(t)

	previous := viper.Get(ConfigAllowedUploadTypes)
	t.Cleanup(func() { viper.Set(ConfigAllowedUploadTypes, previous) })

	for _, configured := range []map[string]any{
		{"": ""},
		{"image/png": ""},
		{"": ".png"},
		{"image/png": "../../evil"},
	} {
		viper.Set(ConfigAllowedUploadTypes, configured)

		file, err := NewMultipartFile("photo.png", bytes.NewReader(pngBytes(600)))
		require.NoError(t, err, "an unusable allowlist %v must not refuse every upload", configured)
		assert.Equal(t, ".png", filepath.Ext(file.GetName()))
		require.NoError(t, file.Close())

		_, err = NewMultipartFile("payload.html", bytes.NewReader([]byte(htmlPayload)))
		require.Error(t, err, "and the fallback must still be the table that excludes html")
	}
}

// TestALongFilenameIsStillStored. Every component of the stored name but the base came
// from the framework, and the base was passed through at whatever length the client sent —
// so a filename longer than the filesystem's per-component limit (255 bytes nearly
// everywhere) made os.Create fail and the upload was refused with "file name too long".
// That is an ordinary browser upload from a user with a verbose filename, not an attack.
func TestALongFilenameIsStillStored(t *testing.T) {
	inIsolatedStorage(t)

	for _, length := range []int{50, 200, 255, 300, 4000} {
		file, err := NewMultipartFile(strings.Repeat("x", length)+".png", bytes.NewReader(pngBytes(600)))
		require.NoError(t, err, "a %d-character filename must still be storable", length)
		t.Cleanup(func() { _ = file.Close() })

		assert.LessOrEqual(t, len(file.GetName()), 255,
			"the stored name has to fit in one filesystem path component")
		assert.Equal(t, ".png", filepath.Ext(file.GetName()))
	}
}

// TestWriteWithoutAContentSourceIsAnErrorNotAPanic. Write reads the temp copy when there is
// one and the caller's reader otherwise, and with neither it dereferenced the nil reader.
// The reachable shape is a released file: MultipartParser closes the stored uploads
// immediately for a message that does not take part in request-scoped cleanup, and the
// handler then publishes with Write(path, nil).
func TestWriteWithoutAContentSourceIsAnErrorNotAPanic(t *testing.T) {
	inIsolatedStorage(t)

	file, err := NewMultipartFile("avatar.png", bytes.NewReader(pngBytes(600)))
	require.NoError(t, err)
	require.NoError(t, file.Close())

	require.NotPanics(t, func() {
		_, err = file.Write("avatars", nil)
	})
	assert.Error(t, err, "a released upload has nothing left to publish")

	bare := &MultipartFile{}
	bare.SetName("avatar.png")
	require.NotPanics(t, func() {
		_, err = bare.Write("avatars", nil)
	})
	assert.Error(t, err)
}

func tempStorageEntries(t *testing.T) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(TempStorage, "*"))
	require.NoError(t, err)
	return matches
}
