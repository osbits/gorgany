package job

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"github.com/jasonlvhit/gocron"
)

type ClearExpiredSessionsJob struct {
	SessionStorage core.ISessionStorage `container:"inject"`
}

func (thiz ClearExpiredSessionsJob) InitSchedule() *gocron.Job {
	return gocron.Every(uint64(thiz.SessionStorage.GetSessionLifetime().Seconds())).Seconds()
}

func (thiz ClearExpiredSessionsJob) Handle() {
	thiz.SessionStorage.ClearExpiredSessions()
}
