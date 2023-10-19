package provider

import (
	"github.com/jasonlvhit/gocron"
	"gorgany/log"
	"gorgany/proxy"
	"reflect"
)

func NewJobProvider() *JobProvider {
	return &JobProvider{jobs: make(chan proxy.IJob)}
}

type JobProvider struct {
	jobs chan proxy.IJob
}

func (thiz *JobProvider) InitProvider() {
	if thiz.jobs == nil {
		thiz.jobs = make(chan proxy.IJob)
	}
	thiz.startScheduler()
}

func (this *JobProvider) RegisterJob(job proxy.IJob) {
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
