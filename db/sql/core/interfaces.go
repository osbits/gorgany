package core

import (
	"context"
)

// DatabaseFeatures represents database-specific capabilities
type DatabaseFeatures interface {
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
type IQueryExecutor interface {
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

// IQueryBuilder defines the interface for building database queries
type IQueryBuilder interface {
	// Basic query building methods
	Select(fields ...string) IQueryBuilder
	From(table string) IQueryBuilder
	Where(condition Condition) IQueryBuilder
	Join(join *JoinClause) IQueryBuilder
	OrderBy(field string, direction string) IQueryBuilder
	GroupBy(fields ...string) IQueryBuilder
	Having(condition Condition) IQueryBuilder
	Limit(limit int) IQueryBuilder
	Offset(offset int) IQueryBuilder

	// Advanced query building methods
	WithCTE(name string, query *Query) IQueryBuilder
	Union(query *Query) IQueryBuilder
	UnionAll(query *Query) IQueryBuilder
	Window(name string, definition *WindowDefinition) IQueryBuilder
	Subquery(query *Query, alias string) IQueryBuilder
	LateralJoin(query *Query, alias string, condition Condition) IQueryBuilder
	DistinctOn(fields ...string) IQueryBuilder
	Returning(fields ...string) IQueryBuilder

	// Join helper methods
	InnerJoin(table string, condition Condition) IQueryBuilder
	LeftJoin(table string, condition Condition) IQueryBuilder
	RightJoin(table string, condition Condition) IQueryBuilder
	FullJoin(table string, condition Condition) IQueryBuilder
	CrossJoin(table string) IQueryBuilder
	NaturalJoin(table string) IQueryBuilder

	// Group by helper methods
	GroupingSets(sets ...[]string) IQueryBuilder
	Rollup(fields ...string) IQueryBuilder
	Cube(fields ...string) IQueryBuilder

	// Window function helper methods
	Over(name string) string

	// Query finalization
	Build() *Query
	ToSQL() string
}

// IDBTransaction defines the interface for database transactions
type IDBTransaction interface {
	// Query creates a new query builder
	IQueryExecutor
	IQueryBuilder
	Query() IQueryBuilder
}

// ISession defines the interface for database sessions
type ISession interface {
	// Executor returns the query executor for this session
	Executor() IQueryExecutor

	// Query creates a new query builder
	Query() IQueryBuilder

	// Transaction executes the provided function within a transaction
	Transaction(ctx context.Context, fn func(IDBTransaction) error) error

	// Close closes the session
	Close() error
}

// IDataSource defines the interface for database connections
type IDataSource interface {
	// NewSession creates a new database session
	NewSession() (ISession, error)

	// Close closes the database connection
	Close() error
}
