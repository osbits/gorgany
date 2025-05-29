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

// IQueryExecutor defines the interface for executing database queries
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

// IQueryBuilder defines the interface for building database queries
type IQueryBuilder interface {
	QueryBuilder
}

// IDBTransaction defines the interface for database transactions
type IDBTransaction interface {
	// Query creates a new query builder
	Query() IQueryBuilder

	// Find executes a query and stores the results in the provided destination
	Find(ctx context.Context, result interface{}, conditions ...Condition) error

	// FindOne executes a query and stores the first result in the provided destination
	FindOne(ctx context.Context, result interface{}, conditions ...Condition) error

	// Count returns the number of records matching the conditions
	Count(ctx context.Context, table string, conditions ...Condition) (int64, error)

	// Insert inserts a new record
	Insert(ctx context.Context, table string, data interface{}) error

	// Update updates records matching the conditions
	Update(ctx context.Context, table string, data interface{}, conditions ...Condition) error

	// Delete deletes records matching the conditions
	Delete(ctx context.Context, table string, conditions ...Condition) error

	// ExecuteRaw executes a raw SQL query without returning results
	ExecuteRaw(ctx context.Context, sql string, args ...interface{}) error

	// ExecuteRawWithResult executes a raw SQL query and stores the results in the provided destination
	ExecuteRawWithResult(ctx context.Context, result interface{}, sql string, args ...interface{}) error

	// CountRaw executes a raw SQL COUNT query
	CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error)
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
