package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ------------------------------------------------------ a recording SQL driver

// recording is the state of a fake database/sql driver: the statements it received, in
// order, and the failures a test asked for. A statement run on a transaction is logged
// as "tx: <sql>" and one run on the pool as "pool: <sql>", so where the bookkeeping row
// was written is visible in the log.
type recording struct {
	log       []string
	applied   map[string]bool // names the migrations table already holds
	beginErr  error
	commitErr error
	failExec  string // an Exec whose SQL contains this fails
	failQuery string // a Query whose SQL contains this fails
}

var errInjected = errors.New("injected failure")

func (r *recording) Open(string) (driver.Conn, error) { return &recordingConn{r: r}, nil }
func (r *recording) Connect(context.Context) (driver.Conn, error) {
	return &recordingConn{r: r}, nil
}
func (r *recording) Driver() driver.Driver { return r }

type recordingConn struct {
	r    *recording
	inTx bool
}

func (c *recordingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("recording driver: Prepare is not supported")
}
func (c *recordingConn) Close() error { return nil }
func (c *recordingConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *recordingConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if c.r.beginErr != nil {
		return nil, c.r.beginErr
	}
	c.r.log = append(c.r.log, "BEGIN")
	c.inTx = true
	return c, nil
}

func (c *recordingConn) Commit() error {
	c.inTx = false
	c.r.log = append(c.r.log, "COMMIT")
	return c.r.commitErr
}

func (c *recordingConn) Rollback() error {
	c.inTx = false
	c.r.log = append(c.r.log, "ROLLBACK")
	return nil
}

func (c *recordingConn) where() string {
	if c.inTx {
		return "tx: "
	}
	return "pool: "
}

func (c *recordingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.r.log = append(c.r.log, c.where()+query)
	if c.r.failExec != "" && strings.Contains(query, c.r.failExec) {
		return nil, errInjected
	}
	return driver.RowsAffected(1), nil
}

func (c *recordingConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.r.log = append(c.r.log, c.where()+query)
	if c.r.failQuery != "" && strings.Contains(query, c.r.failQuery) {
		return nil, errInjected
	}

	rows := &recordingRows{}
	if len(args) > 0 {
		if name, ok := args[0].Value.(string); ok && c.r.applied[name] {
			rows.values = [][]driver.Value{{name, time.Now()}}
		}
	}
	return rows, nil
}

// recordingRows answers the applied-migration lookup: one row when the name is recorded.
type recordingRows struct{ values [][]driver.Value }

func (r *recordingRows) Columns() []string { return []string{"name", "date"} }
func (r *recordingRows) Close() error      { return nil }
func (r *recordingRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

// recordingGorm returns a GORM handle, with the PostgreSQL dialect, over r.
func recordingGorm(t *testing.T, r *recording) *gorm.DB {
	t.Helper()

	gormDb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(r)}), &gorm.Config{
		Logger: logger.Discard,
	})
	require.NoError(t, err)
	return gormDb
}

// withoutReads drops the applied-migration lookups from a log, leaving the writes and
// the transaction boundaries the tests assert on.
func withoutReads(log []string) []string {
	var out []string
	for _, entry := range log {
		if !strings.Contains(entry, "SELECT") {
			out = append(out, entry)
		}
	}
	return out
}

const recordSQL = `tx: INSERT INTO "migrations" ("name","date") VALUES ($1,$2)`

// execMigration is a migration whose Up runs one statement, or fails without running any.
type execMigration struct {
	name  string
	sql   string
	upErr error
}

func (m execMigration) Name() string { return m.name }
func (m execMigration) Up() core.MigrationClosure {
	return func(g *gorm.DB) error {
		if m.upErr != nil {
			return m.upErr
		}
		return g.Exec(m.sql).Error
	}
}
func (m execMigration) Down() core.MigrationClosure {
	return func(*gorm.DB) error { return errors.New("not reversible") }
}

// ------------------------------------------------------------ applyMigration

// TestApplyMigrationRecordsInsideTheTransaction: the row used to be written after the
// commit, on the pool, so the two could disagree. It now sits between BEGIN and COMMIT.
func TestApplyMigrationRecordsInsideTheTransaction(t *testing.T) {
	r := &recording{}

	err := applyMigration(recordingGorm(t, r), execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"})

	require.NoError(t, err)
	assert.Equal(t, []string{"BEGIN", "tx: CREATE TABLE widgets (id int)", recordSQL, "COMMIT"}, r.log)
}

// TestApplyMigrationReportsAFailedCommit: the commit's error used to be discarded, so a
// migration whose COMMIT failed was recorded as applied and the run exited 0.
func TestApplyMigrationReportsAFailedCommit(t *testing.T) {
	r := &recording{commitErr: errInjected}

	err := applyMigration(recordingGorm(t, r), execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"})

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "cannot commit migration m1")
	assert.Equal(t, []string{"BEGIN", "tx: CREATE TABLE widgets (id int)", recordSQL, "COMMIT"}, r.log,
		"the row must have been part of the transaction that failed to commit")
}

// TestApplyMigrationRollsBackWhenTheRowCannotBeWritten: the insert's error used to be
// unchecked, leaving an applied migration unrecorded, to run again next time.
func TestApplyMigrationRollsBackWhenTheRowCannotBeWritten(t *testing.T) {
	r := &recording{failExec: `INSERT INTO "migrations"`}

	err := applyMigration(recordingGorm(t, r), execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"})

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "migration m1 ran but could not be recorded")
	assert.Equal(t, []string{"BEGIN", "tx: CREATE TABLE widgets (id int)", recordSQL, "ROLLBACK"}, r.log)
}

func TestApplyMigrationRollsBackAFailedUp(t *testing.T) {
	r := &recording{}

	err := applyMigration(recordingGorm(t, r), execMigration{name: "m1", upErr: errInjected})

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "migration m1 failed")
	assert.Equal(t, []string{"BEGIN", "ROLLBACK"}, r.log, "nothing may be recorded")
}

func TestApplyMigrationReportsAFailedBegin(t *testing.T) {
	r := &recording{beginErr: errInjected}

	err := applyMigration(recordingGorm(t, r), execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"})

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "cannot begin a transaction for migration m1")
	assert.Empty(t, r.log)
}

// ------------------------------------------------------------ applyPending

func TestApplyPendingSkipsRecordedMigrationsAndStopsAtTheFirstFailure(t *testing.T) {
	r := &recording{applied: map[string]bool{"m1": true}}

	err := MigrateCommand{}.applyPending(recordingGorm(t, r), []core.IMigration{
		execMigration{name: "m1", sql: "CREATE TABLE one (id int)"},
		execMigration{name: "m2", sql: "CREATE TABLE two (id int)"},
		execMigration{name: "m3", upErr: errInjected},
		execMigration{name: "m4", sql: "CREATE TABLE four (id int)"},
	})

	require.ErrorIs(t, err, errInjected)
	assert.Equal(t, []string{
		"BEGIN", "tx: CREATE TABLE two (id int)", recordSQL, "COMMIT",
		"BEGIN", "ROLLBACK",
	}, withoutReads(r.log), "m1 is recorded, m2 applies, m3 fails, and m4 never starts")
}

// TestApplyPendingReportsAFailedLookup: a failed read of the migrations table used to
// count as "not applied", so the migration ran again.
func TestApplyPendingReportsAFailedLookup(t *testing.T) {
	r := &recording{failQuery: `FROM "migrations"`}

	err := MigrateCommand{}.applyPending(recordingGorm(t, r), []core.IMigration{
		execMigration{name: "m1", sql: "CREATE TABLE one (id int)"},
	})

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "cannot read whether migration m1 is applied")
	assert.Empty(t, withoutReads(r.log), "nothing may run")
}
