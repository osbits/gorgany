package v2

import (
	"context"
	"fmt"

	"git.qix.sx/gorgany/gorgany.git/db/sql/core"

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
	sql, args := query.ToSQL()

	res := e.db.Exec(sql, args...)

	queryResult := core.QueryResult{
		Error:        res.Error,
		RowsAffected: res.RowsAffected,
	}

	return queryResult
}

// Find executes a query and stores the result in the provided destination
func (e *Executor) Find(ctx context.Context, query core.IQueryBuilder, result interface{}) core.QueryResult {
	sql, args := query.ToSQL()

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
	sql, args := query.ToSQL()
	var count int64
	err := e.db.Raw(sql, args...).Count(&count).Error
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
