package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/viper"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/job"
	"github.com/osbits/gorgany/v2/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingJob records every run so a test can assert the schedule fired.
type countingJob struct {
	hits     *int32
	schedule core.JobSchedule
}

func (j countingJob) Schedule() core.JobSchedule { return j.schedule }

func (j countingJob) Run(context.Context) error {
	atomic.AddInt32(j.hits, 1)
	return nil
}

// injectedJob has a container-injected dependency, so the test covers the Make step
// as well as the scheduling.
type injectedJob struct {
	Validator core.IValidator `container:"inject"`
	saw       *int32
	injected  *int32
}

func (j injectedJob) Schedule() core.JobSchedule {
	return core.JobSchedule{Every: 20 * time.Millisecond}
}

func (j injectedJob) Run(context.Context) error {
	atomic.AddInt32(j.saw, 1)
	if j.Validator != nil {
		atomic.AddInt32(j.injected, 1)
	}
	return nil
}

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

// TestAJobRegisteredThroughTheProviderFires is the end-to-end version of the A2
// regression. Before this, JobProvider.Boot registered work on gocron's
// package-level default scheduler and then started a separate, zero-value
// &gocron.Scheduler{} — so a job scheduled every second fired zero times, forever,
// in every gorgany app.
func TestAJobRegisteredThroughTheProviderFires(t *testing.T) {
	var hits int32

	p := NewJobProvider()
	p.AddJob(func() core.IJob {
		return countingJob{hits: &hits, schedule: core.JobSchedule{Every: 20 * time.Millisecond}}
	})

	c := service.NewContainer()
	p.Register(c)
	p.Boot(c)
	t.Cleanup(p.Scheduler().Stop)

	eventually(t, 2*time.Second,
		func() bool { return atomic.LoadInt32(&hits) >= 3 },
		"a job registered through JobProvider must actually run")
}

// TestTheSchedulerIsResolvableFromTheContainer — an app needs a handle to stop it at
// shutdown, which the old code made impossible because the running scheduler was
// gocron's unreachable global.
func TestTheSchedulerIsResolvableFromTheContainer(t *testing.T) {
	p := NewJobProvider()

	c := service.NewContainer()
	p.Register(c)

	var resolved *job.Scheduler
	require.NoError(t, c.Resolve(&resolved))
	require.NotNil(t, resolved)
	assert.Same(t, p.Scheduler(), resolved,
		"the container must hand back the same scheduler the provider runs")
}

// TestJobDependenciesAreInjectedBeforeScheduling
func TestJobDependenciesAreInjectedBeforeScheduling(t *testing.T) {
	var saw, injected int32

	p := NewJobProvider()
	p.AddJob(func() core.IJob { return &injectedJob{saw: &saw, injected: &injected} })

	c := service.NewContainer()
	require.NoError(t, c.SingletonLazy(func() core.IValidator { return &stubValidator{} }))
	p.Register(c)
	p.Boot(c)
	t.Cleanup(p.Scheduler().Stop)

	eventually(t, 2*time.Second,
		func() bool { return atomic.LoadInt32(&saw) >= 1 },
		"the job must run")
	assert.Positive(t, atomic.LoadInt32(&injected),
		"container:\"inject\" fields must be filled before the job is scheduled")
}

// TestTheJobIsRegisteredUnderItsTypeName
func TestTheJobIsRegisteredUnderItsTypeName(t *testing.T) {
	var hits int32

	p := NewJobProvider()
	p.AddJob(func() core.IJob {
		return countingJob{hits: &hits, schedule: core.JobSchedule{Every: time.Hour}}
	})

	c := service.NewContainer()
	p.Register(c)
	p.Boot(c)
	t.Cleanup(p.Scheduler().Stop)

	assert.Equal(t, []string{"countingJob"}, p.Scheduler().Jobs())
}

// TestAnUnschedulableJobIsFatalAtBoot: a job that could never run must stop the app
// rather than be silently dropped. A silently unscheduled job is close to
// undetectable — the symptom is a slow leak, which is exactly how A2 survived.
func TestAnUnschedulableJobIsFatalAtBoot(t *testing.T) {
	var hits int32

	p := NewJobProvider()
	p.AddJob(func() core.IJob {
		// No interval at all.
		return countingJob{hits: &hits, schedule: core.JobSchedule{}}
	})

	c := service.NewContainer()
	p.Register(c)

	assert.Panics(t, func() { p.Boot(c) },
		"a job with no runnable schedule must fail the boot")
}

// TestClearExpiredSessionsJobIsSchedulable pins the framework's own job, whose never
// running is why apps on database-backed sessions leak rows.
func TestClearExpiredSessionsJobIsSchedulable(t *testing.T) {
	var cleared int32

	storage := &stubSessionStorage{lifetime: 20 * time.Millisecond, cleared: &cleared}

	p := NewJobProvider()
	p.AddJob(func() core.IJob { return &job.ClearExpiredSessionsJob{} })

	c := service.NewContainer()
	// The storage arrives through the container, as it does in a real app where
	// AppProvider binds it.
	require.NoError(t, c.SingletonLazy(func() core.ISessionStorage { return storage }))
	p.Register(c)
	p.Boot(c)
	t.Cleanup(p.Scheduler().Stop)

	assert.Equal(t, []string{"ClearExpiredSessionsJob"}, p.Scheduler().Jobs())

	eventually(t, 2*time.Second,
		func() bool { return atomic.LoadInt32(&cleared) >= 2 },
		"the framework's session GC must actually sweep")
}

// TestClearExpiredSessionsRunsAtStartup: a process restarting more often than its
// session lifetime would otherwise never collect anything.
func TestClearExpiredSessionsRunsAtStartup(t *testing.T) {
	var cleared int32
	storage := &stubSessionStorage{lifetime: time.Hour, cleared: &cleared}

	schedule := (&job.ClearExpiredSessionsJob{SessionStorage: storage}).Schedule()
	assert.True(t, schedule.RunAtStartup)
	assert.False(t, schedule.AllowOverlap)
	assert.Equal(t, time.Hour, schedule.Every)
}

// ------------------------------------------------------------------- test doubles

type stubValidator struct{ core.IValidator }

type stubSessionStorage struct {
	core.ISessionStorage
	lifetime time.Duration
	cleared  *int32
}

func (s *stubSessionStorage) GetSessionLifetime() time.Duration { return s.lifetime }

func (s *stubSessionStorage) ClearExpiredSessions() error {
	atomic.AddInt32(s.cleared, 1)
	return nil
}

// -------------------------------------- H4: the sweep the framework never registered

// H4. ClearExpiredSessionsJob's own doc comment said it was "registered by the standard setup
// for apps using auth.session.storage: database" — and no registration existed anywhere in
// the framework. provider/bootstrap.go holds no providers of its own, JobProvider had no
// default job, and nothing else referenced the type outside tests. Meanwhile DbProvider adds
// the sessions migration unconditionally, so every database-backed app got the table and no
// sweep. The symptom is a table that only grows, which is indistinguishable from a sweep that
// runs and finds nothing.
//
// TestClearExpiredSessionsJobIsSchedulable above proves the job *can* be scheduled once
// something adds it. These prove something does.

func TestTheSessionGcIsRegisteredForDatabaseStorage(t *testing.T) {
	withSessionStorageConfig(t, "database")

	p := NewJobProvider()
	c := containerWithSessionStorage(t, &stubSessionStorage{lifetime: time.Hour, cleared: new(int32)})
	p.Register(c)
	p.Boot(c)
	t.Cleanup(p.Scheduler().Stop)

	assert.Equal(t, []string{"ClearExpiredSessionsJob"}, p.Scheduler().Jobs(),
		"a database-backed app must get the sweep without asking for it")
}

// TestTheSessionGcIsNotRegisteredForMemoryStorage: memory sessions live in this process's
// heap and go away when it exits, so there is nothing to collect.
func TestTheSessionGcIsNotRegisteredForMemoryStorage(t *testing.T) {
	withSessionStorageConfig(t, "memory")

	p := NewJobProvider()
	c := containerWithSessionStorage(t, &stubSessionStorage{lifetime: time.Hour, cleared: new(int32)})
	p.Register(c)
	p.Boot(c)
	t.Cleanup(p.Scheduler().Stop)

	assert.Empty(t, p.Scheduler().Jobs())
}

func TestDisableSessionGcOptsOut(t *testing.T) {
	withSessionStorageConfig(t, "database")

	p := NewJobProvider()
	p.DisableSessionGc()
	c := containerWithSessionStorage(t, &stubSessionStorage{lifetime: time.Hour, cleared: new(int32)})
	p.Register(c)
	p.Boot(c)
	t.Cleanup(p.Scheduler().Stop)

	assert.Empty(t, p.Scheduler().Jobs(),
		"an app sweeping some other way must be able to turn this off")
}

// TestAnAppsOwnSessionGcRegistrationStillBoots is the compatibility half, and it is the
// reason the framework's copy is appended and skipped rather than simply added.
// Scheduler.Add rejects a duplicate name and Boot panics on the error, so an app that had
// wired the job by hand — the only way there was — would otherwise have stopped booting on
// `job "ClearExpiredSessionsJob" is already registered`.
func TestAnAppsOwnSessionGcRegistrationStillBoots(t *testing.T) {
	withSessionStorageConfig(t, "database")

	p := NewJobProvider()
	p.AddJob(func() core.IJob { return &job.ClearExpiredSessionsJob{} })

	c := containerWithSessionStorage(t, &stubSessionStorage{lifetime: time.Hour, cleared: new(int32)})
	p.Register(c)
	require.NotPanics(t, func() { p.Boot(c) })
	t.Cleanup(p.Scheduler().Stop)

	assert.Equal(t, []string{"ClearExpiredSessionsJob"}, p.Scheduler().Jobs(),
		"the job must be scheduled exactly once")
}

// TestTheRegisteredSessionGcActuallySweeps closes the loop: registered, injected, and
// running. The injection matters — ClearExpiredSessionsJob.Schedule() dereferences
// SessionStorage, so a job added without it panics rather than running.
func TestTheRegisteredSessionGcActuallySweeps(t *testing.T) {
	withSessionStorageConfig(t, "database")

	var cleared int32
	p := NewJobProvider()
	c := containerWithSessionStorage(t, &stubSessionStorage{
		lifetime: 20 * time.Millisecond,
		cleared:  &cleared,
	})
	p.Register(c)
	p.Boot(c)
	t.Cleanup(p.Scheduler().Stop)

	eventually(t, 2*time.Second,
		func() bool { return atomic.LoadInt32(&cleared) >= 2 },
		"the automatically registered session GC must actually sweep")
}

func withSessionStorageConfig(t *testing.T, storage string) {
	t.Helper()

	previous := viper.Get("auth.session.storage")
	viper.Set("auth.session.storage", storage)
	t.Cleanup(func() { viper.Set("auth.session.storage", previous) })
}

func containerWithSessionStorage(t *testing.T, storage core.ISessionStorage) core.IContainer {
	t.Helper()

	c := service.NewContainer()
	require.NoError(t, c.SingletonLazy(func() core.ISessionStorage { return storage }))
	return c
}
