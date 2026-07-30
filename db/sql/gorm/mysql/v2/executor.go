package v2

import (
	"context"
	"fmt"

	"github.com/osbits/gorgany/v2/db/sql/core"

	"gorm.io/gorm"
)

// Executor implements core.IQueryExecutor over a *gorm.DB.
//
// Every method that renders a builder checks ToSQL's error first, so a construct
// MySQL cannot express surfaces as QueryResult.Error naming the construct instead
// of as an opaque driver syntax error.
type Executor struct {
	db *gorm.DB
}

// NewExecutor creates a new query executor.
func NewExecutor(db *gorm.DB) *Executor {
	return &Executor{db: db}
}

// Exec executes a query without returning results.
func (e *Executor) Exec(ctx context.Context, query core.IQueryBuilder) core.QueryResult {
	sql, args, err := query.ToSQL()
	if err != nil {
		return core.QueryResult{Error: err}
	}

	res := e.db.WithContext(ctx).Exec(sql, args...)

	return core.QueryResult{
		Error:        res.Error,
		RowsAffected: res.RowsAffected,
	}
}

// Find executes a query and scans the result into dest.
func (e *Executor) Find(ctx context.Context, query core.IQueryBuilder, dest interface{}) core.QueryResult {
	if dest == nil {
		return core.QueryResult{Error: fmt.Errorf("destination cannot be nil")}
	}

	sql, args, err := query.ToSQL()
	if err != nil {
		return core.QueryResult{Error: err}
	}

	res := e.db.WithContext(ctx).Raw(sql, args...).Scan(dest)

	return core.QueryResult{
		Error:        res.Error,
		RowsAffected: res.RowsAffected,
		Found:        res.RowsAffected > 0,
	}
}

// Count executes a COUNT query.
func (e *Executor) Count(ctx context.Context, query core.IQueryBuilder) (int64, error) {
	sql, args, err := query.ToSQL()
	if err != nil {
		return 0, err
	}

	var count int64
	err = e.db.WithContext(ctx).Raw(sql, args...).Count(&count).Error
	return count, err
}

// ExecRaw executes a raw SQL statement.
func (e *Executor) ExecRaw(ctx context.Context, sql string, args ...interface{}) core.QueryResult {
	res := e.db.WithContext(ctx).Exec(sql, args...)

	return core.QueryResult{
		Error:        res.Error,
		RowsAffected: res.RowsAffected,
	}
}

// FindRaw executes a raw SQL query and scans the result into dest.
func (e *Executor) FindRaw(ctx context.Context, dest interface{}, sql string, args ...interface{}) core.QueryResult {
	if dest == nil {
		return core.QueryResult{Error: fmt.Errorf("destination cannot be nil")}
	}

	db := e.db.WithContext(ctx).Raw(sql, args...)
	err := db.Scan(dest).Error

	return core.QueryResult{
		Error:        err,
		RowsAffected: db.RowsAffected,
		Found:        db.RowsAffected > 0,
	}
}

// CountRaw executes a raw SQL COUNT query.
func (e *Executor) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	var count int64
	err := e.db.WithContext(ctx).Raw(sql, args...).Count(&count).Error
	return count, err
}

// ExecInsert runs q and reports any generated key, implementing
// core.LastInsertIDExecutor.
//
// The key comes from the driver's sql.Result for this very statement, obtained via
// GORM's ConnPool. That matters: on MySQL the value behind LAST_INSERT_ID() is
// connection-scoped, so issuing a separate `SELECT LAST_INSERT_ID()` could be
// served by a different pooled connection and return someone else's key, or zero.
// Reading sql.Result cannot race that way, and inside a transaction ConnPool *is*
// the *sql.Tx, so the read stays transactional.
//
// A driver that does not implement LastInsertId — pgx, for one — leaves
// HasLastInsertID false rather than reporting an error; Postgres reads generated
// values back with RETURNING instead.
func (e *Executor) ExecInsert(ctx context.Context, query core.IQueryBuilder) core.InsertResult {
	sql, args, err := query.ToSQL()
	if err != nil {
		return core.InsertResult{QueryResult: core.QueryResult{Error: err}}
	}

	db := e.db.WithContext(ctx)
	if db.ConnPool == nil {
		// No pool to read a result from; fall back to a plain exec.
		res := db.Exec(sql, args...)
		return core.InsertResult{QueryResult: core.QueryResult{
			Error:        res.Error,
			RowsAffected: res.RowsAffected,
		}}
	}

	result, execErr := db.ConnPool.ExecContext(ctx, sql, args...)
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
