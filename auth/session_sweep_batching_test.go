package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/osbits/gorgany/v2/app/core"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// I3. DeleteExpired used to be one statement, `DELETE FROM sessions WHERE expiry < NOW()`.
// Harmless while nothing called it — the job meant to call it was registered by nothing until
// H4 — but H4 gives it a caller, so the first sweep on an app running for months would delete
// the whole accumulated backlog in a single statement: a long lock on the matched tuples, a WAL
// burst proportional to the backlog, and bloat needing VACUUM. The fix for the leak would have
// hit hardest exactly the apps that had leaked most.
//
// The statement's portability is checked against live Postgres and MySQL in e2e (the derived
// table exists because MySQL rejects a subquery on the DELETE's own target). What is checked
// here is the loop around it, which no live test can pin cheaply: that it stops on a short
// batch, that it does not stop early, that it surfaces an error instead of looping on it, and
// that it cannot run forever.

// ---------------------------------------------------------------- stub plumbing

// countingExecutor answers a fixed sequence of RowsAffected and records the SQL and args.
type countingExecutor struct {
	dbCore.IQueryExecutor

	remaining int64
	err       error
	calls     int
	lastSQL   string
	lastArgs  []any
}

func (e *countingExecutor) ExecRaw(_ context.Context, sql string, args ...interface{}) dbCore.QueryResult {
	e.calls++
	e.lastSQL = sql
	e.lastArgs = args

	if e.err != nil {
		return dbCore.QueryResult{Error: e.err}
	}

	batch := int64(args[0].(int))
	deleted := batch
	if e.remaining < batch {
		deleted = e.remaining
	}
	e.remaining -= deleted

	return dbCore.QueryResult{RowsAffected: deleted}
}

type stubSweepSession struct {
	dbCore.ISession
	executor *countingExecutor
}

func (s *stubSweepSession) Executor() dbCore.IQueryExecutor { return s.executor }
func (s *stubSweepSession) Close() error                    { return nil }

type stubSweepDataSource struct {
	dbCore.IDataSource
	session *stubSweepSession
}

func (d *stubSweepDataSource) NewSession() (dbCore.ISession, error) { return d.session, nil }

type stubSweepDbContext struct {
	core.IDBContext
	dataSource dbCore.IDataSource
}

func (c *stubSweepDbContext) GetDataSource(string) dbCore.IDataSource { return c.dataSource }

// sweepRepository wires a repository over a stub reporting `expired` deletable rows.
func sweepRepository(expired int64, err error) (*DbSessionRepository, *countingExecutor) {
	executor := &countingExecutor{remaining: expired, err: err}
	return &DbSessionRepository{
		dbContext: &stubSweepDbContext{
			dataSource: &stubSweepDataSource{session: &stubSweepSession{executor: executor}},
		},
	}, executor
}

func withBatchSize(t *testing.T, size int) {
	t.Helper()

	previous := SessionSweepBatchSize
	SessionSweepBatchSize = size
	t.Cleanup(func() { SessionSweepBatchSize = previous })
}

// -------------------------------------------------------------------- the tests

// TestTheSweepIsNotOneStatement is the headline: a backlog must not go in a single DELETE.
func TestTheSweepIsNotOneStatement(t *testing.T) {
	withBatchSize(t, 100)

	repo, executor := sweepRepository(1000, nil)
	require.NoError(t, repo.DeleteExpired())

	// 10 full batches, then one short batch that ends the loop.
	assert.Equal(t, 11, executor.calls)
	assert.Contains(t, executor.lastSQL, "LIMIT ?", "each statement must be bounded")
	assert.Equal(t, []any{100}, executor.lastArgs, "the bound is passed as a parameter")
}

// TestTheSweepStopsOnAShortBatch, rather than needing a second query per iteration to ask
// whether anything is left. A short batch means the last expired row is gone.
func TestTheSweepStopsOnAShortBatch(t *testing.T) {
	withBatchSize(t, 100)

	repo, executor := sweepRepository(30, nil)
	require.NoError(t, repo.DeleteExpired())

	assert.Equal(t, 1, executor.calls)
	assert.Zero(t, executor.remaining)
}

// TestTheSweepDoesNotStopEarly. An exact multiple of the batch size is the case a naive loop
// gets wrong: the last full batch looks like there might be more, and there is not — so it
// costs one extra empty statement and must not leave rows behind.
func TestTheSweepDoesNotStopEarly(t *testing.T) {
	withBatchSize(t, 10)

	repo, executor := sweepRepository(20, nil)
	require.NoError(t, repo.DeleteExpired())

	assert.Zero(t, executor.remaining, "every expired row must be gone")
	assert.Equal(t, 3, executor.calls, "two full batches plus the empty one that ends it")
}

// TestAnEmptyTableCostsOneStatement — the steady state on a healthy app.
func TestAnEmptyTableCostsOneStatement(t *testing.T) {
	repo, executor := sweepRepository(0, nil)
	require.NoError(t, repo.DeleteExpired())

	assert.Equal(t, 1, executor.calls)
}

// TestASweepErrorIsReturnedNotRetriedForever. Without this the loop would spin on a permanent
// failure — a revoked GRANT, a lock timeout — until the batch cap, hammering the database.
func TestASweepErrorIsReturnedNotRetriedForever(t *testing.T) {
	boom := errors.New("permission denied for table sessions")
	repo, executor := sweepRepository(1000, boom)

	err := repo.DeleteExpired()

	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, executor.calls, "it must give up on the first failure")
}

// TestTheSweepCannotRunForever. If rows arrive as fast as they are deleted the loop would
// never see a short batch. The cap ends the sweep and reports it, so the operator learns the
// table is not keeping up instead of the process holding a database connection indefinitely.
func TestTheSweepCannotRunForever(t *testing.T) {
	withBatchSize(t, 10)

	previous := SessionSweepMaxBatches
	SessionSweepMaxBatches = 5
	t.Cleanup(func() { SessionSweepMaxBatches = previous })

	// Far more expired rows than the cap can clear.
	repo, executor := sweepRepository(1_000_000, nil)

	err := repo.DeleteExpired()

	require.Error(t, err, "an unfinished sweep must not report success")
	assert.Contains(t, err.Error(), "next run",
		"the message has to say the remaining rows are not lost")
	assert.Equal(t, 5, executor.calls)
}

// TestAnInvalidBatchSizeFallsBack rather than making LIMIT 0 delete nothing forever, which
// would be an infinite loop bounded only by the batch cap.
func TestAnInvalidBatchSizeFallsBack(t *testing.T) {
	withBatchSize(t, 0)

	repo, executor := sweepRepository(5, nil)
	require.NoError(t, repo.DeleteExpired())

	require.NotEmpty(t, executor.lastArgs)
	assert.Positive(t, executor.lastArgs[0], "LIMIT 0 would never make progress")
}

// TestAMissingDataSourceIsAnError, not a nil dereference: the sweep now runs on a schedule, so
// it reaches this on any app whose default datasource failed to open.
func TestAMissingDataSourceIsAnError(t *testing.T) {
	repo := &DbSessionRepository{dbContext: &stubSweepDbContext{dataSource: nil}}

	require.Error(t, repo.DeleteExpired())
}
