package provider

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/viper"

	"github.com/osbits/gorgany/v2/app/core"
	grgerr "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/job"
	"github.com/osbits/gorgany/v2/util"
)

type JobProvider struct {
	ctors []func() core.IJob

	// scheduler is created in Register and started in Boot, so an app can reach it
	// through the container to inspect or stop it.
	scheduler *job.Scheduler

	// sessionGcDisabled opts out of the framework's own session sweep.
	sessionGcDisabled bool
}

func NewJobProvider() *JobProvider {
	return &JobProvider{ctors: make([]func() core.IJob, 0)}
}

// DisableSessionGc stops this provider adding job.ClearExpiredSessionsJob.
//
// Use it when the app sweeps some other way — the `session:gc` command from an external
// cron, a database-side job, a TTL policy. Mirrors RouteProvider.DisableCsrfController: the
// framework's default should work without being asked for, and opting out should be one call.
func (p *JobProvider) DisableSessionGc() {
	p.sessionGcDisabled = true
}

func (p *JobProvider) AddJob(ctor func() core.IJob) {
	p.ctors = append(p.ctors, ctor)
}

// Scheduler returns the scheduler this provider manages, so an app can Stop() it at
// shutdown.
func (p *JobProvider) Scheduler() *job.Scheduler {
	return p.scheduler
}

func (p *JobProvider) Register(c core.IContainer) {
	p.scheduler = job.NewScheduler()

	scheduler := p.scheduler
	c.SingletonLazy(func() *job.Scheduler {
		return scheduler
	})

	for _, ctor := range p.ctors {
		c.TransientLazy(func(ctor func() core.IJob) func() core.IJob {
			return ctor
		}(ctor))
	}
}

// Boot registers every job on the provider's own scheduler and starts it.
//
// The previous implementation could not have worked:
//
//	sched := &gocron.Scheduler{}
//	c.Make(sched)                  // field injection, not resolution — see B4
//	...
//	schedule := job.InitSchedule() // gocron.Every() -> package-level defaultScheduler
//	schedule.Do(handler)           // registered on defaultScheduler
//	go sched.Start()               // ticks the zero-value scheduler
//
// Jobs were registered on gocron's global while an empty, uninitialised scheduler
// was started, and nothing ever called gocron.Start() on that global. So no
// scheduled job in any gorgany app ever ran, and the panic-recovery wrapper around
// the handler was dead code. ClearExpiredSessionsJob is the framework's own session
// GC, which is why every app on `auth.session.storage: database` has a sessions
// table that grows without bound — the symptom is a slow leak, not an error, which
// is how this went unnoticed.
//
// A job that fails to register is now fatal, for the same reason: a silently
// unscheduled job is close to undetectable.
//
// H4 fixed the other half. Making the scheduler work did not make the session GC run,
// because nothing added ClearExpiredSessionsJob to any provider — its own doc comment claimed
// it was "registered by the standard setup" and no such registration existed anywhere in the
// framework, while DbProvider adds the sessions migration unconditionally. So a
// database-backed app got the table and never got the sweep. It is added here now.
func (p *JobProvider) Boot(c core.IContainer) {
	if p.scheduler == nil {
		p.scheduler = job.NewScheduler()
	}

	registered := make(map[string]bool, len(p.ctors)+1)

	for _, ctor := range p.addSessionGc(p.ctors) {
		instance := ctor()

		if err := injectJobDependencies(c, instance); err != nil {
			panic(fmt.Errorf("job Boot: %w", err))
		}

		name := util.IndirectType(reflect.TypeOf(instance)).Name()

		// An app that already added the framework's session GC by hand — which the docs
		// used to be the only way — must not now fail to boot on
		// `job "ClearExpiredSessionsJob" is already registered`. Skipping the framework's
		// copy here means the app's own registration wins, keeping any Schedule override
		// it made.
		if registered[name] {
			continue
		}
		registered[name] = true

		if err := p.scheduler.Add(name, instance.Schedule(), instance.Run); err != nil {
			panic(fmt.Errorf("job Boot: %w", err))
		}
	}

	if err := p.scheduler.Start(context.Background()); err != nil {
		grgerr.HandleError(fmt.Errorf("job Boot: cannot start the scheduler: %w", err))
	}
}

// addSessionGc adds the framework's session sweep for a database-backed app.
//
// Only for `database` storage: memory sessions live in this process's heap and are collected
// when it exits, so the sweep would be busywork — and MemorySession's own map is bounded by
// the process's lifetime, not by the sessions table.
//
// It is appended rather than prepended so an app's own registration of the same job is seen
// first and wins, and it goes through the return value rather than AddJob so calling Boot
// twice does not accumulate copies.
//
// Consulted in Boot rather than the constructor so the config has been parsed by the time it
// is read, and so DisableSessionGc can be called in between.
func (p *JobProvider) addSessionGc(ctors []func() core.IJob) []func() core.IJob {
	if p.sessionGcDisabled {
		return ctors
	}
	if viper.GetString("auth.session.storage") != "database" {
		return ctors
	}

	return append(append([]func() core.IJob{}, ctors...),
		func() core.IJob { return &job.ClearExpiredSessionsJob{} })
}

// injectJobDependencies fills a job's `container:"inject"` fields.
//
// Injection needs a pointer, because Go cannot write fields into a copy. A
// value-typed job with no inject tags needs nothing and proceeds; one that *has*
// inject tags is a wiring error that could never work, so it fails here rather than
// running forever with nil dependencies. The old code called c.Make unconditionally
// and passed the error to HandleError, which logged and returned — so a
// value-registered job silently stopped the whole Boot loop after the first one.
func injectJobDependencies(c core.IContainer, instance core.IJob) error {
	if reflect.ValueOf(instance).Kind() == reflect.Ptr {
		if err := c.Make(instance); err != nil {
			return fmt.Errorf("cannot make job %T: %w", instance, err)
		}
		return nil
	}

	if fields := injectTaggedFields(reflect.TypeOf(instance)); len(fields) > 0 {
		return fmt.Errorf(
			"job %T is registered by value but declares container:\"inject\" field(s) %v, "+
				"which can never be filled; register it as a pointer (&%T{}) instead",
			instance, fields, instance)
	}
	return nil
}

// injectTaggedFields lists the `container:"inject"` field names of a struct type.
func injectTaggedFields(rt reflect.Type) []string {
	rt = util.IndirectType(rt)
	if rt.Kind() != reflect.Struct {
		return nil
	}

	var tagged []string
	for i := 0; i < rt.NumField(); i++ {
		field := rt.Field(i)
		if tag, ok := field.Tag.Lookup("container"); ok && strings.HasPrefix(tag, "inject") {
			tagged = append(tagged, field.Name)
		}
	}
	return tagged
}
