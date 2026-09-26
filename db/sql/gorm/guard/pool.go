package guard

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"gorm.io/gorm"
)

// The send-time check.
//
// gorm builds a statement from a model, and from the fragments an app adds to it — Select,
// Table, Where, Joins, Group, Having, Order, Clauses(clause.Expr{…}), gorm.Expr — in the same
// callback that sends it, so no callback sees that SQL before the server does. What does see
// it is the connection pool gorm sends through: every statement gorm runs, and every
// statement anything sends on a handle's ConnPool directly, is a call to ExecContext,
// QueryContext, QueryRowContext or PrepareContext on it, with the SQL as sent. So an install
// also guards the pools: the handle's own, the transactions begun from it, and whatever pool
// a statement carries when it reaches the guard's callbacks, such as the *sql.Conn of
// db.Connection. The pools refuse, as the statement is sent, what the policies installed on
// the handle refuse: core.GuardExternalSchemaSQL for external_schema, and
// core.GuardReadOnlyShape for read_only. Read-only's is the shape check because the pool
// cannot tell which text an app wrote: SQL set with db.Raw or db.Exec has already had the
// deep check in the callback by then (see InstallReadOnly).

// state is the policy installed on one gorm.Open handle, which its callbacks and its guarded
// pools check. It is kept as a gorm plugin, whose registry every handle derived from that
// handle shares, so a second install, on any of them, finds it.
type state struct {
	policy atomic.Pointer[sendPolicy]
}

// sendPolicy holds the lexicon of each policy installed, and nil for one that is not.
type sendPolicy struct {
	readOnly, externalSchema *dbCore.SQLLexicon
}

const pluginName = "gorgany:guard"

type plugin struct{ state *state }

func (*plugin) Name() string              { return pluginName }
func (*plugin) Initialize(*gorm.DB) error { return nil }

// stateOf returns the state installed on db's handle, installing an empty one first when
// there is none.
func stateOf(db *gorm.DB) (*state, error) {
	if p, ok := db.Config.Plugins[pluginName].(*plugin); ok {
		return p.state, nil
	}
	p := &plugin{state: &state{}}
	if err := db.Use(p); err != nil {
		return nil, err
	}
	return p.state, nil
}

// enable sets one policy's lexicon, unless an earlier install set it already: installing
// again adds nothing.
func (s *state) enable(set func(*sendPolicy) **dbCore.SQLLexicon, lex dbCore.SQLLexicon) {
	for {
		old := s.policy.Load()
		next := &sendPolicy{}
		if old != nil {
			*next = *old
		}
		if *set(next) != nil {
			return
		}
		*set(next) = &lex
		if s.policy.CompareAndSwap(old, next) {
			return
		}
	}
}

func readOnlyPolicy(p *sendPolicy) **dbCore.SQLLexicon       { return &p.readOnly }
func externalSchemaPolicy(p *sendPolicy) **dbCore.SQLLexicon { return &p.externalSchema }

// check refuses sql as it is about to be sent. The SAVEPOINT and ROLLBACK TO that gorm sends
// for a nested Transaction pass the read-only check, as they do in the callbacks.
func (s *state) check(sql string) error {
	p := s.policy.Load()
	if p == nil {
		return nil
	}
	if p.readOnly != nil && !isGormSavepoint(sql) {
		if err := dbCore.GuardReadOnlyShape(sql, *p.readOnly); err != nil {
			return err
		}
	}
	if p.externalSchema != nil {
		if err := dbCore.GuardExternalSchemaSQL(sql, *p.externalSchema); err != nil {
			return err
		}
	}
	return nil
}

// guardHandle guards the pool db was opened with, so that everything that sends on it
// directly — a handle derived from db after this, a transaction begun from one, an
// executor's ExecInsert — is checked as it sends.
func (s *state) guardHandle(db *gorm.DB) {
	opened := db.Config.ConnPool
	if prepared, ok := opened.(*gorm.PreparedStmtDB); ok {
		// With PrepareStmt, gorm looks for its prepared-statement layer by type — SavePoint
		// unwraps it, since a SAVEPOINT cannot be prepared — so it stays outermost and the
		// pool inside it is guarded, in place: gorm.Open keeps other pointers to it.
		prepared.ConnPool = s.wrap(prepared.ConnPool)
		return
	}
	guarded := s.wrap(opened)
	if db.Statement != nil && db.Statement.ConnPool == opened {
		db.Statement.ConnPool = guarded
	}
	db.Config.ConnPool = guarded
}

// guardStatement guards the pool the statement db is running will be sent on, when nothing
// has yet: a handle derived before the install, a transaction begun from one, or the
// *sql.Conn of db.Connection.
func (s *state) guardStatement(db *gorm.DB) {
	if db.Statement != nil {
		db.Statement.ConnPool = s.wrap(db.Statement.ConnPool)
	}
}

// wrap returns cp guarded by s. A pool s already guards comes back as it is. gorm tells a
// transaction from a pool by the interfaces it implements — Commit and Rollback make a
// transaction, and a nested Transaction takes a savepoint on one; BeginTx makes a pool;
// StmtContext lets PrepareStmt reuse a statement in a transaction — so the guarded pool
// implements the ones cp does, and no others.
func (s *state) wrap(cp gorm.ConnPool) gorm.ConnPool {
	switch c := cp.(type) {
	case nil:
		return nil
	case *pool:
		if c.state == s {
			return c
		}
	case *txPool:
		if c.state == s {
			return c
		}
	case *stmtTxPool:
		if c.state == s {
			return c
		}
	case *gorm.PreparedStmtDB:
		// gorm's prepared-statement layers stay outermost; see guardHandle.
		if inner := s.wrap(c.ConnPool); inner != c.ConnPool {
			return &gorm.PreparedStmtDB{ConnPool: inner, Mux: c.Mux, Stmts: c.Stmts}
		}
		return c
	case *gorm.PreparedStmtTX:
		if inner := s.wrap(c.Tx); inner != gorm.ConnPool(c.Tx) {
			return &gorm.PreparedStmtTX{Tx: inner.(gorm.Tx), PreparedStmtDB: c.PreparedStmtDB}
		}
		return c
	}
	g := guarded{inner: cp, state: s}
	switch c := cp.(type) {
	case gorm.Tx:
		return &stmtTxPool{txPool{guarded: g, committer: c}, c}
	case gorm.TxCommitter:
		return &txPool{guarded: g, committer: c}
	}
	return &pool{g}
}

// guarded checks each statement it is asked to send and sends it on inner.
type guarded struct {
	inner gorm.ConnPool
	state *state
}

func (g guarded) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if err := g.state.check(query); err != nil {
		return nil, err
	}
	return g.inner.PrepareContext(ctx, query)
}

func (g guarded) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	if err := g.state.check(query); err != nil {
		return nil, err
	}
	return g.inner.ExecContext(ctx, query, args...)
}

func (g guarded) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	if err := g.state.check(query); err != nil {
		return nil, err
	}
	return g.inner.QueryContext(ctx, query, args...)
}

func (g guarded) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	if err := g.state.check(query); err != nil {
		return refusedRow(err)
	}
	return g.inner.QueryRowContext(ctx, query, args...)
}

// GetDBConn is what gorm's DB() asks a pool it does not know for the *sql.DB behind it, and
// the answer is what DB() would have given for inner.
func (g guarded) GetDBConn() (*sql.DB, error) {
	return (&gorm.DB{Config: &gorm.Config{ConnPool: g.inner}}).DB()
}

// pool is a guarded pool that is not a transaction.
type pool struct{ guarded }

// BeginTx begins a transaction on the pool, guarded like it. A pool that cannot begin one
// answers as gorm does for it, with gorm.ErrInvalidTransaction, which gorm's own default
// transaction takes to mean "run without one".
func (p *pool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	var (
		tx  gorm.ConnPool
		err error
	)
	switch beginner := p.inner.(type) {
	case gorm.TxBeginner:
		var sqlTx *sql.Tx
		if sqlTx, err = beginner.BeginTx(ctx, opts); err == nil {
			tx = sqlTx
		}
	case gorm.ConnPoolBeginner:
		tx, err = beginner.BeginTx(ctx, opts)
	default:
		return nil, gorm.ErrInvalidTransaction
	}
	if err != nil {
		return nil, err
	}
	return p.state.wrap(tx), nil
}

// txPool is a guarded transaction.
type txPool struct {
	guarded
	committer gorm.TxCommitter
}

func (t *txPool) Commit() error   { return t.committer.Commit() }
func (t *txPool) Rollback() error { return t.committer.Rollback() }

// stmtTxPool is a guarded transaction that can take over a prepared statement, as *sql.Tx
// can; gorm's PrepareStmt needs that of a transaction.
type stmtTxPool struct {
	txPool
	tx gorm.Tx
}

// StmtContext hands stmt to the transaction. Its SQL was checked when it was prepared.
func (t *stmtTxPool) StmtContext(ctx context.Context, stmt *sql.Stmt) *sql.Stmt {
	return t.tx.StmtContext(ctx, stmt)
}

// refusedRow returns a *sql.Row whose Scan and Err report err. Only database/sql can make a
// *sql.Row that carries an error, and it makes one when a query's context is done before the
// query gets a connection — so refusedRow asks a pool that never connects, with a context
// that is already done and gives err as the reason. Nothing is sent anywhere.
func refusedRow(err error) *sql.Row {
	return refusalPool().QueryRowContext(refusedContext{err}, "")
}

// refusalPool is the pool refusedRow asks. It is opened once, on the first refusal that needs
// it, and lives as long as the process, as does the goroutine database/sql starts for it.
var refusalPool = sync.OnceValue(func() *sql.DB { return sql.OpenDB(neverConnects{}) })

type neverConnects struct{}

var errNeverConnects = errors.New("guard: this pool never connects")

func (neverConnects) Connect(context.Context) (driver.Conn, error) { return nil, errNeverConnects }
func (neverConnects) Driver() driver.Driver                        { return neverConnects{} }
func (neverConnects) Open(string) (driver.Conn, error)             { return nil, errNeverConnects }

// refusedContext is a context that is done from the start, with err as the reason.
type refusedContext struct{ err error }

var done = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

func (refusedContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (refusedContext) Done() <-chan struct{}       { return done }
func (c refusedContext) Err() error                { return c.err }
func (refusedContext) Value(any) any               { return nil }
