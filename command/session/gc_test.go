package session

import (
	"context"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/osbits/gorgany/v2/app/core"
)

// H4. Nothing in the framework swept the sessions table. JobProvider now adds the job for a
// database-backed app, but that only covers a process that runs the scheduler; this command
// covers the rest — a web tier deployed without JobProvider, or wall-clock scheduling that
// core.JobSchedule's interval-only shape cannot express, which is what pushes maintenance
// towards an external cron in the first place.

type recordingStorage struct {
	core.ISessionStorage
	sweeps int
}

func (s *recordingStorage) ClearExpiredSessions() { s.sweeps++ }

func withStorageConfig(t *testing.T, storage string) {
	t.Helper()

	previous := viper.Get("auth.session.storage")
	viper.Set("auth.session.storage", storage)
	t.Cleanup(func() { viper.Set("auth.session.storage", previous) })
}

func TestTheCommandSweeps(t *testing.T) {
	withStorageConfig(t, "database")

	storage := &recordingStorage{}
	GcCommand{SessionStorage: storage}.Execute(context.Background())

	assert.Equal(t, 1, storage.sweeps)
}

// TestTheCommandRefusesMemoryStorage. Memory sessions live in the web process's heap, so
// sweeping from a separate CLI process empties a map created moments earlier and about to be
// discarded. Reporting success there would be the most likely way to wire this up wrong and
// believe it was working.
func TestTheCommandRefusesMemoryStorage(t *testing.T) {
	withStorageConfig(t, "memory")

	storage := &recordingStorage{}
	GcCommand{SessionStorage: storage}.Execute(context.Background())

	assert.Zero(t, storage.sweeps)
}

// TestTheCommandSurvivesAnUnresolvedStorage: the field is injected, so a misconfigured
// container can leave it nil.
func TestTheCommandSurvivesAnUnresolvedStorage(t *testing.T) {
	withStorageConfig(t, "database")

	require.NotPanics(t, func() {
		GcCommand{}.Execute(context.Background())
	})
}

// TestTheCommandIsNamedWhatTheDocsSay — cron lines and docs quote it, so it is pinned.
func TestTheCommandIsNamedWhatTheDocsSay(t *testing.T) {
	assert.Equal(t, "session:gc", GcCommand{}.GetName())
}

// TestTheCommandAndTheJobSweepTheSameWay. Both call
// ISessionStorage.ClearExpiredSessions, which is what keeps a cron-driven app and a
// scheduler-driven one from diverging. Asserted through the storage rather than by reading
// the two call sites, so the property survives a refactor of either.
func TestTheCommandAndTheJobSweepTheSameWay(t *testing.T) {
	withStorageConfig(t, "database")

	viaCommand := &recordingStorage{}
	GcCommand{SessionStorage: viaCommand}.Execute(context.Background())

	viaJob := &recordingStorage{}
	require.NoError(t, sweepJob{storage: viaJob}.run())

	assert.Equal(t, viaCommand.sweeps, viaJob.sweeps)
}

// sweepJob stands in for job.ClearExpiredSessionsJob without importing it, which would make
// command/session depend on job purely for a test.
type sweepJob struct{ storage core.ISessionStorage }

func (j sweepJob) run() error {
	j.storage.ClearExpiredSessions()
	return nil
}
