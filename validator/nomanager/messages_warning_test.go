package nomanager_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/i18n"
	grglog "github.com/osbits/gorgany/v2/log"
	"github.com/osbits/gorgany/v2/validator"
)

// H5. The skip in validator.message when no i18n manager is installed is deliberate: a CLI
// app validates its command DTOs without booting i18n, and panicking inside GetManager would
// turn a bad flag into a crash. What was missing was any way to distinguish that case from
// the one that looks identical and is a bug — a server app that ships `validation.*` keys and
// never installs the manager. Its translations are silently ignored, every message comes back
// in the framework's English, and the only symptom is that the overrides appear not to work.
// Nothing was emitted, so there was nothing to grep for.
//
// These tests live in this package for the same reason the ones next door do: the assertion
// is about the *absence* of a manager, and i18n.SetManager panics on a second call, so it
// cannot be arranged in a package that installs one.

// The "silent when i18n is not configured" half of this lives in validator/noi18nconfig,
// not here. The warning fires once per process, and this package provokes it deliberately, so
// an assertion that nothing was logged would pass here for the wrong reason.
func TestTheSkippedLookupIsReportedWhenI18nIsConfigured(t *testing.T) {
	require.False(t, i18n.HasManager(),
		"this package exists to assert the un-booted path")

	logged := captureTheLog(t)
	withI18nConfigured(t)

	err := validator.New().ValidateStruct(struct {
		Name string `json:"name" validate:"required"`
	}{})
	require.Error(t, err)

	text := logged.text()
	assert.Contains(t, text, "no manager is installed",
		"a server app whose translations are being ignored must be told")
	assert.Contains(t, text, "validation.",
		"the message names the keys that could not be read")
	assert.Contains(t, text, "I18nProvider",
		"and the remedy, which is the whole point of warning")
}

// TestTheWarningFiresAtMostOncePerProcess. A validation failure is request-driven, so a line
// per rejected field is a log flood any client could trigger by posting an empty body in a
// loop.
//
// It runs after the test above in file order, and sync.Once is process-wide, so this asserts
// the second-and-later calls stay quiet — which is the property that matters.
func TestTheWarningFiresAtMostOncePerProcess(t *testing.T) {
	withI18nConfigured(t)

	// Drain whatever the earlier tests left, then provoke several more failures.
	_ = validator.New().ValidateStruct(struct {
		Name string `json:"name" validate:"required"`
	}{})

	logged := captureTheLog(t)
	for i := 0; i < 5; i++ {
		_ = validator.New().ValidateStruct(struct {
			Name string `json:"name" validate:"required"`
		}{})
	}

	assert.Empty(t, logged.text(),
		"the warning must not repeat once it has been emitted")
}

// TestTheEnglishDefaultsStillApply — the warning must be a diagnostic, not a behaviour
// change. The message a client receives is unchanged.
func TestTheEnglishDefaultsStillApply(t *testing.T) {
	withI18nConfigured(t)

	err := validator.New().ValidateStruct(struct {
		Name string `json:"name" validate:"required"`
	}{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

// ------------------------------------------------------------------ helpers

func withI18nConfigured(t *testing.T) {
	t.Helper()

	previous := viper.Get("i18n")
	viper.Set("i18n", map[string]any{"lang": map[string]any{"default": "en"}})
	t.Cleanup(func() { viper.Set("i18n", previous) })
}

// ------------------------------------------------------------------ log capture

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
