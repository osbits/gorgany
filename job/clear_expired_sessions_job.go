package job

import (
	"github.com/jasonlvhit/gocron"
	"gorgany/auth"
	"gorgany/internal"
)

type ClearExpiredSessionsJob struct {
}

func (thiz ClearExpiredSessionsJob) InitSchedule() *gocron.Job {
	return gocron.Every(uint64(internal.GetFrameworkRegistrar().GetSessionLifetime())).Seconds()
}

func (thiz ClearExpiredSessionsJob) Handle() {
	auth.GetSessionStorage().ClearExpiredSessions()
}
