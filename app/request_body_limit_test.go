package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingBody reports how many bytes were actually pulled off the wire, which is the only
// way to tell "refused at the ceiling" apart from "read to the end, then rejected".
type countingBody struct {
	remaining int64
	read      atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > b.remaining {
		n = b.remaining
	}
	b.remaining -= n
	b.read.Add(n)
	return int(n), nil
}

func (b *countingBody) Close() error { return nil }

// TestTheServerBoundaryCapsTheBodyBeforeRouting. ServerApp.Run is where the ceiling has to
// be applied for the paths that never build a message — an app that mounts a plain
// http.Handler of its own on the same server, or anything answering before the router's
// message middleware runs. The framework caps per request as well, and that is the wrapper
// that covers every reader of the body; this one exists because it is the only one that is
// in place before routing happens at all.
func TestTheServerBoundaryCapsTheBodyBeforeRouting(t *testing.T) {
	const ceiling = 1 << 20
	const streamed = 64 << 20

	body := &countingBody{remaining: streamed}
	req := httptest.NewRequest(http.MethodPost, "/anything", body)

	var readErr error
	var served bool
	handler := limitRequestBodies(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		served = true
		_, readErr = io.ReadAll(r.Body)
	}), ceiling)

	handler.ServeHTTP(httptest.NewRecorder(), req)

	require.True(t, served, "the request still has to reach the handler")
	require.Error(t, readErr, "a body over the ceiling must fail the reader")
	assert.LessOrEqual(t, body.read.Load(), int64(ceiling)+4096,
		"the body must be refused at the ceiling, not buffered and measured afterwards")
	assert.Less(t, body.read.Load(), int64(streamed), "the whole body was pulled off the wire")
}

// TestTheServerBoundaryLeavesASmallBodyAlone is the regression fence: a cap that refused
// ordinary requests would be worse than none.
func TestTheServerBoundaryLeavesASmallBodyAlone(t *testing.T) {
	body := &countingBody{remaining: 4096}
	req := httptest.NewRequest(http.MethodPost, "/anything", body)

	var read []byte
	var readErr error
	handler := limitRequestBodies(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		read, readErr = io.ReadAll(r.Body)
	}), 1<<20)

	handler.ServeHTTP(httptest.NewRecorder(), req)

	require.NoError(t, readErr)
	assert.Len(t, read, 4096)
}

// TestMaxRequestBodyBytesNeverUndercutsTheUploadBudget. The ceiling derives from the upload
// budget rather than standing on its own, because an app that raises the budget to accept
// larger uploads must not then have them refused here — a self-inflicted 413 on a request the
// upload limits explicitly allow. The framing allowance is what covers the part boundaries
// and headers a multipart body carries beyond the file bytes themselves.
func TestMaxRequestBodyBytesNeverUndercutsTheUploadBudget(t *testing.T) {
	restore := func(keys ...string) {
		previous := make(map[string]any, len(keys))
		for _, key := range keys {
			previous[key] = viper.Get(key)
		}
		t.Cleanup(func() {
			for key, value := range previous {
				viper.Set(key, value)
			}
		})
	}
	restore(ConfigMaxRequestBytes, configMaxMultipartSize)

	viper.Set(ConfigMaxRequestBytes, nil)
	for _, budget := range []int64{0, 1 << 20, DefaultMaxRequestBytes, 512 << 20} {
		viper.Set(configMaxMultipartSize, budget)
		assert.Greater(t, MaxRequestBodyBytes(), budget,
			"a budget of %d must leave room for the framing around it", budget)
		assert.GreaterOrEqual(t, MaxRequestBodyBytes(), DefaultMaxRequestBytes,
			"and a tighter budget must not tighten the ceiling below the default")
	}

	viper.Set(ConfigMaxRequestBytes, 4096)
	assert.Equal(t, int64(4096), MaxRequestBodyBytes(), "an explicit ceiling wins outright")
}
