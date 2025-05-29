package v2

import (
	"context"
	"fmt"

	"git.qix.sx/gorgany/gorgany.git/db/gorm/postgres/v2/core"
)

// transactionImpl implements the IDBTransaction interface
type transactionImpl struct {
	*Builder
	*Executor
}

// Query creates a new query builder
func (t *transactionImpl) Query() core.IQueryBuilder {
	return t.Builder
}

// Find executes a query and stores the results in the provided destination
func (t *transactionImpl) Find(ctx context.Context, result interface{}, conditions ...core.Condition) error {
	query := t.Query().
		Where(And(conditions...)).
		Build()

	return t.ExecuteWithResult(ctx, query, result)
}

// FindOne executes a query and stores the first result in the provided destination
func (t *transactionImpl) FindOne(ctx context.Context, result interface{}, conditions ...core.Condition) error {
	query := t.Query().
		Where(And(conditions...)).
		Limit(1).
		Build()

	return t.ExecuteWithResult(ctx, query, result)
}

// Count returns the number of records matching the conditions
func (t *transactionImpl) Count(ctx context.Context, table string, conditions ...core.Condition) (int64, error) {
	query := t.Query().
		From(table).
		Where(And(conditions...)).
		Build()

	return t.Executor.Count(ctx, query)
}

// Insert inserts a new record
func (t *transactionImpl) Insert(ctx context.Context, table string, data interface{}) error {
	query := t.Query().
		From(table).
		Build()

	return t.Execute(ctx, query)
}

// Update updates records matching the conditions
func (t *transactionImpl) Update(ctx context.Context, table string, data interface{}, conditions ...core.Condition) error {
	query := t.Query().
		From(table).
		Where(And(conditions...)).
		Build()

	return t.Execute(ctx, query)
}

// Delete deletes records matching the conditions
func (t *transactionImpl) Delete(ctx context.Context, table string, conditions ...core.Condition) error {
	query := t.Query().
		From(table).
		Where(And(conditions...)).
		Build()

	return t.Execute(ctx, query)
}

// ExecuteRaw executes a raw SQL query without returning results
func (t *transactionImpl) ExecuteRaw(ctx context.Context, sql string, args ...interface{}) error {
	return t.Executor.ExecuteRaw(ctx, sql, args...)
}

// ExecuteRawWithResult executes a raw SQL query and stores the results in the provided destination
func (t *transactionImpl) ExecuteRawWithResult(ctx context.Context, result interface{}, sql string, args ...interface{}) error {
	return t.Executor.ExecuteRawWithResult(ctx, result, sql, args...)
}

// CountRaw executes a raw SQL COUNT query
func (t *transactionImpl) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	return t.Executor.CountRaw(ctx, sql, args...)
}

// Helper methods for joins
func (t *transactionImpl) InnerJoin(table string, condition core.Condition) core.IQueryBuilder {
	return t.Join(&core.JoinClause{
		Type:      "INNER",
		Table:     table,
		Condition: condition,
	})
}

func (t *transactionImpl) LeftJoin(table string, condition core.Condition) core.IQueryBuilder {
	return t.Join(&core.JoinClause{
		Type:      "LEFT",
		Table:     table,
		Condition: condition,
	})
}

func (t *transactionImpl) RightJoin(table string, condition core.Condition) core.IQueryBuilder {
	return t.Join(&core.JoinClause{
		Type:      "RIGHT",
		Table:     table,
		Condition: condition,
	})
}

func (t *transactionImpl) FullJoin(table string, condition core.Condition) core.IQueryBuilder {
	return t.Join(&core.JoinClause{
		Type:      "FULL",
		Table:     table,
		Condition: condition,
	})
}

func (t *transactionImpl) CrossJoin(table string) core.IQueryBuilder {
	return t.Join(&core.JoinClause{
		Type:  "CROSS",
		Table: table,
	})
}

func (t *transactionImpl) NaturalJoin(table string) core.IQueryBuilder {
	return t.Join(&core.JoinClause{
		Type:  "NATURAL",
		Table: table,
	})
}

// Helper methods for group by
func (t *transactionImpl) GroupingSets(sets ...[]string) core.IQueryBuilder {
	var allFields []string
	for _, set := range sets {
		allFields = append(allFields, set...)
	}
	return t.GroupBy(allFields...)
}

func (t *transactionImpl) Rollup(fields ...string) core.IQueryBuilder {
	return t.GroupBy(fields...)
}

func (t *transactionImpl) Cube(fields ...string) core.IQueryBuilder {
	return t.GroupBy(fields...)
}

// Helper method for window functions
func (t *transactionImpl) Over(name string) string {
	return fmt.Sprintf("OVER (%s)", name)
}
