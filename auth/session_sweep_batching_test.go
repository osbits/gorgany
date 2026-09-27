package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"

	"github.com/osbits/gorgany/v2/app/core"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	pgv2 "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	sqlserverv2 "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
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

// ------------------------------------------------------------ the statement per dialect

// SQL Server has neither LIMIT nor NOW(), so BatchedExpiredDeleteSQL fails there as a syntax
// error and a SQL Server default would never have swept anything. The repository now picks the
// statement by the dialect its datasource reports. What these pin is the choice and that it
// changes nothing else: the Postgres and MySQL statement is byte-for-byte the one that was run
// against both live engines, a datasource that cannot say which dialect it speaks keeps it,
// and the loop around the T-SQL one batches exactly as it does around the portable one.

// namedDialect is a dialect that can only say its name; anything else would panic on the nil
// embedded interface, which is the point: choosing a statement must not need more.
type namedDialect struct {
	dbCore.SQLDialect
	name string
}

func (d namedDialect) Name() string { return d.name }

// dialectSweepDataSource is the sweep stub with a Dialect method, as the Postgres, MySQL and
// SQL Server datasources have. dialect may be nil, which a datasource can also return.
type dialectSweepDataSource struct {
	*stubSweepDataSource
	dialect dbCore.SQLDialect
}

func (d *dialectSweepDataSource) Dialect() dbCore.SQLDialect { return d.dialect }

// sweepRepositoryOn is sweepRepository over a datasource reporting dialect.
func sweepRepositoryOn(dialect dbCore.SQLDialect, expired int64) (*DbSessionRepository, *countingExecutor) {
	executor := &countingExecutor{remaining: expired}
	dataSource := &dialectSweepDataSource{
		stubSweepDataSource: &stubSweepDataSource{session: &stubSweepSession{executor: executor}},
		dialect:             dialect,
	}
	return &DbSessionRepository{dbContext: &stubSweepDbContext{dataSource: dataSource}}, executor
}

// TestBatchedExpiredDeleteSQLIsUnchanged: the portable statement is exported, run verbatim by
// the e2e portability check and reusable by apps, and was verified against live Postgres and
// MySQL. Adding SQL Server must not have touched a byte of it.
func TestBatchedExpiredDeleteSQLIsUnchanged(t *testing.T) {
	assert.Equal(t,
		"DELETE FROM sessions WHERE id IN (SELECT id FROM (SELECT id FROM sessions WHERE expiry < NOW() LIMIT ?) AS batch)",
		BatchedExpiredDeleteSQL)
}

// TestTheSQLServerSweepStatementIsBoundedAndOffsetAware pins the T-SQL: bounded by a TOP
// whose size is the one parameter, and cut off at SYSDATETIMEOFFSET(), which compares
// correctly with a datetimeoffset column whatever time zone the server's clock is on.
func TestTheSQLServerSweepStatementIsBoundedAndOffsetAware(t *testing.T) {
	assert.Equal(t,
		"DELETE TOP (?) FROM [sessions] WHERE [expiry] < SYSDATETIMEOFFSET()",
		BatchedExpiredDeleteSQLServer)
	assert.Equal(t, 1, strings.Count(BatchedExpiredDeleteSQLServer, "?"),
		"the batch size is its only argument, as it is the portable statement's")
	assert.NotContains(t, BatchedExpiredDeleteSQLServer, "LIMIT")
	assert.NotContains(t, BatchedExpiredDeleteSQLServer, "NOW()")
	assert.NotContains(t, BatchedExpiredDeleteSQLServer, "GETDATE()",
		"GETDATE() is local time with no offset, and would shift the cut-off by the server's offset")
}

func TestBatchedExpiredDeleteSQLForPicksTSQLOnlyForSQLServer(t *testing.T) {
	assert.Equal(t, BatchedExpiredDeleteSQLServer, BatchedExpiredDeleteSQLFor("sqlserver"))

	for _, dialect := range []string{"postgres", "mysql", "", "SQLServer", "mssql"} {
		assert.Equalf(t, BatchedExpiredDeleteSQL, BatchedExpiredDeleteSQLFor(dialect),
			"%q must keep the portable statement", dialect)
	}
}

// TestTheSQLServerNameIsTheEnginesOwn: the repository recognises SQL Server by a string, so
// that auth does not link go-mssqldb. This test may import the engine, and checks that the
// string is still the one its dialect reports.
func TestTheSQLServerNameIsTheEnginesOwn(t *testing.T) {
	assert.Equal(t, sqlserverv2.DialectName, sqlServerDialect)
	assert.Equal(t, BatchedExpiredDeleteSQLServer,
		BatchedExpiredDeleteSQLFor((&sqlserverv2.SQLServerDialect{}).Name()))
	assert.Equal(t, BatchedExpiredDeleteSQL,
		BatchedExpiredDeleteSQLFor((&pgv2.PostgresDialect{}).Name()))
}

// TestTheSweepUsesTSQLOnSQLServer runs the same backlog as TestTheSweepIsNotOneStatement on a
// SQL Server default: the statement changes and nothing else does.
func TestTheSweepUsesTSQLOnSQLServer(t *testing.T) {
	withBatchSize(t, 100)

	repo, executor := sweepRepositoryOn(namedDialect{name: "sqlserver"}, 1000)
	require.NoError(t, repo.DeleteExpired())

	assert.Equal(t, BatchedExpiredDeleteSQLServer, executor.lastSQL)
	assert.Equal(t, 11, executor.calls, "10 full batches, then the short one that ends the loop")
	assert.Equal(t, []any{100}, executor.lastArgs, "the bound is passed as a parameter")
	assert.Zero(t, executor.remaining)
}

// TestTheSQLServerSweepStillStopsAtTheCap: the cap is the loop's, so the T-SQL statement is
// held to it too.
func TestTheSQLServerSweepStillStopsAtTheCap(t *testing.T) {
	withBatchSize(t, 10)

	previous := SessionSweepMaxBatches
	SessionSweepMaxBatches = 3
	t.Cleanup(func() { SessionSweepMaxBatches = previous })

	repo, executor := sweepRepositoryOn(namedDialect{name: "sqlserver"}, 1_000_000)

	err := repo.DeleteExpired()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "next run")
	assert.Equal(t, 3, executor.calls)
	assert.Equal(t, BatchedExpiredDeleteSQLServer, executor.lastSQL)
}

// TestTheSweepKeepsThePortableStatementWithoutADialect covers every way a datasource can fail
// to say it is SQL Server: it has no Dialect method (the stubs above, and any datasource an app
// wrote before the question was asked), its Dialect returns nil, or it names another engine.
func TestTheSweepKeepsThePortableStatementWithoutADialect(t *testing.T) {
	t.Run("no Dialect method", func(t *testing.T) {
		repo, executor := sweepRepository(5, nil)
		require.NoError(t, repo.DeleteExpired())
		assert.Equal(t, BatchedExpiredDeleteSQL, executor.lastSQL)
	})

	t.Run("a nil dialect", func(t *testing.T) {
		repo, executor := sweepRepositoryOn(nil, 5)
		require.NoError(t, repo.DeleteExpired())
		assert.Equal(t, BatchedExpiredDeleteSQL, executor.lastSQL)
	})

	for _, name := range []string{"postgres", "mysql"} {
		t.Run(name, func(t *testing.T) {
			repo, executor := sweepRepositoryOn(namedDialect{name: name}, 5)
			require.NoError(t, repo.DeleteExpired())
			assert.Equal(t, BatchedExpiredDeleteSQL, executor.lastSQL)
		})
	}
}

// TestAPolicyRefusalOnSQLServerStillSendsNothing: the dialect is read after the policy check,
// so a read_only SQL Server default is refused before a statement is chosen or a session
// opened, exactly as any other is.
func TestAPolicyRefusalOnSQLServerStillSendsNothing(t *testing.T) {
	executor := &countingExecutor{remaining: 5}
	dataSource := &policyDialectDataSource{
		policy:  dbCore.DataSourcePolicy{ReadOnly: true},
		dialect: namedDialect{name: "sqlserver"},
		session: &stubSweepSession{executor: executor},
	}
	repo := &DbSessionRepository{dbContext: &stubSweepDbContext{dataSource: dataSource}}

	require.ErrorIs(t, repo.DeleteExpired(), dbCore.ErrReadOnly)
	assert.Zero(t, dataSource.opened, "no session may be opened")
	assert.Zero(t, executor.calls, "and nothing sent")
}

// policyDialectDataSource reports both a policy and a dialect, as the shipped datasources do.
type policyDialectDataSource struct {
	dbCore.IDataSource
	policy  dbCore.DataSourcePolicy
	dialect dbCore.SQLDialect
	session *stubSweepSession
	opened  int
}

func (d *policyDialectDataSource) NewSession() (dbCore.ISession, error) {
	d.opened++
	return d.session, nil
}
func (d *policyDialectDataSource) Policy() dbCore.DataSourcePolicy { return d.policy }
func (d *policyDialectDataSource) Dialect() dbCore.SQLDialect      { return d.dialect }

// --------------------------------------------------------- the attribute column type

// namedDialector is a gorm dialector that can only say its name.
type namedDialector struct {
	gorm.Dialector
	name string
}

func (d namedDialector) Name() string { return d.name }

// TestTheAttributeBagIsUnicodeOnSQLServer: SQL Server's text is deprecated and stores the code
// page of the collation, so a bag declared text there loses every character outside it.
// Postgres and MySQL keep text, which is what their existing tables have.
func TestTheAttributeBagIsUnicodeOnSQLServer(t *testing.T) {
	typeOn := func(dialector gorm.Dialector) string {
		return AttributesMap{}.GormDBDataType(&gorm.DB{Config: &gorm.Config{Dialector: dialector}}, nil)
	}

	assert.Equal(t, "nvarchar(max)", typeOn(namedDialector{name: "sqlserver"}))
	assert.Equal(t, "nvarchar(max)", typeOn(sqlserver.New(sqlserver.Config{})),
		"the name must be the one gorm's SQL Server dialector reports")

	assert.Equal(t, "text", typeOn(postgres.New(postgres.Config{})))
	assert.Equal(t, "text", typeOn(mysql.New(mysql.Config{})))
	assert.Equal(t, "text", typeOn(nil), "a handle without a dialector keeps the old type")
	assert.Equal(t, "text", AttributesMap{}.GormDBDataType(nil, nil))
	assert.Equal(t, "text", AttributesMap{}.GormDBDataType(&gorm.DB{}, nil))
}
