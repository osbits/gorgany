package v2

import (
	"context"
	"fmt"

	"git.qix.sx/gorgany/gorgany.git/db/gorm/postgres/v2/core"
	"gorm.io/gorm"
)

// Database is the main facade for database operations
type Database struct {
	executor *Executor
}

// NewDatabase creates a new database instance
func NewDatabase(db *gorm.DB) *Database {
	return &Database{
		executor: NewExecutor(db),
	}
}

// Query creates a new query builder
func (d *Database) Query() core.QueryBuilder {
	return NewBuilder()
}

// Find executes a query and stores the results in the provided destination
func (d *Database) Find(ctx context.Context, result interface{}, conditions ...core.Condition) error {
	query := d.Query().
		Where(And(conditions...)).
		Build()

	return d.executor.ExecuteWithResult(ctx, query, result)
}

// FindOne executes a query and stores the first result in the provided destination
func (d *Database) FindOne(ctx context.Context, result interface{}, conditions ...core.Condition) error {
	query := d.Query().
		Where(And(conditions...)).
		Limit(1).
		Build()

	return d.executor.ExecuteWithResult(ctx, query, result)
}

// Count returns the number of records matching the conditions
func (d *Database) Count(ctx context.Context, table string, conditions ...core.Condition) (int64, error) {
	query := d.Query().
		From(table).
		Where(And(conditions...)).
		Build()

	return d.executor.Count(ctx, query)
}

// Insert inserts a new record
func (d *Database) Insert(ctx context.Context, table string, data interface{}) error {
	query := d.Query().
		From(table).
		Build()

	return d.executor.Execute(ctx, query)
}

// Update updates records matching the conditions
func (d *Database) Update(ctx context.Context, table string, data interface{}, conditions ...core.Condition) error {
	query := d.Query().
		From(table).
		Where(And(conditions...)).
		Build()

	return d.executor.Execute(ctx, query)
}

// Delete deletes records matching the conditions
func (d *Database) Delete(ctx context.Context, table string, conditions ...core.Condition) error {
	query := d.Query().
		From(table).
		Where(And(conditions...)).
		Build()

	return d.executor.Execute(ctx, query)
}

// Transaction executes the provided function within a transaction
func (d *Database) Transaction(ctx context.Context, fn func(core.Transaction) error) error {
	tx := d.executor.db.Begin()
	if tx.Error != nil {
		return tx.Error
	}

	transaction := &TransactionImpl{
		tx:       tx,
		Builder:  NewBuilder(),
		Executor: NewExecutor(tx),
	}

	if err := fn(transaction); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// TransactionImpl implements the Transaction interface
type TransactionImpl struct {
	tx *gorm.DB
	*Builder
	*Executor
}

// NewTransaction creates a new transaction
func NewTransaction(tx *gorm.DB) *TransactionImpl {
	return &TransactionImpl{
		tx:       tx,
		Builder:  NewBuilder(),
		Executor: NewExecutor(tx),
	}
}

// Commit commits the transaction
func (t *TransactionImpl) Commit() error {
	return t.tx.Commit().Error
}

// Rollback rolls back the transaction
func (t *TransactionImpl) Rollback() error {
	return t.tx.Rollback().Error
}

// Execute executes a query without returning results
func (t *TransactionImpl) Execute(ctx context.Context, query *core.Query) error {
	return t.Executor.Execute(ctx, query)
}

// ExecuteWithResult executes a query and stores the results in the provided destination
func (t *TransactionImpl) ExecuteWithResult(ctx context.Context, query *core.Query, result interface{}) error {
	return t.Executor.ExecuteWithResult(ctx, query, result)
}

// Count executes a COUNT query
func (t *TransactionImpl) Count(ctx context.Context, query *core.Query) (int64, error) {
	return t.Executor.Count(ctx, query)
}

// ExecuteRaw executes a raw SQL query without returning results
func (t *TransactionImpl) ExecuteRaw(ctx context.Context, sql string, args ...interface{}) error {
	return t.Executor.ExecuteRaw(ctx, sql, args...)
}

// ExecuteRawWithResult executes a raw SQL query and stores the results in the provided destination
func (t *TransactionImpl) ExecuteRawWithResult(ctx context.Context, result interface{}, sql string, args ...interface{}) error {
	return t.Executor.ExecuteRawWithResult(ctx, result, sql, args...)
}

// CountRaw executes a raw SQL COUNT query
func (t *TransactionImpl) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	return t.Executor.CountRaw(ctx, sql, args...)
}

// Database methods for raw SQL execution
func (d *Database) ExecuteRaw(ctx context.Context, sql string, args ...interface{}) error {
	return d.executor.ExecuteRaw(ctx, sql, args...)
}

func (d *Database) ExecuteRawWithResult(ctx context.Context, result interface{}, sql string, args ...interface{}) error {
	return d.executor.ExecuteRawWithResult(ctx, result, sql, args...)
}

func (d *Database) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	return d.executor.CountRaw(ctx, sql, args...)
}

// Helper methods for joins
func (t *TransactionImpl) InnerJoin(table string, condition core.Condition) core.QueryBuilder {
	return t.Join(&core.JoinClause{
		Type:      "INNER",
		Table:     table,
		Condition: condition,
	})
}

func (t *TransactionImpl) LeftJoin(table string, condition core.Condition) core.QueryBuilder {
	return t.Join(&core.JoinClause{
		Type:      "LEFT",
		Table:     table,
		Condition: condition,
	})
}

func (t *TransactionImpl) RightJoin(table string, condition core.Condition) core.QueryBuilder {
	return t.Join(&core.JoinClause{
		Type:      "RIGHT",
		Table:     table,
		Condition: condition,
	})
}

func (t *TransactionImpl) FullJoin(table string, condition core.Condition) core.QueryBuilder {
	return t.Join(&core.JoinClause{
		Type:      "FULL",
		Table:     table,
		Condition: condition,
	})
}

func (t *TransactionImpl) CrossJoin(table string) core.QueryBuilder {
	return t.Join(&core.JoinClause{
		Type:  "CROSS",
		Table: table,
	})
}

func (t *TransactionImpl) NaturalJoin(table string) core.QueryBuilder {
	return t.Join(&core.JoinClause{
		Type:  "NATURAL",
		Table: table,
	})
}

// Helper methods for group by
func (t *TransactionImpl) GroupingSets(sets ...[]string) core.QueryBuilder {
	var allFields []string
	for _, set := range sets {
		allFields = append(allFields, set...)
	}
	return t.GroupBy(allFields...)
}

func (t *TransactionImpl) Rollup(fields ...string) core.QueryBuilder {
	return t.GroupBy(fields...)
}

func (t *TransactionImpl) Cube(fields ...string) core.QueryBuilder {
	return t.GroupBy(fields...)
}

// Helper method for window functions
func (t *TransactionImpl) Over(name string) string {
	return fmt.Sprintf("OVER (%s)", name)
}
