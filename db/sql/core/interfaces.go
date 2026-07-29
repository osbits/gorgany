package core

import (
	"context"
)

// IDataSource defines the interface for database connections
type IDataSource interface {
	// NewSession creates a new database session
	NewSession() (ISession, error)

	GetDriver() (any, error)

	// Close closes the database connection
	Close() error
}

// IQueryExecutor handles the execution of queries
type IQueryExecutor interface {
	Exec(ctx context.Context, q IQueryBuilder) QueryResult

	Find(ctx context.Context, q IQueryBuilder, dest interface{}) QueryResult

	Count(ctx context.Context, q IQueryBuilder) (int64, error)

	ExecRaw(ctx context.Context, sql string, args ...interface{}) QueryResult
	FindRaw(ctx context.Context, dest interface{}, sql string, args ...interface{}) QueryResult
	CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error)
}

// InsertResult is what an INSERT reported back.
type InsertResult struct {
	QueryResult

	// LastInsertID is the auto-generated key the driver reported for this
	// statement, valid only when HasLastInsertID is true.
	LastInsertID int64
	// HasLastInsertID reports whether the driver supplied a generated key. It is
	// false on engines whose driver does not implement it — notably pgx, which is
	// why Postgres reads generated values back with RETURNING instead.
	HasLastInsertID bool
}

// LastInsertIDExecutor is implemented by executors that can report the
// auto-generated key of an INSERT.
//
// It reads the key from the driver's own sql.Result for that statement, so it is
// taken from the same connection that ran the INSERT. A separate
// `SELECT LAST_INSERT_ID()` would not be safe: the value is connection-scoped and
// the follow-up query can be served by a different connection from the pool.
//
// This is an optional interface — check for it with a type assertion.
type LastInsertIDExecutor interface {
	// ExecInsert runs q as a statement and reports any generated key.
	ExecInsert(ctx context.Context, q IQueryBuilder) InsertResult
}

// SupportsReturning reports whether d can render a RETURNING clause.
//
// The dialect is the only authority worth asking: a hand-maintained capability
// flag would eventually disagree with what FormatReturning actually does, and the
// disagreement would show up as invalid SQL rather than as a failed check.
func SupportsReturning(d SQLDialect) bool {
	if d == nil {
		return false
	}
	_, _, err := d.FormatReturning([]string{"id"})
	return err == nil
}

// QueryResult contains the result of a query operation
type QueryResult struct {
	// Error is the error that occurred during the query, if any
	Error error

	// RowsAffected is the number of rows affected by the query
	RowsAffected int64

	// Found indicates whether any rows were found by the query
	Found bool
}

// QueryResultAware is an interface for objects that need to be aware of query results
type QueryResultAware interface {
	// SetQueryResult sets the query result metadata
	SetQueryResult(result *QueryResult)

	// GetQueryResult gets the query result metadata
	GetQueryResult() *QueryResult
}

// ISession defines the interface for database sessions
type ISession interface {
	// Executor returns the query executor for this session
	Executor() IQueryExecutor

	// Query creates a new query builder
	Query() IQueryBuilder

	// Transaction executes the provided function within a transaction
	Transaction(ctx context.Context, fn func(IDBTransaction) error) error

	DataSource() IDataSource

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

// SQLDialect handles SQL formatting for a specific database.
//
// Every method returns an error so a dialect can refuse a construct its engine
// cannot express (see UnsupportedError) instead of emitting SQL the server will
// reject. Implementations must be stateless and safe for concurrent use: one
// dialect value is shared by every builder created from a datasource.
//
// See docs/DIALECTS.md for a walkthrough of implementing a new dialect.
type SQLDialect interface {
	// Name identifies the dialect, e.g. "postgres" or "mysql". It appears in
	// UnsupportedError messages.
	Name() string

	// QuoteIdentifier renders a simple or dotted identifier using the engine's
	// quoting rules — "tbl"."col" on Postgres, `tbl`.`col` on MySQL — escaping
	// any embedded quote character.
	QuoteIdentifier(identifier string) string

	// FormatSelect formats the SELECT clause
	FormatSelect(fields []string, distinct bool, distinctOn []string) (string, []any, error)
	// FormatFrom formats the FROM clause
	FormatFrom(table string, alias string) (string, []any, error)
	// FormatJoin formats a JOIN clause
	FormatJoin(join *JoinClause) (string, []any, error)
	// FormatWhere formats the WHERE clause
	FormatWhere(condition *WhereClause) (string, []any, error)
	// FormatOrderBy formats the ORDER BY clause
	FormatOrderBy(field string, direction string) (string, []any, error)
	// FormatGroupBy formats the GROUP BY clause, including ROLLUP, CUBE and
	// GROUPING SETS. It takes the whole clause because engines place those
	// modifiers differently: Postgres writes GROUP BY ROLLUP (a, b) while MySQL
	// writes GROUP BY a, b WITH ROLLUP.
	FormatGroupBy(groupBy *GroupByClause) (string, []any, error)
	// FormatHaving formats the HAVING clause
	FormatHaving(condition *HavingClause) (string, []any, error)
	// FormatLimit formats the LIMIT clause
	FormatLimit(limit int) (string, []any, error)
	// FormatOffset formats the OFFSET clause
	FormatOffset(offset int) (string, []any, error)
	// FormatCTE formats a Common Table Expression
	FormatCTE(name string, query *Query) (string, []any, error)
	// FormatUnion formats a UNION clause
	FormatUnion(query *Query, all bool) (string, []any, error)
	// FormatWindow formats a window function definition
	FormatWindow(name string, definition *WindowDefinition) (string, []any, error)
	// FormatSubquery formats a subquery
	FormatSubquery(query *Query, alias string) (string, []any, error)
	// FormatDistinctOn formats a DISTINCT ON clause
	FormatDistinctOn(fields []string) (string, []any, error)
	// FormatReturning formats a RETURNING clause
	FormatReturning(fields []string) (string, []any, error)

	// FormatQuery renders a complete query.
	FormatQuery(query *Query) (string, []any, error)
}

// IQueryBuilder defines the interface for building database queries
type IQueryBuilder interface {
	// Basic query building methods
	Select(fields ...string) IQueryBuilder
	From(table string) IQueryBuilder
	Where(condition Condition) IQueryBuilder
	Join(join *JoinClause) IQueryBuilder
	OrderBy(field string, direction string) IQueryBuilder
	OrderByRaw(expression string, direction string) IQueryBuilder
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

	HasFrom() bool
	HasWhere() bool
	HasJoin() bool
	HasOrderBy() bool
	HasGroupBy() bool
	HasLimit() bool
	HasOffset() bool

	Insert(table string) IQueryBuilder
	Columns(columns ...string) IQueryBuilder
	Values(values ...interface{}) IQueryBuilder
	FromSelect(query *Query) IQueryBuilder
	OnConflict(columns ...string) IQueryBuilder
	DoNothing() IQueryBuilder
	DoUpdate(setValues map[string]interface{}) IQueryBuilder

	Update(table string) IQueryBuilder
	Set(field string, value interface{}) IQueryBuilder
	SetMap(values map[string]interface{}) IQueryBuilder

	Delete(table string) IQueryBuilder

	Build() *Query

	// ToSQL renders the accumulated query through the builder's dialect.
	//
	// The error is non-nil when the dialect cannot express the query — for
	// example RETURNING or DISTINCT ON against MySQL. Callers must check it:
	// on error the returned SQL is empty rather than invalid.
	ToSQL() (string, []any, error)

	// Dialect returns the dialect this builder renders through.
	Dialect() SQLDialect
}
