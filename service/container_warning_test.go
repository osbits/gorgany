package service

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T4.4 asks for a warning when a core interface is rebound. Asserting the warning
// actually fires needs a logger we can read back, so this file installs a capturing
// one.
//
// log.SetLoggerFactory panics if called twice, so it is installed once per test
// binary and the buffer is drained per test instead.
//
// The factory here returns a logger *directly*, which is why these tests could never
// have caught the deadlock the same warning line used to cause. The real factory —
// provider.LoggerProvider's — resolves core.Logger back out of the container, and the
// warning was emitted while bind held the container's write lock. See
// service/containerlog for the tests that install a container-backed factory; they live
// in their own package because this file has already claimed the process's one
// SetLoggerFactory slot.

type capturingLogger struct {
	mu    *sync.Mutex
	lines *[]string
}

func (l capturingLogger) record(level, format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.lines = append(*l.lines, level+" "+fmt.Sprintf(format, v...))
}

func (l capturingLogger) SetPrefix(string)          {}
func (l capturingLogger) Info(v ...any)             { l.record("INFO", "%v", v) }
func (l capturingLogger) Infof(f string, v ...any)  { l.record("INFO", f, v...) }
func (l capturingLogger) Warn(v ...any)             { l.record("WARN", "%v", v) }
func (l capturingLogger) Warnf(f string, v ...any)  { l.record("WARN", f, v...) }
func (l capturingLogger) Error(v ...any)            { l.record("ERROR", "%v", v) }
func (l capturingLogger) Errorf(f string, v ...any) { l.record("ERROR", f, v...) }
func (l capturingLogger) Panic(v ...any)            { panic(fmt.Sprint(v...)) }
func (l capturingLogger) Panicf(f string, v ...any) { panic(fmt.Sprintf(f, v...)) }
func (l capturingLogger) Engine() any               { return nil }

var (
	capturedMu    sync.Mutex
	capturedLines []string
	installOnce   sync.Once
)

// captureLogs installs the capturing factory once and clears the buffer, returning
// a reader for whatever the code under test logs.
func captureLogs(t *testing.T) func() []string {
	t.Helper()

	installOnce.Do(func() {
		log.SetLoggerFactory(func(string) core.Logger {
			return capturingLogger{mu: &capturedMu, lines: &capturedLines}
		})
	})

	capturedMu.Lock()
	capturedLines = nil
	capturedMu.Unlock()

	return func() []string {
		capturedMu.Lock()
		defer capturedMu.Unlock()
		return append([]string{}, capturedLines...)
	}
}

// TestRebindingACoreInterfaceWarns is the T4.4 assertion that was missing: the fix
// was implemented and the rebind-still-works behaviour was tested, but nothing
// checked that the warning — the entire point of the change — actually fires.
func TestRebindingACoreInterfaceWarns(t *testing.T) {
	read := captureLogs(t)

	c := NewContainer()
	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "framework"}
	}))

	// Nothing logged yet: the first binding is not a rebind.
	assert.Empty(t, warnings(read()), "the first registration must not warn")

	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "override"}
	}))

	warns := warnings(read())
	require.Len(t, warns, 1, "rebinding a core interface must warn exactly once")
	assert.Contains(t, warns[0], "IValidator", "the warning must name the type")
	assert.Contains(t, warns[0], "register",
		"the warning must state the supported pattern")
	assert.Contains(t, warns[0], "last")
}

// TestRebindingANonCoreTypeDoesNotWarn pins the scoping: an app rebinding its own
// service is ordinary and must stay quiet, or the warning becomes noise nobody reads.
func TestRebindingANonCoreTypeDoesNotWarn(t *testing.T) {
	read := captureLogs(t)

	c := NewContainer()
	require.NoError(t, c.SingletonLazy(func() *appService { return &appService{id: "first"} }))
	require.NoError(t, c.SingletonLazy(func() *appService { return &appService{id: "second"} }))

	assert.Empty(t, warnings(read()),
		"rebinding an app's own type must not warn")
}

// TestNamedRebindWarnsAndNamesTheBinding
func TestNamedRebindWarnsAndNamesTheBinding(t *testing.T) {
	read := captureLogs(t)

	c := NewContainer()
	require.NoError(t, c.NamedSingletonLazy("strict", func() core.IValidator {
		return &frameworkValidator{id: "a"}
	}))
	require.NoError(t, c.NamedSingletonLazy("strict", func() core.IValidator {
		return &frameworkValidator{id: "b"}
	}))

	warns := warnings(read())
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], "[strict]", "a named rebind must identify the name")
}

// TestBindingTheSameTypeUnderDifferentNamesDoesNotWarn
func TestBindingTheSameTypeUnderDifferentNamesDoesNotWarn(t *testing.T) {
	read := captureLogs(t)

	c := NewContainer()
	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &frameworkValidator{id: "unnamed"}
	}))
	require.NoError(t, c.NamedSingletonLazy("strict", func() core.IValidator {
		return &frameworkValidator{id: "named"}
	}))

	assert.Empty(t, warnings(read()),
		"(type, name) pairs are distinct bindings, not a rebind")
}

func warnings(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(l, "WARN ") {
			out = append(out, l)
		}
	}
	return out
}
