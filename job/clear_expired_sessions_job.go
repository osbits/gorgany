package job

import (
	"context"

	"github.com/osbits/gorgany/v2/app/core"
)

// ClearExpiredSessionsJob is the framework's session garbage collector.
//
// JobProvider.Boot adds it for an app on `auth.session.storage: database`, unless
// DisableSessionGc() was called. That sentence was in this comment before it was true: the
// job claimed to be "registered by the standard setup" and no registration existed anywhere
// in the framework, while DbProvider adds the sessions migration unconditionally — so every
// database-backed app got the table and no sweep (H4).
//
// It could not have run even if something had added it. The schedule was built with
// gocron.Every(), which registers on gocron's package-level default scheduler, while
// JobProvider started a different, empty one; A2 replaced gocron with the framework's own
// scheduler and fixed that half.
//
// An app whose web tier does not run the scheduler, or which prefers wall-clock scheduling
// that core.JobSchedule's interval-only shape cannot express, should call DisableSessionGc()
// and run the `session:gc` command from cron instead. Both paths call the same
// ISessionStorage.ClearExpiredSessions.
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
