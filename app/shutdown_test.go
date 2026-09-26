package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/event"
	"github.com/osbits/gorgany/v2/job"
	"github.com/osbits/gorgany/v2/service"
)

type bootstrapperFunc func(core.IContainer)

func (f bootstrapperFunc) Bootstrap(c core.IContainer) { f(c) }

// stubRouter satisfies core.Router for ServerApp.Run, which only serves through it.
type stubRouter struct {
	core.Router
	handler http.Handler
}

func (r *stubRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.handler.ServeHTTP(w, req)
}

// shutdownSteps records the order in which the parts of a shutdown finished.
type shutdownSteps struct {
	mu    sync.Mutex
	steps []string
}

func (s *shutdownSteps) record(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = append(s.steps, step)
}

func (s *shutdownSteps) indexOf(step string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, recorded := range s.steps {
		if recorded == step {
			return i
		}
	}
	return -1
}

type recordingSubscriber struct {
	steps *shutdownSteps
	delay time.Duration
}

func (r *recordingSubscriber) Handle(context.Context) {
	time.Sleep(r.delay)
	r.steps.record("async subscriber finished")
}

// fakeDataSource counts Close calls.
type fakeDataSource struct {
	dbCore.IDataSource
	closed  atomic.Int32
	err     error
	onClose func()
}

func (f *fakeDataSource) Close() error {
	f.closed.Add(1)
	if f.onClose != nil {
		f.onClose()
	}
	return f.err
}

// runningServer is a ServerApp serving on a loopback port through serve().
type runningServer struct {
	app    *ServerApp
	url    string
	stop   context.CancelFunc
	served chan error
}

func startServing(t *testing.T, s *ServerApp, handler http.Handler, timeout time.Duration) *runningServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s.httpServer = &http.Server{Handler: handler}
	stop, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	running := &runningServer{
		app:    s,
		url:    "http://" + listener.Addr().String(),
		stop:   cancel,
		served: make(chan error, 1),
	}
	go func() {
		running.served <- s.serve(stop, listener, timeout)
	}()
	return running
}

func (r *runningServer) waitServed(t *testing.T, within time.Duration) error {
	t.Helper()
	select {
	case err := <-r.served:
		return err
	case <-time.After(within):
		t.Fatalf("serve did not return within %s", within)
		return nil
	}
}

func get(url string) <-chan slowResult {
	result := make(chan slowResult, 1)
	go func() {
		resp, err := http.Get(url)
		if err != nil {
			result <- slowResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		result <- slowResult{status: resp.StatusCode, body: string(body), err: err}
	}()
	return result
}

type slowResult struct {
	status int
	body   string
	err    error
}

// TestShutdownDrainsRequestsThenJobsThenEventsThenClosesDatasources walks the whole
// sequence: the in-flight request completes, then the running job's context is
// cancelled, then the async subscriber the request started is waited for, and only then
// are the datasources closed.
func TestShutdownDrainsRequestsThenJobsThenEventsThenClosesDatasources(t *testing.T) {
	steps := &shutdownSteps{}

	scheduler := job.NewScheduler()
	jobStarted := make(chan struct{})
	require.NoError(t, scheduler.Add("blocking", core.JobSchedule{Every: time.Hour, RunAtStartup: true},
		func(ctx context.Context) error {
			close(jobStarted)
			<-ctx.Done()
			steps.record("job cancelled")
			return ctx.Err()
		}))
	require.NoError(t, scheduler.Start(context.Background()))
	<-jobStarted

	bus := event.NewEventBus()
	require.NoError(t, bus.SubscribeAsync("done", &recordingSubscriber{steps: steps, delay: 200 * time.Millisecond}))

	dataSource := &fakeDataSource{onClose: func() { steps.record("datasources closed") }}
	dbContext := &db.DBContext{}
	dbContext.RegisterDataSource("default", dataSource)

	inFlight := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inFlight)
		time.Sleep(300 * time.Millisecond)
		_ = bus.Publish(context.WithoutCancel(r.Context()), "done")
		steps.record("request completed")
		_, _ = io.WriteString(w, "completed")
	})

	server := startServing(t, &ServerApp{scheduler: scheduler, eventBus: bus, dbContext: dbContext},
		handler, 5*time.Second)
	result := get(server.url + "/")
	<-inFlight
	server.stop()

	got := <-result
	require.NoError(t, got.err, "the in-flight request must get its response")
	assert.Equal(t, http.StatusOK, got.status)
	assert.Equal(t, "completed", got.body)

	require.NoError(t, server.waitServed(t, 5*time.Second))

	for _, step := range []string{"request completed", "job cancelled", "async subscriber finished", "datasources closed"} {
		require.NotEqual(t, -1, steps.indexOf(step), "%q must have happened before serve returned", step)
	}
	assert.Less(t, steps.indexOf("request completed"), steps.indexOf("job cancelled"),
		"jobs are stopped after the requests have drained")
	assert.Less(t, steps.indexOf("async subscriber finished"), steps.indexOf("datasources closed"),
		"a subscriber must not find its datasource closed")
	assert.Less(t, steps.indexOf("job cancelled"), steps.indexOf("datasources closed"),
		"a job must not find its datasource closed")
	assert.Equal(t, int32(1), dataSource.closed.Load())
}

// TestShutdownGivesUpAtTheDeadline: a request that outlives the timeout is cut off, what
// could not be waited for is named, and the datasources are left open because something
// may still be using them.
func TestShutdownGivesUpAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	inFlight := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(inFlight)
		<-release
		_, _ = io.WriteString(w, "completed")
	})

	dataSource := &fakeDataSource{}
	dbContext := &db.DBContext{}
	dbContext.RegisterDataSource("default", dataSource)

	server := startServing(t, &ServerApp{eventBus: event.NewEventBus(), dbContext: dbContext},
		handler, 100*time.Millisecond)
	result := get(server.url + "/")
	<-inFlight

	stopped := time.Now()
	server.stop()
	err := server.waitServed(t, 2*time.Second)

	assert.Less(t, time.Since(stopped), time.Second, "serve must return at the deadline")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), ConfigShutdownTimeout, "the error must name the setting to change")
	assert.Contains(t, err.Error(), "http server: context deadline exceeded")
	assert.Contains(t, err.Error(), "event bus: not waited for")

	got := <-result
	assert.Error(t, got.err, "the connection of an abandoned request is closed, not left open")
	assert.Equal(t, int32(0), dataSource.closed.Load())
}

// TestAShutdownFromApplicationCodeIsWaitedFor: Serve returns ErrServerClosed as soon as
// any Shutdown starts. serve must still not return, and let main exit, until that drain
// has finished.
func TestAShutdownFromApplicationCodeIsWaitedFor(t *testing.T) {
	var requestDone atomic.Bool
	inFlight := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(inFlight)
		time.Sleep(300 * time.Millisecond)
		requestDone.Store(true)
		_, _ = io.WriteString(w, "completed")
	})

	server := startServing(t, &ServerApp{}, handler, 5*time.Second)
	result := get(server.url + "/")
	<-inFlight

	shutdownResults := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { shutdownResults <- server.app.Shutdown(context.Background()) }()
	}

	require.NoError(t, server.waitServed(t, 5*time.Second))
	assert.True(t, requestDone.Load(), "serve returned before the drain it was waiting on finished")

	got := <-result
	require.NoError(t, got.err)
	assert.Equal(t, "completed", got.body)

	for i := 0; i < 2; i++ {
		assert.NoError(t, <-shutdownResults, "every caller gets the one drain's result")
	}
}

// TestALaterShutdownCallerStopsWaitingAtItsOwnDeadline: only the first caller runs the
// drain; another returns when its own context is done.
func TestALaterShutdownCallerStopsWaitingAtItsOwnDeadline(t *testing.T) {
	release := make(chan struct{})
	bus := &blockingBus{IEventBus: event.NewEventBus(), release: release, waiting: make(chan struct{})}
	s := &ServerApp{eventBus: bus}

	first := make(chan error, 1)
	go func() { first <- s.Shutdown(context.Background()) }()
	<-bus.waiting

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	assert.ErrorIs(t, s.Shutdown(ctx), context.DeadlineExceeded)

	close(release)
	assert.NoError(t, <-first)
}

// blockingBus holds the drain in WaitAsync until released. Only the first Shutdown caller
// drains, so it is called once.
type blockingBus struct {
	core.IEventBus
	release <-chan struct{}
	waiting chan struct{}
}

func (b *blockingBus) WaitAsync() {
	close(b.waiting)
	<-b.release
}

func TestShutdownWithNothingToStopSucceeds(t *testing.T) {
	assert.NoError(t, (&ServerApp{}).Shutdown(context.Background()))
}

// TestShutdownTargetsAreWhatTheProvidersBound: an application without JobProvider,
// EventProvider or DbProvider has nothing for Shutdown to drain there. The scheduler case
// matters most, because the container constructs an unbound pointer-to-struct type instead
// of failing.
func TestShutdownTargetsAreWhatTheProvidersBound(t *testing.T) {
	t.Run("nothing bound", func(t *testing.T) {
		s := &ServerApp{}
		s.container = service.NewContainer()
		s.resolveShutdownTargets()

		assert.Nil(t, s.scheduler)
		assert.Nil(t, s.eventBus)
		assert.Nil(t, s.dbContext)
	})

	t.Run("bound by the providers", func(t *testing.T) {
		scheduler := job.NewScheduler()
		require.NoError(t, scheduler.Add("noop", core.JobSchedule{Every: time.Hour},
			func(context.Context) error { return nil }))
		bus := event.NewEventBus()
		dbContext := &db.DBContext{}

		c := service.NewContainer()
		require.NoError(t, c.SingletonLazy(func() *job.Scheduler { return scheduler }))
		require.NoError(t, c.SingletonLazy(func() core.IEventBus { return bus }))
		require.NoError(t, c.SingletonLazy(func() core.IDBContext { return dbContext }))

		s := &ServerApp{}
		s.container = c
		s.resolveShutdownTargets()

		assert.Same(t, scheduler, s.scheduler)
		assert.Same(t, bus, s.eventBus)
		assert.Same(t, dbContext, s.dbContext)
	})
}

func TestShutdownTimeoutDefaultsWhenUnsetOrNotPositive(t *testing.T) {
	previous := viper.Get(ConfigShutdownTimeout)
	t.Cleanup(func() { viper.Set(ConfigShutdownTimeout, previous) })

	for configured, want := range map[any]time.Duration{
		nil:     DefaultShutdownTimeout,
		"0s":    DefaultShutdownTimeout,
		"-5s":   DefaultShutdownTimeout,
		"45s":   45 * time.Second,
		"1m":    time.Minute,
		"250ms": 250 * time.Millisecond,
	} {
		viper.Set(ConfigShutdownTimeout, configured)
		assert.Equal(t, want, ShutdownTimeout(), "configured %v", configured)
	}
}
