package job

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tick is the interval these tests schedule at. Short enough to keep the suite
// fast, long enough that a loaded machine still sees several ticks.
const tick = 30 * time.Millisecond

// eventually polls until cond holds or the deadline passes. Scheduling is
// inherently time-based; polling for the outcome beats sleeping for a fixed span and
// hoping.
func eventually(t *testing.T, within time.Duration, cond func() bool, msg string) {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition never held within %s: %s", within, msg)
}

// TestAJobActuallyFires is the test whose absence let a whole subsystem ship
// broken. No scheduled job in any gorgany app had ever run: gocron.Every()
// registered on gocron's package-level default scheduler while JobProvider started
// a separate zero-value one.
func TestAJobActuallyFires(t *testing.T) {
	var hits int32

	s := NewScheduler()
	require.NoError(t, s.Add("counter", core.JobSchedule{Every: tick}, func(context.Context) error {
		atomic.AddInt32(&hits, 1)
		return nil
	}))

	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(s.Stop)

	eventually(t, 2*time.Second,
		func() bool { return atomic.LoadInt32(&hits) >= 3 },
		"a job scheduled every 30ms must fire repeatedly")
}

// TestRunAtStartupFiresImmediately covers the operator choice the brief asked for:
// run now, or wait a full interval.
func TestRunAtStartupFiresImmediately(t *testing.T) {
	var withStartup, withoutStartup int32

	s := NewScheduler()
	require.NoError(t, s.Add("eager", core.JobSchedule{Every: time.Hour, RunAtStartup: true},
		func(context.Context) error { atomic.AddInt32(&withStartup, 1); return nil }))
	require.NoError(t, s.Add("patient", core.JobSchedule{Every: time.Hour},
		func(context.Context) error { atomic.AddInt32(&withoutStartup, 1); return nil }))

	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(s.Stop)

	eventually(t, time.Second,
		func() bool { return atomic.LoadInt32(&withStartup) == 1 },
		"RunAtStartup must fire without waiting for the interval")

	// The hour-interval job with no startup run must not have fired.
	assert.Zero(t, atomic.LoadInt32(&withoutStartup),
		"without RunAtStartup the first run waits a full interval")
}

// TestOverlapIsPreventedByDefault: a job that runs longer than its interval must not
// pile up copies of itself.
func TestOverlapIsPreventedByDefault(t *testing.T) {
	var concurrent, maxConcurrent int32

	release := make(chan struct{})

	s := NewScheduler()
	require.NoError(t, s.Add("slow", core.JobSchedule{Every: tick}, func(context.Context) error {
		current := atomic.AddInt32(&concurrent, 1)
		for {
			observed := atomic.LoadInt32(&maxConcurrent)
			if current <= observed || atomic.CompareAndSwapInt32(&maxConcurrent, observed, current) {
				break
			}
		}
		<-release
		atomic.AddInt32(&concurrent, -1)
		return nil
	}))

	require.NoError(t, s.Start(context.Background()))

	// Let several ticks elapse while the first run is still blocked.
	eventually(t, time.Second, func() bool {
		_, skipped, _ := s.Stats("slow")
		return skipped >= 2
	}, "ticks arriving during a run must be skipped")

	assert.Equal(t, int32(1), atomic.LoadInt32(&maxConcurrent),
		"at most one run may be in flight when AllowOverlap is false")

	close(release)
	s.Stop()
}

// TestAllowOverlapPermitsConcurrentRuns is the opt-in half.
func TestAllowOverlapPermitsConcurrentRuns(t *testing.T) {
	var concurrent, maxConcurrent int32
	release := make(chan struct{})

	s := NewScheduler()
	require.NoError(t, s.Add("parallel",
		core.JobSchedule{Every: tick, AllowOverlap: true},
		func(context.Context) error {
			current := atomic.AddInt32(&concurrent, 1)
			for {
				observed := atomic.LoadInt32(&maxConcurrent)
				if current <= observed || atomic.CompareAndSwapInt32(&maxConcurrent, observed, current) {
					break
				}
			}
			<-release
			atomic.AddInt32(&concurrent, -1)
			return nil
		}))

	require.NoError(t, s.Start(context.Background()))

	eventually(t, 2*time.Second,
		func() bool { return atomic.LoadInt32(&maxConcurrent) >= 2 },
		"AllowOverlap must let a second run start")

	close(release)
	s.Stop()
}

// TestContextIsCancelledOnStop is the shutdown behaviour gocron could not provide:
// a long-running job gets told to stop instead of being abandoned.
func TestContextIsCancelledOnStop(t *testing.T) {
	observed := make(chan error, 1)

	s := NewScheduler()
	require.NoError(t, s.Add("longrunning",
		core.JobSchedule{Every: time.Hour, RunAtStartup: true},
		func(ctx context.Context) error {
			<-ctx.Done()
			observed <- ctx.Err()
			return ctx.Err()
		}))

	require.NoError(t, s.Start(context.Background()))

	// Stop must cancel the in-flight run and wait for it to return.
	done := make(chan struct{})
	go func() { s.Stop(); close(done) }()

	select {
	case err := <-observed:
		assert.ErrorIs(t, err, context.Canceled,
			"the job's context must be cancelled at shutdown")
	case <-time.After(2 * time.Second):
		t.Fatal("the job's context was never cancelled")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not wait for the in-flight run")
	}
}

// TestCancellingTheParentContextStopsTheScheduler
func TestCancellingTheParentContextStopsTheScheduler(t *testing.T) {
	var hits int32

	ctx, cancel := context.WithCancel(context.Background())

	s := NewScheduler()
	require.NoError(t, s.Add("counter", core.JobSchedule{Every: tick},
		func(context.Context) error { atomic.AddInt32(&hits, 1); return nil }))
	require.NoError(t, s.Start(ctx))

	eventually(t, time.Second, func() bool { return atomic.LoadInt32(&hits) >= 1 }, "job must start")

	cancel()
	s.Stop()

	settled := atomic.LoadInt32(&hits)
	time.Sleep(4 * tick)
	assert.Equal(t, settled, atomic.LoadInt32(&hits),
		"no further runs may happen after the parent context is cancelled")
}

// TestAPanickingJobDoesNotKillItsSchedule: the old recovery wrapper was dead code
// because nothing ran. It must be live now.
func TestAPanickingJobDoesNotKillItsSchedule(t *testing.T) {
	var hits int32

	s := NewScheduler()
	require.NoError(t, s.Add("panicky", core.JobSchedule{Every: tick},
		func(context.Context) error {
			atomic.AddInt32(&hits, 1)
			panic("boom")
		}))

	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(s.Stop)

	eventually(t, 2*time.Second,
		func() bool { return atomic.LoadInt32(&hits) >= 3 },
		"a panicking job must keep being scheduled, not take its goroutine down")
}

// TestAFailingJobKeepsItsSchedule
func TestAFailingJobKeepsItsSchedule(t *testing.T) {
	var hits int32

	s := NewScheduler()
	require.NoError(t, s.Add("failing", core.JobSchedule{Every: tick},
		func(context.Context) error {
			atomic.AddInt32(&hits, 1)
			return errors.New("nope")
		}))

	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(s.Stop)

	eventually(t, 2*time.Second,
		func() bool { return atomic.LoadInt32(&hits) >= 3 },
		"a returned error is logged, not a reason to unschedule")
}

// ------------------------------------------------------------ registration guards

// TestAnInvalidScheduleIsRejectedAtRegistration: a job that would never run must
// fail loudly at boot rather than be silently dropped — the whole failure mode this
// subsystem had.
func TestAnInvalidScheduleIsRejectedAtRegistration(t *testing.T) {
	s := NewScheduler()

	err := s.Add("zero", core.JobSchedule{}, func(context.Context) error { return nil })
	require.Error(t, err)
	assert.ErrorIs(t, err, core.ErrJobScheduleMissingInterval)
	assert.Contains(t, err.Error(), "zero", "the error must name the job")

	err = s.Add("negative", core.JobSchedule{Every: -time.Second},
		func(context.Context) error { return nil })
	require.Error(t, err)
}

func TestAddRejectsMissingNameOrWork(t *testing.T) {
	s := NewScheduler()

	require.Error(t, s.Add("", core.JobSchedule{Every: tick}, func(context.Context) error { return nil }))
	require.Error(t, s.Add("noop", core.JobSchedule{Every: tick}, nil))
}

func TestAddRejectsDuplicateNames(t *testing.T) {
	s := NewScheduler()
	run := func(context.Context) error { return nil }

	require.NoError(t, s.Add("dup", core.JobSchedule{Every: tick}, run))
	err := s.Add("dup", core.JobSchedule{Every: tick}, run)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already registered")
}

// TestAddAfterStartIsRejected: a job registered into a running scheduler would never
// be ticked, which is precisely the silent failure this type exists to end.
func TestAddAfterStartIsRejected(t *testing.T) {
	s := NewScheduler()
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(s.Stop)

	err := s.Add("late", core.JobSchedule{Every: tick}, func(context.Context) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after Start")
}

func TestStartTwiceIsRejected(t *testing.T) {
	s := NewScheduler()
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(s.Stop)

	require.Error(t, s.Start(context.Background()))
}

func TestJobsAndStats(t *testing.T) {
	s := NewScheduler()
	require.NoError(t, s.Add("a", core.JobSchedule{Every: time.Hour}, func(context.Context) error { return nil }))
	require.NoError(t, s.Add("b", core.JobSchedule{Every: time.Hour}, func(context.Context) error { return nil }))

	assert.Equal(t, []string{"a", "b"}, s.Jobs())

	_, _, found := s.Stats("a")
	assert.True(t, found)
	_, _, found = s.Stats("nonexistent")
	assert.False(t, found)
}

// TestStopIsSafeWithoutStart
func TestStopIsSafeWithoutStart(t *testing.T) {
	require.NotPanics(t, func() { NewScheduler().Stop() })
}

func TestJobScheduleValidate(t *testing.T) {
	assert.NoError(t, core.JobSchedule{Every: time.Second}.Validate())
	assert.ErrorIs(t, core.JobSchedule{}.Validate(), core.ErrJobScheduleMissingInterval)
	assert.ErrorIs(t, core.JobSchedule{Every: -1}.Validate(), core.ErrJobScheduleMissingInterval)
}
