package job

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/log"
)

// Scheduler runs registered jobs on their schedules.
//
// This replaces github.com/jasonlvhit/gocron rather than swapping it for another
// library. That dependency was unmaintained, pinned at v0.0.1, had no context
// support — so a job could not be cancelled at shutdown — and its only entry point
// registered work on a package-level global that the caller could not reach. Every
// one of those is why jobs never ran. An interval scheduler is a ticker per job;
// taking a dependency for it bought nothing and cost the ability to cancel.
//
// Cron expressions are deliberately not supported. Adding them means either a cron
// parser dependency or writing one, and shipping a `Cron` field that returns "not
// implemented" would be exactly the empty promise this codebase already has one of
// (the removed core.MongoDb). Interval scheduling covers the framework's own job and the common
// case; cron can be added when something needs it.
//
// A Scheduler is safe for concurrent use. Add before Start; adding afterwards is an
// error, because a job registered into a running scheduler would silently never be
// ticked — the class of bug this type exists to end.
type Scheduler struct {
	mu      sync.Mutex
	entries []*entry
	started bool

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// entry is one scheduled job.
type entry struct {
	name     string
	schedule core.JobSchedule
	run      func(context.Context) error

	// running guards against overlapping runs when AllowOverlap is false.
	running atomic.Bool
	// runs counts completed invocations, for observability and tests.
	runs atomic.Int64
	// skipped counts ticks dropped because the previous run was still going.
	skipped atomic.Int64
}

// NewScheduler creates an empty scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{}
}

// Add registers a job under name.
//
// An invalid schedule is rejected here, at boot, rather than being silently
// dropped: a job that never runs is close to undetectable in production, which is
// how the previous implementation went unnoticed.
func (s *Scheduler) Add(name string, schedule core.JobSchedule, run func(context.Context) error) error {
	if name == "" {
		return fmt.Errorf("scheduler: a job needs a name")
	}
	if run == nil {
		return fmt.Errorf("scheduler: job %q has no work to run", name)
	}
	if scheduleErr := schedule.Validate(); scheduleErr != nil {
		return fmt.Errorf("scheduler: job %q: %w", name, scheduleErr)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return fmt.Errorf("scheduler: cannot add job %q after Start", name)
	}
	for _, existing := range s.entries {
		if existing.name == name {
			return fmt.Errorf("scheduler: job %q is already registered", name)
		}
	}

	s.entries = append(s.entries, &entry{name: name, schedule: schedule, run: run})
	return nil
}

// Start begins ticking every registered job. It returns immediately; each job runs
// on its own goroutine.
//
// The context governs the whole scheduler: cancelling it stops every ticker and is
// passed to each in-flight Run, so a job can return early at shutdown. Stop waits
// for in-flight runs to finish.
func (s *Scheduler) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("scheduler: already started")
	}
	s.started = true

	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	entries := append([]*entry{}, s.entries...)
	s.mu.Unlock()

	for _, e := range entries {
		s.wg.Add(1)
		go s.loop(runCtx, e)
	}

	if len(entries) > 0 {
		log.Log().Infof("scheduler: started with %d job(s)", len(entries))
	}
	return nil
}

// Stop cancels the scheduler and waits for in-flight runs to return.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// loop ticks one job until the context is cancelled.
//
// Each tick is dispatched onto its own goroutine rather than run inline. That is not
// incidental: time.Ticker's channel buffers exactly one tick, so a loop that ran the
// job inline would stop reading the ticker for the duration of the run. Ticks would
// then be dropped by the ticker itself, AllowOverlap could never take effect, and no
// skip would ever be recorded — the scheduler would silently stretch the interval of
// any job that ran longer than it. Dispatching keeps the ticker drained and leaves
// the overlap decision to invoke, where it is explicit and observable.
func (s *Scheduler) loop(ctx context.Context, e *entry) {
	defer s.wg.Done()

	if e.schedule.RunAtStartup {
		s.dispatch(ctx, e)
	}

	ticker := time.NewTicker(e.schedule.Every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.dispatch(ctx, e)
		}
	}
}

// dispatch runs one invocation on its own goroutine, tracked so Stop can wait for it.
//
// Adding to the WaitGroup here is safe: the calling loop goroutine holds a count of
// its own for as long as it is running, so the counter is never zero at this point.
func (s *Scheduler) dispatch(ctx context.Context, e *entry) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.invoke(ctx, e)
	}()
}

// invoke runs the job once, honouring overlap protection and recovering a panic.
func (s *Scheduler) invoke(ctx context.Context, e *entry) {
	if !e.schedule.AllowOverlap {
		if !e.running.CompareAndSwap(false, true) {
			e.skipped.Add(1)
			log.Log().Warnf(
				"scheduler: skipping job %s, the previous run has not finished (skipped %d so far)",
				e.name, e.skipped.Load())
			return
		}
		defer e.running.Store(false)
	}

	// A panicking job must not take the scheduler goroutine — and therefore every
	// future run of that job — down with it.
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Log().Errorf("scheduler: job %s panicked: %v\n%s",
				e.name, recovered, err.GetStacktrace())
		}
	}()

	log.Log().Infof("scheduler: job %s starting", e.name)
	if runErr := e.run(ctx); runErr != nil {
		// A cancelled context at shutdown is expected, not a failure.
		if ctx.Err() != nil {
			log.Log().Infof("scheduler: job %s stopped: %v", e.name, runErr)
		} else {
			log.Log().Errorf("scheduler: job %s failed: %v", e.name, runErr)
		}
	}
	e.runs.Add(1)
	log.Log().Infof("scheduler: job %s finished", e.name)
}

// Jobs returns the registered job names, for diagnostics.
func (s *Scheduler) Jobs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	names := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		names = append(names, e.name)
	}
	return names
}

// Stats reports how many times a job has completed and how many ticks were skipped
// for overlap. Intended for tests and health endpoints.
func (s *Scheduler) Stats(name string) (runs int64, skipped int64, found bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range s.entries {
		if e.name == name {
			return e.runs.Load(), e.skipped.Load(), true
		}
	}
	return 0, 0, false
}
