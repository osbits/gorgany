package v2

import (
	"context"
	"fmt"

	"github.com/osbits/gorgany/db/sql/core"

	"gorm.io/gorm"
)

// Executor implements the QueryExecutor interface
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

	res := e.db.Exec(sql, args...)

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

	res := e.db.Raw(sql, args...).Scan(result)

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
	err = e.db.Raw(sql, args...).Count(&count).Error
	return count, err
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
