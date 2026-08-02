package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ResponseWriterWrapper embeds four optional interfaces as fields, and the router fills in
// only the ones the underlying writer actually satisfies. Embedding still puts the promoted
// method in the wrapper's method set, though, so a type assertion against the wrapper
// succeeds for an interface the writer does not implement and the call dereferences nil.
//
// httptest.ResponseRecorder implements none of ReaderFrom, StringWriter or Hijacker, which is
// exactly the shape every router-level test and every middleware-wrapped writer has.

// wrapperOver builds the wrapper the way the router does for a writer that supports nothing
// optional — see http/router/gorgany.go, where each field is set only on a successful
// two-value assertion.
func wrapperOver(w http.ResponseWriter) *ResponseWriterWrapper {
	return &ResponseWriterWrapper{ResponseWriter: w, Writer: w, StatusCode: 200}
}

// plainReader reads and nothing else.
//
// It matters that the source has no WriteTo: io.Copy prefers src.WriteTo over dst.ReadFrom, so
// copying from a *strings.Reader never reaches the destination's ReadFrom at all and would
// test nothing. This is also the shape the real path has — http.ServeContent copies through
// io.CopyN, which wraps the file in an *io.LimitedReader, and that has no WriteTo either.
type plainReader struct{ r io.Reader }

func (p plainReader) Read(b []byte) (int, error) { return p.r.Read(b) }

// TestTheResponseWrapperReadFromFallsBackWhenTheWriterHasNone is the one that blocks
// streaming: http.ServeContent copies through io.CopyN, which prefers the destination's
// ReadFrom.
func TestTheResponseWrapperReadFromFallsBackWhenTheWriterHasNone(t *testing.T) {
	recorder := httptest.NewRecorder()
	wrapper := wrapperOver(recorder)

	var written int64
	var err error
	require.NotPanics(t, func() {
		written, err = io.Copy(wrapper, plainReader{strings.NewReader("streamed")})
	}, "io.Copy finds the wrapper's promoted ReadFrom and calls it against a nil ReaderFrom")

	require.NoError(t, err)
	assert.Equal(t, int64(len("streamed")), written)
	assert.Equal(t, "streamed", recorder.Body.String())
}

// TestTheResponseWrapperReadFromUsesTheWriterWhenItHasOne — the fallback must not cost a
// writer that can stream its ability to do so.
func TestTheResponseWrapperReadFromUsesTheWriterWhenItHasOne(t *testing.T) {
	recorder := httptest.NewRecorder()
	delegate := &countingReaderFrom{w: recorder}

	wrapper := wrapperOver(recorder)
	wrapper.ReaderFrom = delegate

	_, err := io.Copy(wrapper, plainReader{strings.NewReader("streamed")})
	require.NoError(t, err)
	assert.Equal(t, 1, delegate.calls, "the underlying writer's own ReadFrom should have been used")
	assert.Equal(t, "streamed", recorder.Body.String())
}

// TestTheResponseWrapperReadFromDoesNotRecurse. The fallback cannot copy with
// io.Copy(wrapper, r) — that finds this very method again and never terminates.
func TestTheResponseWrapperReadFromDoesNotRecurse(t *testing.T) {
	recorder := httptest.NewRecorder()
	wrapper := wrapperOver(recorder)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = wrapper.ReadFrom(plainReader{strings.NewReader(strings.Repeat("x", 1<<20))})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFrom did not terminate, which is what recursing through io.Copy looks like")
	}
	assert.Equal(t, 1<<20, recorder.Body.Len())
}

// TestTheResponseWrapperWriteStringFallsBackWhenTheWriterHasNone.
func TestTheResponseWrapperWriteStringFallsBackWhenTheWriterHasNone(t *testing.T) {
	recorder := httptest.NewRecorder()
	wrapper := wrapperOver(recorder)

	var n int
	var err error
	require.NotPanics(t, func() {
		n, err = io.WriteString(wrapper, "written")
	})

	require.NoError(t, err)
	assert.Equal(t, len("written"), n)
	assert.Equal(t, "written", recorder.Body.String())
}

// TestTheResponseWrapperFlushIsANoOpWhenTheWriterCannotFlush. A writer with no Flusher has
// nothing buffered to lose.
func TestTheResponseWrapperFlushIsANoOpWhenTheWriterCannotFlush(t *testing.T) {
	wrapper := wrapperOver(&plainWriter{})
	require.NotPanics(t, func() {
		wrapper.Flush()
	})
}

// TestTheResponseWrapperHijackReportsItCannotRatherThanPanicking. Same defect as ReadFrom, on
// the path a websocket upgrade takes. http.ErrNotSupported is the answer net/http gives from
// its own non-hijackable writers, so a caller that checks already handles it.
func TestTheResponseWrapperHijackReportsItCannotRatherThanPanicking(t *testing.T) {
	wrapper := wrapperOver(httptest.NewRecorder())

	hijacker, ok := any(wrapper).(http.Hijacker)
	require.True(t, ok, "the wrapper advertises Hijack whether or not the writer supports it")

	var err error
	require.NotPanics(t, func() {
		_, _, err = hijacker.Hijack()
	})
	assert.ErrorIs(t, err, http.ErrNotSupported)
}

// TestTheResponseWrapperStillRecordsTheStatusCodeOfAStreamedResponse — the bookkeeping the
// wrapper exists for has to survive the streaming path.
func TestTheResponseWrapperStillRecordsTheStatusCodeOfAStreamedResponse(t *testing.T) {
	recorder := httptest.NewRecorder()
	wrapper := wrapperOver(recorder)

	wrapper.WriteHeader(http.StatusPartialContent)
	_, err := io.Copy(wrapper, plainReader{strings.NewReader("partial")})
	require.NoError(t, err)

	assert.Equal(t, http.StatusPartialContent, wrapper.StatusCode)
	assert.Equal(t, http.StatusPartialContent, recorder.Code)
}

// --- doubles ---

type countingReaderFrom struct {
	w     io.Writer
	calls int
}

func (c *countingReaderFrom) ReadFrom(r io.Reader) (int64, error) {
	c.calls++
	return io.Copy(c.w, r)
}

// plainWriter implements http.ResponseWriter and nothing else.
type plainWriter struct{ header http.Header }

func (p *plainWriter) Header() http.Header {
	if p.header == nil {
		p.header = http.Header{}
	}
	return p.header
}
func (p *plainWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *plainWriter) WriteHeader(int)             {}
