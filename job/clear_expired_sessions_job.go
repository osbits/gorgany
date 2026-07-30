package job

import (
	"context"

	"github.com/osbits/gorgany/v2/app/core"
)

// ClearExpiredSessionsJob is the framework's session garbage collector, registered
// by the standard setup for apps using `auth.session.storage: database`.
//
// It has never actually run. Its schedule was built with gocron.Every(), which
// registers on gocron's package-level default scheduler, while JobProvider started
// a different, empty one — so every such app has a sessions table that grows
// without bound.
type ClearExpiredSessionsJob struct {
	SessionStorage core.ISessionStorage `container:"inject"`
}

var _ core.IJob = (*ClearExpiredSessionsJob)(nil)

// Schedule sweeps once per session lifetime.
//
// RunAtStartup is on because a process restarting more often than its session
// lifetime would otherwise never collect anything. Overlap is disallowed so a sweep
// that outlasts its interval cannot run twice over the same rows.
func (thiz ClearExpiredSessionsJob) Schedule() core.JobSchedule {
	return core.JobSchedule{
		Every:        thiz.SessionStorage.GetSessionLifetime(),
		RunAtStartup: true,
		AllowOverlap: false,
	}
}

// Run deletes every expired session.
func (thiz ClearExpiredSessionsJob) Run(_ context.Context) error {
	thiz.SessionStorage.ClearExpiredSessions()
	return nil
}
