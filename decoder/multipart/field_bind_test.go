package multipart

import (
	"bytes"
	"io"
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

// SEC-H11. The decoder wrote the upload to disk and *then* assigned it to the DTO field, and
// the assignment is where reflect notices the field cannot hold a file. reflect signals that
// by panicking, a panic unwinds without returning, and the closers for every file stored so
// far were a return value — so they were never handed back and never released.
//
// RecoveryMiddleware is registered by default, so the panic was a 500 and the process kept
// serving: an unauthenticated caller could repeat the request and fill the disk. The panic was
// the symptom; the leak was the defect.
//
// The part name is matched case-insensitively against the DTO's fields, so both shapes below
// are chosen entirely by the client.

// stringFieldDto has a field a file cannot be assigned to.
type stringFieldDto struct {
	Title string `scheme:"title"`
}

// unexportedFieldDto has a field that matches by name and cannot be set.
type unexportedFieldDto struct {
	file core.IFile //nolint:unused // matched by FieldByNameFunc, which ignores exportedness
}

// mixedDto takes a real upload and also has a field that cannot hold one, so a single request
// can store one file and then fail on the next.
type mixedDto struct {
	File  core.IFile `scheme:"file"`
	Title string     `scheme:"title"`
}

// twoPartForm builds a form carrying two parts, both of them files.
func twoPartForm(t *testing.T, firstField, secondField string) map[string][]*multipart.FileHeader {
	t.Helper()

	const boundary = "twopartboundary"
	var body bytes.Buffer
	for _, field := range []string{firstField, secondField} {
		body.WriteString("--" + boundary + "\r\n")
		body.WriteString(`Content-Disposition: form-data; name="` + field +
			`"; filename="` + field + ".png\"\r\n")
		body.WriteString("Content-Type: image/png\r\n\r\n")
		body.Write(pngContent(64))
		body.WriteString("\r\n")
	}
	body.WriteString("--" + boundary + "--\r\n")

	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	require.NoError(t, req.ParseMultipartForm(32<<20))
	t.Cleanup(func() { _ = req.MultipartForm.RemoveAll() })

	return req.MultipartForm.File
}

// tempFiles is what is sitting in the framework's temp directory right now.
func tempFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(model.TempStorage, "*"))
	require.NoError(t, err)
	return matches
}

// TestAFilePartMatchingAStringFieldIsRejectedWithoutStoringAnything.
func TestAFilePartMatchingAStringFieldIsRejectedWithoutStoringAnything(t *testing.T) {
	t.Chdir(t.TempDir())

	files := formFilesFor(t, "title", "payload.png", "image/png", pngContent(64))

	dto := &stringFieldDto{}
	var stored []interface{ Close() error }
	var err error

	require.NotPanics(t, func() {
		closers, decodeErr := DecodeFiles(files, dto)
		err = decodeErr
		for _, closer := range closers {
			stored = append(stored, closer)
		}
	}, "reflect.Set panics when the field cannot hold a file, and the panic loses the closers")

	var bindError *FieldBindError
	require.ErrorAs(t, err, &bindError)
	assert.Equal(t, "title", bindError.Field)
	assert.Empty(t, stored)
	assert.Empty(t, tempFiles(t), "nothing may be left in resource/temp")
}

// TestAFilePartMatchingAnUnexportedFieldIsRejectedWithoutStoringAnything.
//
// FieldByNameFunc matches unexported fields too, so this is reachable purely by choosing the
// part name — and CanSet, not assignability, is what catches it.
func TestAFilePartMatchingAnUnexportedFieldIsRejectedWithoutStoringAnything(t *testing.T) {
	t.Chdir(t.TempDir())

	files := formFilesFor(t, "file", "payload.png", "image/png", pngContent(64))

	var err error
	require.NotPanics(t, func() {
		_, err = DecodeFiles(files, &unexportedFieldDto{})
	})

	var bindError *FieldBindError
	require.ErrorAs(t, err, &bindError)
	assert.Empty(t, tempFiles(t))
}

// TestAnEarlierStoredFileIsReleasedWhenALaterFieldFails is the leak test.
//
// Map iteration order is unspecified, so the loop runs enough times for both orderings to
// occur: the case that matters is the one where a real upload is written first and the
// incompatible field is reached second.
func TestAnEarlierStoredFileIsReleasedWhenALaterFieldFails(t *testing.T) {
	t.Chdir(t.TempDir())

	for i := 0; i < 25; i++ {
		files := twoPartForm(t, "file", "title")

		var err error
		require.NotPanics(t, func() {
			_, err = DecodeFiles(files, &mixedDto{})
		})
		require.Error(t, err)

		require.Empty(t, tempFiles(t),
			"iteration %d left a temp file behind; that is the disk fill", i)
	}
}

// TestDecodeFilesNeverPanicsOnAHostileDestination. The destination's shape is checked before
// any reflection that could panic on it.
func TestDecodeFilesNeverPanicsOnAHostileDestination(t *testing.T) {
	t.Chdir(t.TempDir())

	destinations := map[string]any{
		"nil":                 nil,
		"typed nil pointer":   (*uploadDto)(nil),
		"pointer to slice":    &[]int{},
		"pointer to map":      &map[string]any{},
		"non-pointer struct":  uploadDto{},
		"pointer to a string": new(string),
	}

	for name, dest := range destinations {
		t.Run(name, func(t *testing.T) {
			files := formFilesFor(t, "file", "payload.png", "image/png", pngContent(64))

			var err error
			require.NotPanics(t, func() {
				_, err = DecodeFiles(files, dest)
			})
			assert.Error(t, err)
			assert.Empty(t, tempFiles(t))
		})
	}
}

// TestTheRejectionMessageDoesNotNameGoTypes. The reason reaches the client, and internal type
// names are of no use to a legitimate one.
func TestTheRejectionMessageDoesNotNameGoTypes(t *testing.T) {
	t.Chdir(t.TempDir())

	files := formFilesFor(t, "title", "payload.png", "image/png", pngContent(64))
	_, err := DecodeFiles(files, &stringFieldDto{})
	require.Error(t, err)

	var bindError *FieldBindError
	require.ErrorAs(t, err, &bindError)
	assert.NotContains(t, bindError.Reason, "MultipartFile")
	assert.NotContains(t, bindError.Reason, "reflect")
	assert.NotContains(t, bindError.Reason, "string")
}

// TestACompatibleFieldStillBinds is the over-blocking fence.
func TestACompatibleFieldStillBinds(t *testing.T) {
	t.Chdir(t.TempDir())

	files := formFilesFor(t, "file", "payload.png", "image/png", pngContent(64))

	dto := &uploadDto{}
	stored, err := DecodeFiles(files, dto)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.NotNil(t, dto.File)

	for _, closer := range stored {
		require.NoError(t, closer.Close())
	}
	assert.Empty(t, tempFiles(t))
}

// countingCloser records that it was released.
type countingCloser struct{ closed int }

func (c *countingCloser) Close() error { c.closed++; return nil }

// TestTheCleanupGuardReleasesEverythingOnAPanic fences the backstop directly.
//
// It has to be driven directly, because the explicit CanSet and AssignableTo checks close the
// two panics that were actually reachable. The guard stays because *the leak, not the panic,
// is the security defect*: reflect has panic sources those checks do not enumerate, and any
// one of them would re-open unauthenticated repeatable disk fill.
func TestTheCleanupGuardReleasesEverythingOnAPanic(t *testing.T) {
	first, second := &countingCloser{}, &countingCloser{}

	var stored []io.Closer
	var err error

	func() {
		defer releaseOnFailure(&stored, &err)
		stored = []io.Closer{first, second}
		panic("something reflect did not warn about")
	}()

	assert.Equal(t, 1, first.closed, "everything stored before the panic must be released")
	assert.Equal(t, 1, second.closed)
	assert.Nil(t, stored, "and ownership must not be left ambiguous")

	var bindError *FieldBindError
	require.ErrorAs(t, err, &bindError,
		"the panic has to become an error the HTTP layer can turn into a 4xx")
}

// TestTheCleanupGuardLeavesASuccessfulCallAlone — the files a successful bind stored belong to
// the caller, which publishes them and releases them when the request ends.
func TestTheCleanupGuardLeavesASuccessfulCallAlone(t *testing.T) {
	closer := &countingCloser{}

	stored := []io.Closer{closer}
	var err error
	releaseOnFailure(&stored, &err)

	assert.Zero(t, closer.closed)
	assert.Len(t, stored, 1)
}
