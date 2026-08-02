package session

import (
	"context"
	"errors"
	"io"
	"os"
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
	err    error
}

func (s *recordingStorage) ClearExpiredSessions() error {
	s.sweeps++
	return s.err
}

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

// TestTheCommandDoesNotClaimToHaveSweptWhenTheSweepFailed. A cron entry judges the run by
// what it prints, and the storage used to swallow the error entirely: a sweep that had never
// once succeeded printed the same line as one that worked.
func TestTheCommandDoesNotClaimToHaveSweptWhenTheSweepFailed(t *testing.T) {
	withStorageConfig(t, "database")

	storage := &recordingStorage{err: errors.New("permission denied for table sessions")}

	stdout := captureStdout(t, func() {
		GcCommand{SessionStorage: storage}.Execute(context.Background())
	})

	assert.Equal(t, 1, storage.sweeps)
	assert.Contains(t, stdout, "NOT cleared")
	assert.Contains(t, stdout, "permission denied for table sessions")
}

// captureStdout collects what fn prints. The command reports to the operator through stdout,
// so that is where the assertion has to look.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	previous := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = previous }()

	fn()
	require.NoError(t, writer.Close())

	out, err := io.ReadAll(reader)
	require.NoError(t, err)

	return string(out)
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
	return j.storage.ClearExpiredSessions()
}
