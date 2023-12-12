package job

import (
	"git.qix.sx/gorgany/gorgany.git/auth"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"github.com/jasonlvhit/gocron"
)

type ClearExpiredSessionsJob struct {
}

func (thiz ClearExpiredSessionsJob) InitSchedule() *gocron.Job {
	return gocron.Every(uint64(internal.GetFrameworkRegistrar().GetSessionLifetime())).Seconds()
}

func (thiz ClearExpiredSessionsJob) Handle() {
	auth.GetSessionStorage().ClearExpiredSessions()
}
