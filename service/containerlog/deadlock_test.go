package containerlog_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/log"
	"github.com/osbits/gorgany/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// current is the container the installed factory resolves through. Each test points it
// at its own container, because the factory itself can only be installed once per
// process.
var current atomic.Pointer[service.Container]

// installOnce installs a logger factory shaped exactly like the one
// provider.LoggerProvider installs: resolve core.Logger out of the container, and on
// failure bind a default one and return that.
//
// The fallback branch matters as much as the resolve. It calls Singleton, which calls
// bind — so a warning emitted from bind can reach bind again.
var installOnce sync.Once

func installContainerBackedFactory() {
	installOnce.Do(func() {
		log.SetLoggerFactory(func(key string) core.Logger {
			c := current.Load()
			if c == nil {
				return &log.DefaultLogger{}
			}

			var logger core.Logger
			var err error
			if key == "" {
				err = c.Resolve(&logger)
			} else {
				err = c.NamedResolve(&logger, key)
			}
			if err == nil && logger != nil {
				return logger
			}

			fallback := &log.DefaultLogger{}
			_ = c.Singleton(func() core.Logger { return fallback })
			return fallback
		})
	})
}

// containerUnder returns a container wired to the factory for the duration of the test.
func containerUnder(t *testing.T) *service.Container {
	t.Helper()

	installContainerBackedFactory()

	c := service.NewContainer()
	current.Store(c)
	t.Cleanup(func() { current.Store(nil) })
	return c
}

// completesWithin runs fn and reports whether it returned before the deadline.
//
// A deadlock has to be a *failure*, not a hang: left to block, the test would sit until
// `go test` kills the whole binary with no indication of which test was at fault. The
// goroutine is deliberately abandoned on timeout — it is wedged on a mutex and there is
// nothing to cancel.
func completesWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()

	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestRebindingACoreInterfaceDoesNotDeadlock is the regression.
//
// bind took c.mu.Lock() with a deferred unlock and then logged the rebind warning while
// still holding it. log.Log() goes through the factory above, which takes c.mu.RLock();
// sync.RWMutex is not reentrant, so this hung the process every time. Observed in a real
// app as four warnings printed (from providers registered before the factory existed) and
// then a boot that never finished.
func TestRebindingACoreInterfaceDoesNotDeadlock(t *testing.T) {
	c := containerUnder(t)

	require.NoError(t, c.SingletonLazy(func() core.IValidator { return nil }))

	completed := completesWithin(5*time.Second, func() {
		_ = c.SingletonLazy(func() core.IValidator { return nil })
	})

	require.True(t, completed,
		"rebinding a core interface deadlocked: the warning is being logged while the "+
			"container's write lock is held, and the logger resolves through the container")
}

// TestTheWholeProviderOrderingDeadlocks reproduces the app-level shape: a logger is
// registered first, then several core interfaces are rebound — which is what the warning
// itself tells an app to do.
func TestTheWholeProviderOrderingDeadlocks(t *testing.T) {
	c := containerUnder(t)

	// What LoggerProvider.Register does.
	require.NoError(t, c.SingletonLazy(func() core.Logger { return &log.DefaultLogger{} }))

	// What an app's providers then do: bind, then override.
	require.NoError(t, c.SingletonLazy(func() core.IValidator { return nil }))
	require.NoError(t, c.SingletonLazy(func() core.IDataContext { return nil }))
	require.NoError(t, c.SingletonLazy(func() core.IAuthContext { return nil }))

	completed := completesWithin(5*time.Second, func() {
		_ = c.SingletonLazy(func() core.IValidator { return nil })
		_ = c.SingletonLazy(func() core.IDataContext { return nil })
		_ = c.SingletonLazy(func() core.IAuthContext { return nil })
	})

	require.True(t, completed, "a provider-ordering rebind sequence must complete")
}

// TestRebindingLoggerItselfDoesNotRecurse covers the reentrancy the unlock alone does not
// fix.
//
// core.Logger is itself a core interface, so rebinding it warns — and the warning resolves
// core.Logger. With a binding that resolves to nil the factory takes its fallback branch,
// which calls Singleton, which is another rebind of core.Logger, which warns again. Without
// the guard in containerWarnf that recurses until the stack runs out.
func TestRebindingLoggerItselfDoesNotRecurse(t *testing.T) {
	c := containerUnder(t)

	// A logger binding that resolves to nil, so the factory always falls back and always
	// re-binds. An app's own logger provider can produce exactly this.
	require.NoError(t, c.SingletonLazy(func() core.Logger { return nil }))

	completed := completesWithin(5*time.Second, func() {
		_ = c.SingletonLazy(func() core.Logger { return nil })
	})

	require.True(t, completed,
		"rebinding core.Logger recursed: the warning resolves a logger, which re-binds a "+
			"logger, which warns")
}

// TestConcurrentRebindsDoNotDeadlock: bind is safe for concurrent use and the warning path
// must not change that.
func TestConcurrentRebindsDoNotDeadlock(t *testing.T) {
	c := containerUnder(t)
	require.NoError(t, c.SingletonLazy(func() core.Logger { return &log.DefaultLogger{} }))

	completed := completesWithin(10*time.Second, func() {
		var wg sync.WaitGroup
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = c.SingletonLazy(func() core.IValidator { return nil })
			}()
		}
		wg.Wait()
	})

	require.True(t, completed, "concurrent rebinds must not deadlock")
}

// TestTheRebindStillTakesEffect guards against "fixing" the deadlock by dropping the
// rebind, and against the extracted critical section losing the write.
func TestTheRebindStillTakesEffect(t *testing.T) {
	c := containerUnder(t)
	require.NoError(t, c.SingletonLazy(func() core.Logger { return &log.DefaultLogger{} }))

	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &stubValidator{id: "first"}
	}))
	require.NoError(t, c.SingletonLazy(func() core.IValidator {
		return &stubValidator{id: "second"}
	}))

	var resolved core.IValidator
	require.NoError(t, c.Resolve(&resolved))

	stub, ok := resolved.(*stubValidator)
	require.True(t, ok)
	assert.Equal(t, "second", stub.id, "the last registration must win")
}

// TestResolutionStillWorksAfterAWarning: the diagnostic must leave the container usable,
// which a half-released lock would not.
func TestResolutionStillWorksAfterAWarning(t *testing.T) {
	c := containerUnder(t)
	require.NoError(t, c.SingletonLazy(func() core.Logger { return &log.DefaultLogger{} }))

	require.NoError(t, c.SingletonLazy(func() core.IValidator { return &stubValidator{id: "a"} }))
	require.NoError(t, c.SingletonLazy(func() core.IValidator { return &stubValidator{id: "b"} }))

	completed := completesWithin(5*time.Second, func() {
		var logger core.Logger
		_ = c.Resolve(&logger)

		var validator core.IValidator
		_ = c.Resolve(&validator)
	})

	require.True(t, completed, "the container must still be resolvable after a warning")
}

// stubValidator is a core.IValidator we can tell apart by id.
type stubValidator struct {
	core.IValidator
	id string
}
