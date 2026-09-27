package v2

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeServer is a database/sql driver that stands in for SQL Server behind gorm's SQL Server
// dialector, the way the datasource opens it: sqlserver.New(Config{Conn: sql.OpenDB(…)}). It
// records every statement with the arguments database/sql handed it, and answers each with
// what respond says, which may be several result sets, as a batch with a tail is.
//
// It implements driver.NamedValueChecker and accepts every argument as it is, so a test sees
// exactly what gorm bound — a byteArg, not the bytes a real driver would convert it to.
type fakeServer struct {
	mu      sync.Mutex
	calls   []fakeCall
	respond func(query string) fakeResponse

	beginContexts []context.Context
	commitErr     error
	rollbackErr   error
}

type fakeCall struct {
	kind  string // query, exec, begin, commit or rollback
	query string
	args  []any
}

// fakeResponse is one statement's answer.
type fakeResponse struct {
	sets     []fakeSet
	err      error // from QueryContext or ExecContext
	nextErr  error // from Next, after the last row of the last set
	affected int64 // ExecContext's row count
}

type fakeSet struct {
	cols []string
	rows [][]driver.Value
}

// tail is the result set of a row-count tail reporting n rows.
func tail(n int64) fakeSet {
	return fakeSet{cols: []string{"affected"}, rows: [][]driver.Value{{n}}}
}

func (f *fakeServer) Connect(context.Context) (driver.Conn, error) { return &fakeConn{server: f}, nil }
func (f *fakeServer) Driver() driver.Driver                        { return f }
func (f *fakeServer) Open(string) (driver.Conn, error)             { return &fakeConn{server: f}, nil }

func (f *fakeServer) record(kind, query string, args []driver.NamedValue) fakeResponse {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}

	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{kind: kind, query: query, args: values})
	respond := f.respond
	f.mu.Unlock()

	if respond == nil || kind == "begin" || kind == "commit" || kind == "rollback" {
		return fakeResponse{}
	}
	return respond(query)
}

// log returns every call so far, as "kind query".
func (f *fakeServer) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines := make([]string, len(f.calls))
	for i, call := range f.calls {
		lines[i] = strings.TrimSpace(call.kind + " " + call.query)
	}
	return lines
}

// statements returns the calls that sent SQL.
func (f *fakeServer) statements() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var sent []fakeCall
	for _, call := range f.calls {
		if call.kind == "query" || call.kind == "exec" {
			sent = append(sent, call)
		}
	}
	return sent
}

// only returns the one statement sent so far, failing the test when there was not exactly one.
func (f *fakeServer) only(t *testing.T) fakeCall {
	t.Helper()
	sent := f.statements()
	require.Len(t, sent, 1, "%q", f.log())
	return sent[0]
}

type fakeConn struct{ server *fakeServer }

var (
	_ driver.QueryerContext    = (*fakeConn)(nil)
	_ driver.ExecerContext     = (*fakeConn)(nil)
	_ driver.ConnBeginTx       = (*fakeConn)(nil)
	_ driver.NamedValueChecker = (*fakeConn)(nil)
)

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakeServer: Prepare is not supported")
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *fakeConn) CheckNamedValue(*driver.NamedValue) error { return nil }

func (c *fakeConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.server.record("begin", "", nil)
	c.server.mu.Lock()
	c.server.beginContexts = append(c.server.beginContexts, ctx)
	c.server.mu.Unlock()
	return &fakeTx{server: c.server}, nil
}

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	resp := c.server.record("query", query, args)
	if resp.err != nil {
		return nil, resp.err
	}
	return &fakeRows{sets: resp.sets, nextErr: resp.nextErr}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	resp := c.server.record("exec", query, args)
	if resp.err != nil {
		return nil, resp.err
	}
	return driver.RowsAffected(resp.affected), nil
}

type fakeTx struct{ server *fakeServer }

func (tx *fakeTx) Commit() error {
	tx.server.record("commit", "", nil)
	return tx.server.commitErr
}

func (tx *fakeTx) Rollback() error {
	tx.server.record("rollback", "", nil)
	return tx.server.rollbackErr
}

// fakeRows walks a response's result sets as go-mssqldb's rows do: Next returns io.EOF at the
// end of each, and NextResultSet moves to the next.
type fakeRows struct {
	sets    []fakeSet
	set     int
	row     int
	nextErr error
}

var _ driver.RowsNextResultSet = (*fakeRows)(nil)

func (r *fakeRows) Columns() []string {
	if r.set >= len(r.sets) {
		return nil
	}
	return r.sets[r.set].cols
}

func (r *fakeRows) Close() error { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.set < len(r.sets) && r.row < len(r.sets[r.set].rows) {
		copy(dest, r.sets[r.set].rows[r.row])
		r.row++
		return nil
	}
	if !r.HasNextResultSet() && r.nextErr != nil {
		err := r.nextErr
		r.nextErr = nil
		return err
	}
	return io.EOF
}

func (r *fakeRows) HasNextResultSet() bool { return r.set+1 < len(r.sets) }

func (r *fakeRows) NextResultSet() error {
	if !r.HasNextResultSet() {
		return io.EOF
	}
	r.set++
	r.row = 0
	return nil
}

// fakeGorm opens gorm on server as the datasource opens it on go-mssqldb, with the same
// callbacks installed, and closes it when the test ends.
func fakeGorm(t *testing.T, server *fakeServer, readOnly, externalSchema bool) *gorm.DB {
	t.Helper()

	sqlDB := sql.OpenDB(server)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })

	db, err := openGorm(sqlDB, readOnly, externalSchema)
	require.NoError(t, err)
	return db
}

// fakeExecutor is an Executor over fakeGorm.
func fakeExecutor(t *testing.T, server *fakeServer) *Executor {
	t.Helper()
	return NewExecutor(fakeGorm(t, server, false, false))
}
