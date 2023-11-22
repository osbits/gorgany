package provider

import (
	"fmt"
	"github.com/jasonlvhit/gocron"
	"gorgany/app/core"
	"gorgany/err"
	"gorgany/log"
	"gorgany/util"
	"reflect"
)

func NewJobProvider() *JobProvider {
	return &JobProvider{jobs: make(chan core.IJob)}
}

type JobProvider struct {
	jobs chan core.IJob
}

func (thiz *JobProvider) InitProvider() {
	if thiz.jobs == nil {
		thiz.jobs = make(chan core.IJob)
	}
	thiz.startScheduler()
}

func (thiz *JobProvider) RegisterJob(job core.IJob) {
	thiz.jobs <- job
}

func (thiz *JobProvider) startScheduler() {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				err.HandleErrorWithStacktrace(fmt.Sprintf("Error when scheduling job: %v", r))
			}
		}()

		cronScheduler := gocron.Start()
		for j := range thiz.jobs {
			rtJob := util.IndirectType(reflect.TypeOf(j))
			jobName := rtJob.Name()
			e := j.InitSchedule().Do(thiz.startJob, jobName, j.Handle)
			if e != nil {
				log.Log("").Errorf("Unable to start job %s", jobName)
			}
		}
		<-cronScheduler
	}()
}

func (thiz *JobProvider) startJob(jobName string, handler func()) {
	defer func() {
		if r := recover(); r != nil {
			err.HandleErrorWithStacktrace(fmt.Sprintf("Error when executing job %s: %v", jobName, r))
		}
	}()
	log.Log("").Infof("Start job %s", jobName)
	handler()
	log.Log("").Infof("End job %s", jobName)
}
