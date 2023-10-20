package core

import "github.com/jasonlvhit/gocron"

type IJob interface {
	InitSchedule() *gocron.Job
	Handle()
}
