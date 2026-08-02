package http

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/osbits/gorgany/v2/app"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/model"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingReader reports how many bytes were actually pulled off the wire, which is the
// only way to tell "refused" apart from "buffered, then rejected".
type countingReader struct {
	remaining int64
	read      atomic.Int64
	fill      byte
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > r.remaining {
		n = r.remaining
	}
	for i := int64(0); i < n; i++ {
		p[i] = r.fill
	}
	r.remaining -= n
	r.read.Add(n)
	return int(n), nil
}

func (r *countingReader) Close() error { return nil }

func withViper(t *testing.T, values map[string]any) {
	t.Helper()

	previous := make(map[string]any, len(values))
	for key := range values {
		previous[key] = viper.Get(key)
	}
	for key, value := range values {
		viper.Set(key, value)
	}
	t.Cleanup(func() {
		for key, value := range previous {
			viper.Set(key, value)
		}
	})
}

func newTestMessage(t *testing.T, req *http.Request) (*Message, *httptest.ResponseRecorder) {
	t.Helper()

	recorder := httptest.NewRecorder()
	message := &Message{writer: recorder, request: req}
	message.Init()
	// Init builds a session scope over the injected auth context, which nothing injects
	// here; Close would reach through it for the flash cleanup.
	message.Ses = &mockSessionScope{}
	return message, recorder
}

// TestAnOversizeBodyIsRefusedBeforeItIsBuffered is the headline for the body cap: nothing
// wrapped a request body in http.MaxBytesReader anywhere in the tree, so the assertion that
// matters is the byte counter rather than the error.
//
// It reads through BodyReader deliberately. Body() applied a LimitReader of its own, so it
// was the one reader with any ceiling at all — every other consumer of the body, including
// ParseForm and any hand-rolled streaming a handler does, had none. The cap now sits on the
// request itself, which is what makes it apply to all of them.
func TestAnOversizeBodyIsRefusedBeforeItIsBuffered(t *testing.T) {
	const ceiling = 1 << 20
	const streamed = 256 << 20
	withViper(t, map[string]any{app.ConfigMaxRequestBytes: ceiling})

	body := &countingReader{remaining: streamed, fill: 'a'}
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", "application/octet-stream")

	message, _ := newTestMessage(t, req)

	_, err := io.ReadAll(message.Request().BodyReader())
	require.Error(t, err, "a body over the ceiling must fail the reader")

	assert.LessOrEqual(t, body.read.Load(), int64(ceiling)+4096,
		"the body must be refused at the ceiling, not read to the end and measured afterwards")
	assert.Less(t, body.read.Load(), int64(streamed), "the whole body was pulled off the wire")
}

// TestAnOversizeMultipartBodyIsRefusedBeforeItSpillsToDisk is the same assertion on the
// path that used to be worse: ParseMultipartForm's argument is the in-memory budget, so
// everything above it went to the OS temp directory with no total limit and the size checks
// ran afterwards.
func TestAnOversizeMultipartBodyIsRefusedBeforeItSpillsToDisk(t *testing.T) {
	const budget = 2 << 20
	withViper(t, map[string]any{
		ConfigMaxMultipartSize:         budget,
		app.ConfigMaxRequestBytes:      nil,
		"http.security.body.maxSizeMB": 1,
	})

	const streamed = 64 << 20
	prologue := multipartPrologue(t, "avatar", "big.png")
	content := &countingReader{remaining: streamed, fill: 'a'}
	body := io.MultiReader(strings.NewReader(prologue.header), content)

	req := httptest.NewRequest(http.MethodPost, "/upload", io.NopCloser(body))
	req.Header.Set("Content-Type", prologue.contentType)

	message, _ := newTestMessage(t, req)

	assert.Nil(t, message.Request().GetMultipartFormValues(),
		"a body over the upload budget must not parse")

	// The budget plus the framing allowance is the ceiling the reader enforces, so that
	// plus a read buffer's worth is the most that can have left the client.
	assert.LessOrEqual(t, content.read.Load(), int64(budget)+multipartFramingAllowance+65536,
		"the parser must stop at the budget, not spill the whole body to disk and measure it after")
	assert.Less(t, content.read.Load(), int64(streamed),
		"the entire body was read")
}

// TestAMalformedMultipartBodyIs400RatherThanAPanic. GetMultipartFormValues returns nil when
// the parse fails and validateFormSize ranged straight over form.File, so any client could
// panic any multipart endpoint with a truncated body.
func TestAMalformedMultipartBodyIs400RatherThanAPanic(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/upload",
		strings.NewReader("--boundary\r\nnot a part at all"))
	req.Header.Set("Content-Type", `multipart/form-data; boundary=boundary`)

	message, _ := newTestMessage(t, req)
	parser := &MultipartParser{message: message}

	var err error
	require.NotPanics(t, func() {
		err = parser.Parse(&TestStruct{})
	})

	require.Error(t, err)
	var parseError *error2.InputBodyParseError
	require.ErrorAs(t, err, &parseError, "a body that will not parse is a 400, not a 500")
	assert.Equal(t, "multipart/form-data", parseError.Type)
}

// TestAMissingMultipartFormDoesNotPanicTheSizeValidator covers the guard directly, since a
// nil form can reach validateFormSize from any IRequestScope implementation.
func TestAMissingMultipartFormDoesNotPanicTheSizeValidator(t *testing.T) {
	parser := &MultipartParser{}
	require.NotPanics(t, func() {
		assert.NoError(t, parser.validateFormSize(nil, resolveUploadLimits()))
	})
}

// TestTempFilesAreGoneAfterTheRequest. Two leaks, one per temp directory: the parser's own
// spilled parts in the OS temp directory, which Message.Close never removed because it
// opened a second handle per part instead of calling RemoveAll, and the framework's copies
// under resource/temp, which nothing removed because the cleanup was commented out.
func TestTempFilesAreGoneAfterTheRequest(t *testing.T) {
	t.Chdir(t.TempDir())
	withViper(t, map[string]any{"http.security.body.maxSizeMB": 1})

	content := make([]byte, 2<<20)
	copy(content, "\x89PNG\r\n\x1a\n")
	req := multipartRequestWithFile(t, "file", "avatar.png", content)

	message, _ := newTestMessage(t, req)
	parser := &MultipartParser{message: message}

	var dto TestStruct
	require.NoError(t, parser.Parse(&dto))
	require.NotNil(t, dto.File)

	spilled := spilledPartPaths(t, req)
	require.NotEmpty(t, spilled, "the 2 MB part must have spilled past the 1 MB in-memory budget")
	require.NotEmpty(t, frameworkTempFiles(t), "the framework keeps its own copy under resource/temp")

	require.NoError(t, message.Close())

	for _, path := range spilled {
		_, err := os.Stat(path)
		assert.True(t, os.IsNotExist(err), "spilled part %s survived the request", path)
	}
	assert.Empty(t, frameworkTempFiles(t), "resource/temp must be empty once the request is over")
}

// TestAPublishedUploadSurvivesTheRequestCleanup is the counterpart, and the reason the
// cleanup could not simply be uncommented: Close resolved its own target through the
// public path once Write had set one.
func TestAPublishedUploadSurvivesTheRequestCleanup(t *testing.T) {
	t.Chdir(t.TempDir())

	content := pngContent(4096)
	req := multipartRequestWithFile(t, "file", "avatar.png", content)

	message, _ := newTestMessage(t, req)
	parser := &MultipartParser{message: message}

	var dto TestStruct
	require.NoError(t, parser.Parse(&dto))
	require.NotNil(t, dto.File)

	// What a handler does with a bound upload.
	written, err := dto.File.Write("avatars", nil)
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), written)
	stored := dto.File.FullPath()

	require.NoError(t, message.Close())

	storedBytes, err := os.ReadFile(stored)
	require.NoError(t, err, "the published upload must still be there after the request")
	assert.True(t, bytes.Equal(content, storedBytes), "and it must be byte-identical")
	assert.Empty(t, frameworkTempFiles(t))
}

// TestTheHandRolledUploadPathsAlsoReleaseTheirTempCopies. FormFile and GetFiles write the
// same copy under resource/temp that the DTO path does, and nothing released it: the
// request-scoped cleanup only ever saw the files MultipartParser had bound, so an app taking
// its uploads through the documented request API grew resource/temp by one file per upload
// for the life of the deployment. Neither method can close what it returns — the caller is
// about to publish it — so the request has to.
func TestTheHandRolledUploadPathsAlsoReleaseTheirTempCopies(t *testing.T) {
	t.Run("FormFile", func(t *testing.T) {
		t.Chdir(t.TempDir())

		req := multipartRequestWithFile(t, "avatar", "avatar.png", pngContent(4096))
		message, _ := newTestMessage(t, req)

		file, err := message.Request().FormFile("avatar")
		require.NoError(t, err)
		require.NotEmpty(t, frameworkTempFiles(t), "the temp copy exists while the request runs")

		require.NoError(t, message.Close())
		assert.Empty(t, frameworkTempFiles(t), "resource/temp must be empty once the request is over")
		_ = file
	})

	t.Run("GetFiles", func(t *testing.T) {
		t.Chdir(t.TempDir())

		req := multipartRequestWithFile(t, "gallery", "one.png", pngContent(4096))
		message, _ := newTestMessage(t, req)

		files, err := message.Request().GetFiles("gallery")
		require.NoError(t, err)
		require.Len(t, files, 1)
		require.NotEmpty(t, frameworkTempFiles(t))

		require.NoError(t, message.Close())
		assert.Empty(t, frameworkTempFiles(t))
	})

	t.Run("a published upload still survives", func(t *testing.T) {
		t.Chdir(t.TempDir())

		content := pngContent(4096)
		req := multipartRequestWithFile(t, "avatar", "avatar.png", content)
		message, _ := newTestMessage(t, req)

		file, err := message.Request().FormFile("avatar")
		require.NoError(t, err)

		written, err := file.Write("avatars", nil)
		require.NoError(t, err)
		require.Equal(t, int64(len(content)), written)
		stored := file.FullPath()

		require.NoError(t, message.Close())

		storedBytes, err := os.ReadFile(stored)
		require.NoError(t, err, "the published upload must outlive the request")
		assert.True(t, bytes.Equal(content, storedBytes))
		assert.Empty(t, frameworkTempFiles(t))
	})
}

// TestTheRequestCeilingTracksTheMultipartBudget pins app.MaxRequestBodyBytes to the config
// key http declares. The two live in different packages and cannot reference each other, so
// nothing but this test stops them drifting apart and refusing uploads the upload limits
// allow.
func TestTheRequestCeilingTracksTheMultipartBudget(t *testing.T) {
	withViper(t, map[string]any{app.ConfigMaxRequestBytes: nil, ConfigMaxMultipartSize: nil})
	assert.Greater(t, app.MaxRequestBodyBytes(), DefaultMaxMultipartSize,
		"the default ceiling must leave room for a form at the default budget")

	withViper(t, map[string]any{ConfigMaxMultipartSize: 512 << 20})
	assert.Greater(t, app.MaxRequestBodyBytes(), int64(512<<20),
		"raising the upload budget must raise the ceiling with it")

	withViper(t, map[string]any{app.ConfigMaxRequestBytes: 4096})
	assert.Equal(t, int64(4096), app.MaxRequestBodyBytes(),
		"an explicit ceiling wins over the derivation")
}

// helpers

func pngContent(size int) []byte {
	out := make([]byte, size)
	copy(out, "\x89PNG\r\n\x1a\n")
	for i := 8; i < size; i++ {
		out[i] = byte(i % 251)
	}
	return out
}

func multipartRequestWithFile(t *testing.T, field, filename string, content []byte) *http.Request {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(field, filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

type prologue struct {
	header      string
	contentType string
}

// multipartPrologue builds just the opening of a multipart body, so the part content can be
// streamed after it without ever being materialised.
func multipartPrologue(t *testing.T, field, filename string) prologue {
	t.Helper()

	const boundary = "streamingboundary"
	return prologue{
		header: fmt.Sprintf("--%s\r\n"+
			"Content-Disposition: form-data; name=%q; filename=%q\r\n"+
			"Content-Type: image/png\r\n\r\n", boundary, field, filename),
		contentType: fmt.Sprintf("multipart/form-data; boundary=%s", boundary),
	}
}

func frameworkTempFiles(t *testing.T) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(model.TempStorage, "*"))
	require.NoError(t, err)
	return matches
}

func spilledPartPaths(t *testing.T, req *http.Request) []string {
	t.Helper()

	if req.MultipartForm == nil {
		return nil
	}

	var paths []string
	for _, headers := range req.MultipartForm.File {
		for _, header := range headers {
			opened, err := header.Open()
			if err != nil {
				continue
			}
			if onDisk, ok := opened.(*os.File); ok {
				paths = append(paths, onDisk.Name())
			}
			opened.Close()
		}
	}
	return paths
}
