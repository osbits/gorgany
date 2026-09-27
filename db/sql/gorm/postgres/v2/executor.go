package v2

import (
	"context"
	"fmt"

	"github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/gorm/internal/rendered"

	"gorm.io/gorm"
)

// Executor implements the QueryExecutor interface
//
// The methods that take a builder — Exec, Find, Count and ExecInsert — mark the statement
// they send as rendered by a gorgany dialect (see rendered.Mark), and the Raw ones do not. On
// a read_only datasource the guard then checks a builder's SQL for its shape alone, which is
// what a dialect's structure can get wrong, and not for words: the Postgres dialect writes
// identifiers unquoted, so a column named lock or copy would otherwise make a builder read
// unrunnable. SQL an app wrote gets every word checked.
type Executor struct {
	db *gorm.DB
}

// NewExecutor creates a new query executor
func NewExecutor(db *gorm.DB) *Executor {
	return &Executor{db: db}
}

// Exec executes a query without returning results
func (e *Executor) Exec(ctx context.Context, query core.IQueryBuilder) core.QueryResult {
	sql, args, err := query.ToSQL()
	if err != nil {
		return core.QueryResult{Error: err}
	}

	res := e.builderHandle().Exec(sql, args...)

	queryResult := core.QueryResult{
		Error:        res.Error,
		RowsAffected: res.RowsAffected,
	}

	return queryResult
}

// Find executes a query and stores the result in the provided destination
func (e *Executor) Find(ctx context.Context, query core.IQueryBuilder, result interface{}) core.QueryResult {
	sql, args, err := query.ToSQL()
	if err != nil {
		return core.QueryResult{Error: err}
	}

	res := e.builderHandle().Raw(sql, args...).Scan(result)

	queryResult := core.QueryResult{
		Error:        res.Error,
		RowsAffected: res.RowsAffected,
		Found:        res.RowsAffected > 0,
	}

	return queryResult
}

// Count executes a COUNT query
func (e *Executor) Count(ctx context.Context, query core.IQueryBuilder) (int64, error) {
	sql, args, err := query.ToSQL()
	if err != nil {
		return 0, err
	}
	var count int64
	err = e.builderHandle().Raw(sql, args...).Count(&count).Error
	return count, err
}

// builderHandle returns a handle whose next statement is marked as dialect-rendered SQL.
//
// The mark goes on a handle derived for the purpose, never on e.db: when e.db has already
// begun a statement, gorm runs the next call on that same statement, and a mark on it would
// give SQL an app later sends on e.db the lighter check. Session, rather than WithContext,
// derives it without changing which context the statement runs under, since this executor's
// Exec, Find and Count have always run under e.db's.
func (e *Executor) builderHandle() *gorm.DB {
	return rendered.Mark(e.db.Session(&gorm.Session{}))
}

// ExecRaw executes a raw SQL query without returning results
func (e *Executor) ExecRaw(ctx context.Context, sql string, args ...interface{}) core.QueryResult {
	res := e.db.Exec(sql, args...)

	queryResult := core.QueryResult{
		Error:        res.Error,
		RowsAffected: res.RowsAffected,
	}

	return queryResult
}

// FindRaw executes a raw SQL query and stores the results in the provided destination
func (e *Executor) FindRaw(ctx context.Context, result interface{}, sql string, args ...interface{}) core.QueryResult {
	if result == nil {
		return core.QueryResult{Error: fmt.Errorf("destination cannot be nil")}
	}

	db := e.db.Raw(sql, args...)
	err := db.Scan(result).Error

	queryResult := core.QueryResult{
		Error:        err,
		RowsAffected: db.RowsAffected,
		Found:        db.RowsAffected > 0,
	}

	return queryResult
}

// CountRaw executes a raw SQL COUNT query
func (e *Executor) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	var count int64
	err := e.db.Raw(sql, args...).Count(&count).Error
	return count, err
}

// ExecInsert runs q and reports any generated key, implementing
// core.LastInsertIDExecutor.
//
// The key comes from the driver's sql.Result for this very statement, obtained by
// sending it on the statement's pool, Statement.ConnPool. That matters: on MySQL the
// value behind LAST_INSERT_ID() is connection-scoped, so issuing a separate
// `SELECT LAST_INSERT_ID()` could be served by a different pooled connection and
// return someone else's key, or zero. Reading sql.Result cannot race that way.
//
// It is the statement's pool, not the handle's. The handle's, db.ConnPool, stays the
// *sql.DB it was opened with even inside a transaction; the statement's is the
// *sql.Tx there, so the INSERT runs in the transaction and is rolled back with it.
//
// A driver that does not implement LastInsertId — pgx, for one — leaves
// HasLastInsertID false rather than reporting an error; Postgres reads generated
// values back with RETURNING instead.
//
// Sending on the pool goes past gorm's callbacks, but not past a guard from
// db/sql/gorm/guard, which guards the pool too: the INSERT is checked as it is sent, and on a
// read_only datasource refused there, if a dialect that is not read-only rendered it.
func (e *Executor) ExecInsert(ctx context.Context, query core.IQueryBuilder) core.InsertResult {
	sql, args, err := query.ToSQL()
	if err != nil {
		return core.InsertResult{QueryResult: core.QueryResult{Error: err}}
	}

	db := rendered.Mark(e.db.WithContext(ctx))
	pool := db.Statement.ConnPool
	if pool == nil {
		// No pool to read a result from; fall back to a plain exec.
		res := db.Exec(sql, args...)
		return core.InsertResult{QueryResult: core.QueryResult{
			Error:        res.Error,
			RowsAffected: res.RowsAffected,
		}}
	}

	result, execErr := pool.ExecContext(ctx, sql, args...)
	if execErr != nil {
		return core.InsertResult{QueryResult: core.QueryResult{Error: execErr}}
	}

	insertResult := core.InsertResult{}
	if affected, affErr := result.RowsAffected(); affErr == nil {
		insertResult.RowsAffected = affected
	}
	if lastID, idErr := result.LastInsertId(); idErr == nil && lastID != 0 {
		insertResult.LastInsertID = lastID
		insertResult.HasLastInsertID = true
	}
	return insertResult
}
