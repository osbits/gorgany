package v2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/osbits/gorgany/v2/db/sql/builder"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// connRecorder is a fake database/sql driver that logs every statement it receives, and the
// transaction boundaries, each tagged with the connection that received it, so a test can see
// which statements shared a connection and so a transaction.
type connRecorder struct {
	mu    sync.Mutex
	conns int
	log   []string
}

func (r *connRecorder) Open(string) (driver.Conn, error) { return r.Connect(context.Background()) }
func (r *connRecorder) Driver() driver.Driver            { return r }

func (r *connRecorder) Connect(context.Context) (driver.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conns++
	return &taggedConn{r: r, id: r.conns}, nil
}

func (r *connRecorder) record(conn int, statement string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, fmt.Sprintf("conn%d %s", conn, statement))
}

func (r *connRecorder) statements() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

type taggedConn struct {
	r  *connRecorder
	id int
}

func (c *taggedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("connRecorder: Prepare is not supported")
}
func (c *taggedConn) Close() error { return nil }
func (c *taggedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *taggedConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.r.record(c.id, "BEGIN")
	return c, nil
}

func (c *taggedConn) Commit() error   { c.r.record(c.id, "COMMIT"); return nil }
func (c *taggedConn) Rollback() error { c.r.record(c.id, "ROLLBACK"); return nil }

func (c *taggedConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.r.record(c.id, query)
	return driver.RowsAffected(1), nil
}

func (c *taggedConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.r.record(c.id, query)
	return emptyRows{}, nil
}

type emptyRows struct{}

func (emptyRows) Columns() []string         { return []string{"id"} }
func (emptyRows) Close() error              { return nil }
func (emptyRows) Next([]driver.Value) error { return io.EOF }

// TestExecInsertInATransactionRunsInTheTransaction: ExecInsert sends straight to a pool, to
// read the driver's result, and inside a transaction that has to be the transaction's
// connection. gorm keeps the handle's own pool (db.ConnPool) the *sql.DB even there; only
// the statement's pool is the *sql.Tx. An INSERT sent on the handle's ran on another
// connection, outside the transaction, and survived its rollback.
func TestExecInsertInATransactionRunsInTheTransaction(t *testing.T) {
	r := &connRecorder{}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(r)}), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); assert.NoError(t, err) {
			assert.NoError(t, sqlDB.Close())
		}
	})

	tx := db.Begin()
	require.NoError(t, tx.Error)
	insert := builder.New(&PostgresDialect{}).Insert("widgets").Columns("name").Values("a")
	require.NoError(t, NewExecutor(tx).ExecInsert(context.Background(), insert).Error)
	require.NoError(t, tx.Exec("UPDATE widgets SET name = 'b'").Error)
	require.NoError(t, tx.Rollback().Error)

	log := r.statements()
	require.Len(t, log, 4, "%q", log)
	assert.Equal(t, "conn1 BEGIN", log[0])
	assert.Regexp(t, "^conn1 INSERT INTO ", log[1], "the INSERT must run on the transaction's connection: %q", log)
	assert.Equal(t, "conn1 UPDATE widgets SET name = 'b'", log[2])
	assert.Equal(t, "conn1 ROLLBACK", log[3])

	// Outside a transaction it goes to the pool, as before.
	require.NoError(t, NewExecutor(db).ExecInsert(context.Background(), insert).Error)
	assert.Regexp(t, "^conn[0-9]+ INSERT INTO ", r.statements()[4])
}
