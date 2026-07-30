package config

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/log"
)

// The resolver's warnings are part of its contract — they are the only place an unresolved
// key is reported once the value is blanked — so asserting on them needs a logger we can
// read back.
//
// log.SetLoggerFactory panics on a second call, so it is installed once per test binary and
// the buffer is drained per test.

type warnCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *warnCapture) record(level, format string, v ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, level+" "+fmt.Sprintf(format, v...))
}

type warnLogger struct{ c *warnCapture }

func (l warnLogger) SetPrefix(string)          {}
func (l warnLogger) Info(v ...any)             { l.c.record("INFO", "%v", v) }
func (l warnLogger) Infof(f string, v ...any)  { l.c.record("INFO", f, v...) }
func (l warnLogger) Warn(v ...any)             { l.c.record("WARN", "%v", v) }
func (l warnLogger) Warnf(f string, v ...any)  { l.c.record("WARN", f, v...) }
func (l warnLogger) Error(v ...any)            { l.c.record("ERROR", "%v", v) }
func (l warnLogger) Errorf(f string, v ...any) { l.c.record("ERROR", f, v...) }
func (l warnLogger) Panic(v ...any)            { panic(fmt.Sprint(v...)) }
func (l warnLogger) Panicf(f string, v ...any) { panic(fmt.Sprintf(f, v...)) }
func (l warnLogger) Engine() any               { return nil }

var (
	warnings    warnCapture
	installOnce sync.Once
)

// captureWarnings returns a reader for the WARN lines the code under test emits.
func captureWarnings(t *testing.T) func() []string {
	t.Helper()

	installOnce.Do(func() {
		log.SetLoggerFactory(func(string) core.Logger { return warnLogger{c: &warnings} })
	})

	warnings.mu.Lock()
	warnings.lines = nil
	warnings.mu.Unlock()

	return func() []string {
		warnings.mu.Lock()
		defer warnings.mu.Unlock()

		out := make([]string, 0, len(warnings.lines))
		for _, line := range warnings.lines {
			if strings.HasPrefix(line, "WARN ") {
				out = append(out, line)
			}
		}
		return out
	}
}
