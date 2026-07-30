package noi18nconfig_test

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

// TestNoWarningWithoutI18nConfig. H5 reports a skipped `validation.*` lookup so a server app
// whose translations are being silently ignored finds out. An app that never configured i18n
// is not that app — it is a CLI tool, or a service that never needed translations — and
// telling it about a feature it is not using is the kind of warning that trains people to
// ignore warnings.
func TestNoWarningWithoutI18nConfig(t *testing.T) {
	require.False(t, i18n.HasManager())
	require.False(t, viper.IsSet("i18n"),
		"this package exists to assert the unconfigured path")

	logged := captureTheLog(t)

	// Several failures, because the point is that none of them says anything — not that the
	// first one is swallowed and later ones are not.
	for i := 0; i < 3; i++ {
		require.Error(t, validator.New().ValidateStruct(struct {
			Name string `json:"name" validate:"required"`
		}{}))
	}

	assert.Empty(t, logged.text())
}

// TestTheEnglishDefaultsStillApply — silence must not mean a worse message.
func TestTheEnglishDefaultsStillApply(t *testing.T) {
	err := validator.New().ValidateStruct(struct {
		Name string `json:"name" validate:"required"`
	}{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
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

// captureTheLog installs the factory before anything can log, which is why this package must
// contain nothing that provokes the warning first.
func captureTheLog(t *testing.T) *logCapture {
	t.Helper()

	captured := &logCapture{}
	grglog.SetLoggerFactory(func(string) core.Logger {
		return capturingLogger{c: captured}
	})
	return captured
}
