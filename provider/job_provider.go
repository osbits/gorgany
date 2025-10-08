package provider

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	grgerr "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/jasonlvhit/gocron"
	"reflect"
)

type JobProvider struct {
	ctors []func() core.IJob
}

func NewJobProvider() *JobProvider {
	return &JobProvider{ctors: make([]func() core.IJob, 0)}
}

func (p *JobProvider) AddJob(ctor func() core.IJob) {
	p.ctors = append(p.ctors, ctor)
}

func (p *JobProvider) Register(c core.IContainer) {
	c.SingletonLazy(func() *gocron.Scheduler {
		return gocron.NewScheduler()
	})

	for _, ctor := range p.ctors {
		c.TransientLazy(func(ctor func() core.IJob) func() core.IJob {
			return ctor
		}(ctor))
	}
}

func (p *JobProvider) Boot(c core.IContainer) {
	sched := &gocron.Scheduler{}
	if err := c.Make(sched); err != nil {
		grgerr.HandleError(fmt.Errorf("job Boot: cannot Make Scheduler: %w", err))
		return
	}

	for _, ctor := range p.ctors {

		job := ctor()

		if err := c.Make(job); err != nil {
			grgerr.HandleError(fmt.Errorf("job Boot: cannot make job %T: %w", job, err))
			return
		}

		rt := util.IndirectType(reflect.TypeOf(job))
		name := rt.Name()

		schedule := job.InitSchedule()

		handler := func() {
			defer func() {
				if r := recover(); r != nil {
					log.Log("").Errorf("Error executing job %s: %v", name, r)
				}
			}()
			log.Log("").Infof("Start job %s", name)
			job.Handle()
			log.Log("").Infof("End job %s", name)
		}
		schedule.Do(handler)
	}

	go sched.Start()
}
