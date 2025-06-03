package core

import (
	"context"
)

// IDataSource defines the interface for database connections
type IDataSource interface {
	// NewSession creates a new database session
	NewSession() (ISession, error)

	// Close closes the database connection
	Close() error
}

// IQueryExecutor handles the execution of queries
type IQueryExecutor interface {
	Exec(ctx context.Context, q *Query) error

	QueryOne(ctx context.Context, q *Query, dest interface{}) error

	QueryList(ctx context.Context, q *Query, dest interface{}) error

	Count(ctx context.Context, q *Query) (int64, error)

	ExecRaw(ctx context.Context, sql string, args ...interface{}) error
	QueryRaw(ctx context.Context, dest interface{}, sql string, args ...interface{}) error
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

// IDBTransaction defines the interface for database transactions
type IDBTransaction interface {
	// Query creates a new query builder
	IQueryExecutor
	IQueryBuilder
	Query() IQueryBuilder
}

// SQLDialect handles SQL formatting for a specific database
type SQLDialect interface {
	// FormatSelect formats the SELECT clause
	FormatSelect(fields []string, distinct bool, distinctOn []string) (string, []any)
	// FormatFrom formats the FROM clause
	FormatFrom(table string, alias string) (string, []any)
	// FormatJoin formats a JOIN clause
	FormatJoin(join *JoinClause) (string, []any)
	// FormatWhere formats the WHERE clause
	FormatWhere(condition *WhereClause) (string, []any)
	// FormatOrderBy formats the ORDER BY clause
	FormatOrderBy(field string, direction string) (string, []any)
	// FormatGroupBy formats the GROUP BY clause
	FormatGroupBy(fields []string) (string, []any)
	// FormatHaving formats the HAVING clause
	FormatHaving(condition *HavingClause) (string, []any)
	// FormatLimit formats the LIMIT clause
	FormatLimit(limit int) (string, []any)
	// FormatOffset formats the OFFSET clause
	FormatOffset(offset int) (string, []any)
	// FormatCTE formats a Common Table Expression
	FormatCTE(name string, query *Query) (string, []any)
	// FormatUnion formats a UNION clause
	FormatUnion(query *Query, all bool) (string, []any)
	// FormatWindow formats a window function definition
	FormatWindow(name string, definition *WindowDefinition) (string, []any)
	// FormatSubquery formats a subquery
	FormatSubquery(query *Query, alias string) (string, []any)
	// FormatDistinctOn formats a DISTINCT ON clause
	FormatDistinctOn(fields []string) (string, []any)
	// FormatReturning formats a RETURNING clause
	FormatReturning(fields []string) (string, []any)

	FormatQuery(query *Query) (string, []interface{})
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

	// Condition helper methods
	Eq(field interface{}, value interface{}) IQueryBuilder
	Neq(field interface{}, value interface{}) IQueryBuilder
	Gt(field interface{}, value interface{}) IQueryBuilder
	Gte(field interface{}, value interface{}) IQueryBuilder
	Lt(field interface{}, value interface{}) IQueryBuilder
	Lte(field interface{}, value interface{}) IQueryBuilder
	In(field interface{}, values ...interface{}) IQueryBuilder
	NotIn(field interface{}, values ...interface{}) IQueryBuilder
	InSubquery(field interface{}, subquery *Query) IQueryBuilder
	NotInSubquery(field interface{}, subquery *Query) IQueryBuilder
	Between(field interface{}, lower interface{}, upper interface{}) IQueryBuilder
	NotBetween(field interface{}, lower interface{}, upper interface{}) IQueryBuilder
	Exists(subquery *Query) IQueryBuilder
	NotExists(subquery *Query) IQueryBuilder
	Like(field interface{}, pattern interface{}) IQueryBuilder
	NotLike(field interface{}, pattern interface{}) IQueryBuilder
	LikeEscape(field interface{}, pattern interface{}, escape string) IQueryBuilder
	NotLikeEscape(field interface{}, pattern interface{}, escape string) IQueryBuilder
	IsNull(field interface{}) IQueryBuilder
	IsNotNull(field interface{}) IQueryBuilder

	// Query finalization
	Build() *Query
	ToSQL() (string, []any)
}
