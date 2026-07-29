package provider

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/osbits/gorgany/app/core"
	grgerr "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/job"
	"github.com/osbits/gorgany/util"
)

type JobProvider struct {
	ctors []func() core.IJob

	// scheduler is created in Register and started in Boot, so an app can reach it
	// through the container to inspect or stop it.
	scheduler *job.Scheduler
}

func NewJobProvider() *JobProvider {
	return &JobProvider{ctors: make([]func() core.IJob, 0)}
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
func (p *JobProvider) Boot(c core.IContainer) {
	if p.scheduler == nil {
		p.scheduler = job.NewScheduler()
	}

	for _, ctor := range p.ctors {
		instance := ctor()

		if err := injectJobDependencies(c, instance); err != nil {
			panic(fmt.Errorf("job Boot: %w", err))
		}

		name := util.IndirectType(reflect.TypeOf(instance)).Name()

		if err := p.scheduler.Add(name, instance.Schedule(), instance.Run); err != nil {
			panic(fmt.Errorf("job Boot: %w", err))
		}
	}

	if err := p.scheduler.Start(context.Background()); err != nil {
		grgerr.HandleError(fmt.Errorf("job Boot: cannot start the scheduler: %w", err))
	}
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
