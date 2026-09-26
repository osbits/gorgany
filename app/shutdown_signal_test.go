//go:build !windows

package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/event"
)

// These tests run ServerApp.Run in a child process and send it a real SIGTERM, because
// what they guard is how the process ends: its exit status, and whether a request that
// was in flight when the signal arrived got its whole response. Neither can be observed
// in-process. A panic on another goroutine kills the test binary, and os/signal delivery
// is process-wide.
//
// The child is this test binary, re-run with -test.run naming TestServerAppChildProcess
// and childEnv set. It runs from a temporary directory holding the .env and
// config/config.yaml that app.Run reads relative to the working directory.

const (
	childEnv = "GORGANY_SHUTDOWN_TEST_CHILD"

	// childInFlightMarker is written by the child's handler once a request is being
	// served, so the parent can send the signal mid-request rather than guessing when.
	childInFlightMarker = "CHILD: request in flight"

	// childSubscriberMarker is written by an async event subscriber the handler
	// publishes to. It finishes after the response is written, so only a shutdown that
	// drains the event bus lets it print.
	childSubscriberMarker = "CHILD: async subscriber finished"

	childHandlerDuration    = 1500 * time.Millisecond
	childSubscriberDuration = 500 * time.Millisecond
)

// childSubscriber stands in for an application's async subscriber: slow, and started by
// a request.
type childSubscriber struct{}

func (s *childSubscriber) Handle(context.Context) {
	time.Sleep(childSubscriberDuration)
	fmt.Fprintln(os.Stderr, childSubscriberMarker)
}

// TestServerAppChildProcess is the child's entry point, not a test of its own.
func TestServerAppChildProcess(t *testing.T) {
	if os.Getenv(childEnv) != "1" {
		t.Skip("runs only as the child of TestSigtermDuringARequest*")
	}

	bus := event.NewEventBus()
	if err := bus.SubscribeAsync("slow", &childSubscriber{}); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(os.Stderr, childInFlightMarker)
		time.Sleep(childHandlerDuration)
		_ = bus.Publish(context.WithoutCancel(r.Context()), "slow")
		_, _ = io.WriteString(w, "completed")
	})

	NewServerApp(bootstrapperFunc(func(c core.IContainer) {
		_ = c.Singleton(func() core.Router { return &stubRouter{handler: mux} })
		_ = c.Singleton(func() core.IEventBus { return bus })
	})).Run()

	fmt.Fprintln(os.Stderr, "CHILD: Run returned")
}

// childProcess is a running child and everything it has written to stdout and stderr.
type childProcess struct {
	cmd  *exec.Cmd
	port int

	mu     sync.Mutex
	output strings.Builder
	marks  map[string]chan struct{}
	done   chan struct{}
}

func startChild(t *testing.T, extraConfig string) *childProcess {
	t.Helper()

	port := freePort(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("MODE=dev\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o700))
	config := fmt.Sprintf("app:\n  server:\n    port: %d\n%s", port, extraConfig)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "config.yaml"), []byte(config), 0o600))

	cmd := exec.Command(os.Args[0], "-test.run=^TestServerAppChildProcess$", "-test.v")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), childEnv+"=1")

	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer

	child := &childProcess{
		cmd:  cmd,
		port: port,
		marks: map[string]chan struct{}{
			childInFlightMarker:   make(chan struct{}),
			childSubscriberMarker: make(chan struct{}),
		},
		done: make(chan struct{}),
	}

	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := scanner.Text()
			child.mu.Lock()
			child.output.WriteString(line + "\n")
			for mark, seen := range child.marks {
				if strings.Contains(line, mark) {
					close(seen)
					delete(child.marks, mark)
				}
			}
			child.mu.Unlock()
		}
	}()

	require.NoError(t, cmd.Start())
	go func() {
		_ = cmd.Wait()
		_ = writer.Close()
		close(child.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-child.done
	})

	child.waitUntilServing(t)
	return child
}

func (c *childProcess) waitUntilServing(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-c.done:
			t.Fatalf("the child exited before serving:\n%s", c.log())
		default:
		}
		resp, err := http.Get(c.url("/ping"))
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the child never started serving:\n%s", c.log())
}

func (c *childProcess) url(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", c.port, path)
}

func (c *childProcess) log() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.String()
}

func (c *childProcess) mark(name string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seen, ok := c.marks[name]; ok {
		return seen
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

func (c *childProcess) waitForExit(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(within):
		t.Fatalf("the child did not exit within %s of the signal:\n%s", within, c.log())
	}
	return c.cmd.ProcessState.ExitCode()
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// TestSigtermDuringARequestLetsItFinishAndExitsZero is the case the old Run got wrong.
// Shutdown makes ListenAndServe return http.ErrServerClosed at once, while Shutdown
// itself is still waiting for the request. Run's serving goroutine passed that error to
// Panicf, and a panic on any goroutine ends the process: exit status 2, with the request
// cut off.
func TestSigtermDuringARequestLetsItFinishAndExitsZero(t *testing.T) {
	child := startChild(t, "")

	result := get(child.url("/slow"))
	select {
	case <-child.mark(childInFlightMarker):
	case <-time.After(5 * time.Second):
		t.Fatalf("the request never reached the handler:\n%s", child.log())
	}

	require.NoError(t, child.cmd.Process.Signal(syscall.SIGTERM))

	got := <-result
	require.NoError(t, got.err, "the in-flight request must get its response:\n%s", child.log())
	assert.Equal(t, http.StatusOK, got.status)
	assert.Equal(t, "completed", got.body)

	code := child.waitForExit(t, 10*time.Second)
	output := child.log()
	assert.Equal(t, 0, code, "a normal shutdown must exit 0:\n%s", output)
	assert.NotContains(t, output, "panic")
	assert.NotContains(t, output, http.ErrServerClosed.Error(),
		"ErrServerClosed is how every normal shutdown ends, not an error to report")
	assert.Contains(t, output, childSubscriberMarker,
		"the async subscriber the request started must be drained before the process exits")
	assert.Contains(t, output, "CHILD: Run returned")
}

// TestSigtermWaitsNoLongerThanTheShutdownTimeout pins the deadline: a request that
// outlives app.server.timeout.shutdown is abandoned, and the process still exits, rather
// than waiting for as long as the handler cares to run.
func TestSigtermWaitsNoLongerThanTheShutdownTimeout(t *testing.T) {
	child := startChild(t, "    timeout:\n      shutdown: 200ms\n")

	result := get(child.url("/slow"))
	select {
	case <-child.mark(childInFlightMarker):
	case <-time.After(5 * time.Second):
		t.Fatalf("the request never reached the handler:\n%s", child.log())
	}

	signalled := time.Now()
	require.NoError(t, child.cmd.Process.Signal(syscall.SIGTERM))

	code := child.waitForExit(t, childHandlerDuration)
	elapsed := time.Since(signalled)
	output := child.log()

	assert.Less(t, elapsed, childHandlerDuration,
		"the process must exit at the deadline, not when the handler finishes")
	assert.NotEqual(t, 0, code, "a shutdown that abandoned requests is not a clean exit:\n%s", output)
	assert.NotContains(t, output, "panic")
	assert.Contains(t, output, context.DeadlineExceeded.Error())

	got := <-result
	assert.True(t, got.err != nil || got.body != "completed",
		"the abandoned request cannot have completed")
	var netErr net.Error
	if got.err != nil && errors.As(got.err, &netErr) {
		assert.False(t, netErr.Timeout(), "the connection is closed, not left to time out")
	}
}
