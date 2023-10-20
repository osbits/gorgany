package provider

import (
	"github.com/jasonlvhit/gocron"
	"gorgany/app/core"
	"gorgany/log"
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

func (this *JobProvider) RegisterJob(job core.IJob) {
	this.jobs <- job
}

func (thiz *JobProvider) startScheduler() {
	go func() {
		cronScheduler := gocron.Start()

		for j := range thiz.jobs {
			rtJob := reflect.TypeOf(j).Elem()
			jobName := rtJob.Name()
			err := j.InitSchedule().Do(thiz.startJob, jobName, j.Handle)
			if err != nil {
				log.Log("").Errorf("Unable to start job %s", jobName)
			}
		}

		<-cronScheduler
	}()
}

func (thiz *JobProvider) startJob(jobName string, handler func()) {
	log.Log("").Infof("Start job %s", jobName)
	handler()
	log.Log("").Infof("End job %s", jobName)
}
