package core

import (
	"context"
)

// QueryBuilder is the main interface for building queries
type QueryBuilder interface {
	// Basic query building methods
	Select(fields ...string) QueryBuilder
	From(table string) QueryBuilder
	Where(condition Condition) QueryBuilder
	Join(join *JoinClause) QueryBuilder
	OrderBy(field string, direction string) QueryBuilder
	GroupBy(fields ...string) QueryBuilder
	Having(condition Condition) QueryBuilder
	Limit(limit int) QueryBuilder
	Offset(offset int) QueryBuilder

	// Advanced query building methods
	WithCTE(name string, query *Query) QueryBuilder
	Union(query *Query) QueryBuilder
	UnionAll(query *Query) QueryBuilder
	Window(name string, definition *WindowDefinition) QueryBuilder
	Subquery(query *Query, alias string) QueryBuilder
	LateralJoin(query *Query, alias string, condition Condition) QueryBuilder
	DistinctOn(fields ...string) QueryBuilder
	Returning(fields ...string) QueryBuilder

	// Join helper methods
	InnerJoin(table string, condition Condition) QueryBuilder
	LeftJoin(table string, condition Condition) QueryBuilder
	RightJoin(table string, condition Condition) QueryBuilder
	FullJoin(table string, condition Condition) QueryBuilder
	CrossJoin(table string) QueryBuilder
	NaturalJoin(table string) QueryBuilder

	// Group by helper methods
	GroupingSets(sets ...[]string) QueryBuilder
	Rollup(fields ...string) QueryBuilder
	Cube(fields ...string) QueryBuilder

	// Window function helper methods
	Over(name string) string

	// Query finalization
	Build() *Query
	ToSQL() string
}

// DatabaseFeatures represents database-specific capabilities
type DatabaseFeatures interface {
	// Join support
	SupportsJoinType(joinType string) bool
	SupportsWindowFunctions() bool
	SupportsCTE() bool
	SupportsUnion() bool
	SupportsDistinctOn() bool
	SupportsReturning() bool
	SupportsJSONOperations() bool
	SupportsArrayOperations() bool
	SupportsFullTextSearch() bool
}

// QueryExecutor handles the execution of queries
type QueryExecutor interface {
	// Execute executes a query without returning results
	Execute(ctx context.Context, query *Query) error
	// ExecuteWithResult executes a query and stores the results in the provided destination
	ExecuteWithResult(ctx context.Context, query *Query, result interface{}) error
	// Count executes a COUNT query
	Count(ctx context.Context, query *Query) (int64, error)
	// ExecuteRaw executes a raw SQL query without returning results
	ExecuteRaw(ctx context.Context, sql string, args ...interface{}) error
	// ExecuteRawWithResult executes a raw SQL query and stores the results in the provided destination
	ExecuteRawWithResult(ctx context.Context, result interface{}, sql string, args ...interface{}) error
	// CountRaw executes a raw SQL COUNT query
	CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error)
}

// TransactionManager handles database transactions
type TransactionManager interface {
	// Begin starts a new transaction
	Begin(ctx context.Context) (Transaction, error)
	// WithTransaction executes a function within a transaction
	WithTransaction(ctx context.Context, fn func(Transaction) error) error
}

// Transaction represents a database transaction
type Transaction interface {
	QueryBuilder
	QueryExecutor
	// Commit commits the transaction
	Commit() error
	// Rollback rolls back the transaction
	Rollback() error
}

// SQLDialect handles SQL formatting for a specific database
type SQLDialect interface {
	// FormatSelect formats the SELECT clause
	FormatSelect(fields []string, distinct bool, distinctOn []string) string
	// FormatFrom formats the FROM clause
	FormatFrom(table string, alias string) string
	// FormatJoin formats a JOIN clause
	FormatJoin(join *JoinClause) string
	// FormatWhere formats the WHERE clause
	FormatWhere(condition Condition) string
	// FormatOrderBy formats the ORDER BY clause
	FormatOrderBy(field string, direction string) string
	// FormatGroupBy formats the GROUP BY clause
	FormatGroupBy(fields []string) string
	// FormatHaving formats the HAVING clause
	FormatHaving(condition Condition) string
	// FormatLimit formats the LIMIT clause
	FormatLimit(limit int) string
	// FormatOffset formats the OFFSET clause
	FormatOffset(offset int) string
	// FormatCTE formats a Common Table Expression
	FormatCTE(name string, query *Query) string
	// FormatUnion formats a UNION clause
	FormatUnion(query *Query, all bool) string
	// FormatWindow formats a window function definition
	FormatWindow(name string, definition *WindowDefinition) string
	// FormatSubquery formats a subquery
	FormatSubquery(query *Query, alias string) string
	// FormatDistinctOn formats a DISTINCT ON clause
	FormatDistinctOn(fields []string) string
	// FormatReturning formats a RETURNING clause
	FormatReturning(fields []string) string
}
