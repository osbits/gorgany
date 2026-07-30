package auth

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	grglog "github.com/osbits/gorgany/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H4. ClearExpiredSessions had no caller anywhere in the framework — the job that was
// supposed to call it was never registered — so nothing exercised it and two races sat in
// the path unnoticed:
//
//   - MemorySession.ClearExpiredSessions ranged over the map with no lock held, taking the
//     lock only around each individual delete. Every other method on the type locks
//     correctly. A concurrent map iteration and write is a runtime *fatal* error, not a
//     torn value: RecoveryMiddleware cannot catch it.
//   - Session.expiry was written unguarded by SetExpiry, which SessionMiddleware calls on
//     every request, and read unguarded by the sweep's IsExpired.
//
// Scheduling the sweep is what makes both reachable, so they are fixed and pinned here in
// the same change. These tests are only meaningful under -race.

func TestSweepingWhileSessionsAreAddedIsRaceFree(t *testing.T) {
	storage := NewMemorySession(time.Hour)

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			storage.AddSession(NewSession(string(rune('a'+i%26)), time.Now().Add(time.Hour)))
		}
	}()

	// SetExpiry on every request is what SessionMiddleware does, and it is the write the
	// sweep's IsExpired raced against.
	live := NewSession("live", time.Now().Add(time.Hour))
	storage.AddSession(live)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			live.SetExpiry(time.Now().Add(time.Hour))
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			storage.ClearExpiredSessions()
		}
	}()

	wg.Wait()
}

// TestTheSweepRemovesOnlyExpiredSessions — the locking fix moved the deletes inside the
// iteration, so it is worth pinning that it still deletes the right rows.
func TestTheSweepRemovesOnlyExpiredSessions(t *testing.T) {
	storage := NewMemorySession(time.Hour)

	storage.AddSession(NewSession("fresh", time.Now().Add(time.Hour)))
	storage.AddSession(NewSession("stale", time.Now().Add(-time.Hour)))

	storage.ClearExpiredSessions()

	assert.NotNil(t, storage.GetSessionById("fresh"))
	assert.Nil(t, storage.GetSessionById("stale"))
}

// TestExpiryIsReadableWhileItIsWritten is the field-level half. A time.Time is three words,
// so an unguarded read could see a value that never existed — expiring a live session or
// keeping a dead one.
func TestExpiryIsReadableWhileItIsWritten(t *testing.T) {
	session := NewSession("s", time.Now().Add(time.Hour))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			session.SetExpiry(time.Now().Add(time.Duration(i) * time.Second))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = session.IsExpired()
			_ = session.GetExpiry()
		}
	}()
	wg.Wait()
}

// TestADbSweepFailureIsReported. The error used to be assigned and dropped with `_ = err`,
// alone in a file where every other method reports through grgErr.HandleError — so a sweep
// that failed every time looked exactly like one that worked, and the only symptom was the
// table growing, which is also the symptom of the sweep not running.
//
// The assertion is on what reaches the log, not merely that the call happened: "the
// repository was asked" is true whether or not the failure was reported, which is what made
// the first version of this test pass against the discarded error.
func TestADbSweepFailureIsReported(t *testing.T) {
	logged := captureTheLog(t)

	failing := &failingSessionRepository{}
	storage := &DbSessionStorage{mediator: NewDbSessionMediator(failing)}

	require.NotPanics(t, func() { storage.ClearExpiredSessions() })
	require.True(t, failing.clearCalled, "the sweep must reach the repository")

	assert.Contains(t, logged.text(), "expired sessions",
		"a sweep that failed must not look like one that worked")
	assert.Contains(t, logged.text(), assert.AnError.Error(),
		"the underlying cause has to survive to the log")
}

// TestASuccessfulSweepIsQuiet — the report must be the failure, not every sweep. A line per
// interval per process is noise that trains people to ignore it.
func TestASuccessfulSweepIsQuiet(t *testing.T) {
	logged := captureTheLog(t)

	storage := &DbSessionStorage{mediator: NewDbSessionMediator(&succeedingSessionRepository{})}
	storage.ClearExpiredSessions()

	assert.Empty(t, logged.text())
}

// ------------------------------------------------------------------- log capture
//
// Installed once per test binary: log.SetLoggerFactory panics on a second call. The `auth`
// package had not claimed its slot. Same shape as http/body_not_logged_test.go.

type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *logCapture) record(format string, v ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, v...))
}

func (c *logCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

func (c *logCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = nil
}

type capturingLogger struct{ c *logCapture }

func (l capturingLogger) SetPrefix(string)          {}
func (l capturingLogger) Info(v ...any)             { l.c.record("%v", v) }
func (l capturingLogger) Infof(f string, v ...any)  { l.c.record(f, v...) }
func (l capturingLogger) Warn(v ...any)             { l.c.record("%v", v) }
func (l capturingLogger) Warnf(f string, v ...any)  { l.c.record(f, v...) }
func (l capturingLogger) Error(v ...any)            { l.c.record("%v", v) }
func (l capturingLogger) Errorf(f string, v ...any) { l.c.record(f, v...) }
func (l capturingLogger) Panic(v ...any)            { panic(fmt.Sprint(v...)) }
func (l capturingLogger) Panicf(f string, v ...any) { panic(fmt.Sprintf(f, v...)) }
func (l capturingLogger) Engine() any               { return nil }

var (
	captured    logCapture
	installOnce sync.Once
)

func captureTheLog(t *testing.T) *logCapture {
	t.Helper()

	installOnce.Do(func() {
		grglog.SetLoggerFactory(func(string) core.Logger {
			return capturingLogger{c: &captured}
		})
	})

	captured.reset()
	return &captured
}

// succeedingSessionRepository is the quiet case.
type succeedingSessionRepository struct{ ISessionRepository }

func (r *succeedingSessionRepository) DeleteExpired() error { return nil }

// failingSessionRepository fails the delete the mediator issues, and nothing else.
type failingSessionRepository struct {
	ISessionRepository
	clearCalled bool
}

func (r *failingSessionRepository) DeleteExpired() error {
	r.clearCalled = true
	return assert.AnError
}

var _ core.ISessionStorage = (*MemorySession)(nil)
