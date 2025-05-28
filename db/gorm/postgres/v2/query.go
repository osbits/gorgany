package postgres

import (
	"context"
	"fmt"
	"strings"
)

// Query represents a complete SQL query with all its components
type Query struct {
	Select    *SelectClause
	From      *FromClause
	Where     *WhereClause
	Joins     []*JoinClause
	OrderBy   *OrderByClause
	GroupBy   *GroupByClause
	Having    *HavingClause
	Limit     *int
	Offset    *int
	CTEs      []*CTEClause
	Unions    []*UnionClause
	Windows   []*WindowClause
	Returning []string // For INSERT/UPDATE/DELETE operations
}

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
}

// QueryExecutor handles the execution of queries
type QueryExecutor interface {
	// Query execution methods
	Execute(ctx context.Context, query *Query) error
	ExecuteWithResult(ctx context.Context, query *Query, result interface{}) error
	Count(ctx context.Context, query *Query) (int64, error)

	// Raw query execution methods
	ExecuteRaw(ctx context.Context, sql string, args ...interface{}) error
	ExecuteRawWithResult(ctx context.Context, result interface{}, sql string, args ...interface{}) error
	CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error)
}

// TransactionManager handles database transactions
type TransactionManager interface {
	Begin(ctx context.Context) (Transaction, error)
	WithTransaction(ctx context.Context, fn func(Transaction) error) error
}

// Transaction represents a database transaction
type Transaction interface {
	QueryBuilder
	QueryExecutor
	Commit() error
	Rollback() error
}

// Condition represents a SQL condition (WHERE, HAVING, etc.)
type Condition interface {
	ToSQL() (string, []interface{})
}

// SelectClause represents the SELECT part of a query
type SelectClause struct {
	Fields     []string
	Distinct   bool
	DistinctOn []string
	Windows    []*WindowClause
	Aggregates []*AggregateFunction
}

// AggregateFunction represents an aggregate function with FILTER and WITHIN GROUP
type AggregateFunction struct {
	Name        string
	Arguments   []string
	Filter      Condition
	WithinGroup *OrderByClause
	IsDistinct  bool
}

// FromClause represents the FROM part of a query
type FromClause struct {
	Table      string
	Alias      string
	Subquery   *Query
	IsSubquery bool
	IsLateral  bool
}

// WhereClause represents the WHERE part of a query
type WhereClause struct {
	Conditions []Condition
	Operator   string // AND/OR
}

// JoinClause represents a JOIN operation
type JoinClause struct {
	Type       string // INNER, LEFT, RIGHT, FULL, CROSS, NATURAL, LATERAL
	Table      string
	Alias      string
	Condition  Condition
	Subquery   *Query
	IsSubquery bool
	IsLateral  bool
}

// OrderByClause represents the ORDER BY part of a query
type OrderByClause struct {
	Fields []OrderByField
}

// OrderByField represents a single field in ORDER BY
type OrderByField struct {
	Field     string
	Direction string // ASC/DESC
	Nulls     string // FIRST/LAST
}

// GroupByClause represents the GROUP BY part of a query
type GroupByClause struct {
	Fields []string
	Sets   [][]string // For GROUPING SETS
	Rollup []string   // For ROLLUP
	Cube   []string   // For CUBE
}

// HavingClause represents the HAVING part of a query
type HavingClause struct {
	Condition Condition
}

// CTEClause represents a Common Table Expression
type CTEClause struct {
	Name  string
	Query *Query
}

// UnionClause represents a UNION operation
type UnionClause struct {
	Query *Query
	All   bool
}

// WindowClause represents a window function definition
type WindowClause struct {
	Name       string
	Definition *WindowDefinition
}

// WindowDefinition represents the definition of a window function
type WindowDefinition struct {
	PartitionBy []string
	OrderBy     []OrderByField
	Frame       *WindowFrame
}

// String returns the SQL representation of the window definition
func (w *WindowDefinition) String() string {
	var parts []string

	if len(w.PartitionBy) > 0 {
		parts = append(parts, "PARTITION BY "+strings.Join(w.PartitionBy, ", "))
	}

	if len(w.OrderBy) > 0 {
		orderByParts := make([]string, len(w.OrderBy))
		for i, field := range w.OrderBy {
			orderByParts[i] = field.Field
			if field.Direction != "" {
				orderByParts[i] += " " + field.Direction
			}
			if field.Nulls != "" {
				orderByParts[i] += " NULLS " + field.Nulls
			}
		}
		parts = append(parts, "ORDER BY "+strings.Join(orderByParts, ", "))
	}

	if w.Frame != nil {
		frameParts := []string{w.Frame.Mode}
		if w.Frame.Start != nil {
			frameParts = append(frameParts, w.Frame.Start.String())
		}
		if w.Frame.End != nil {
			frameParts = append(frameParts, "AND", w.Frame.End.String())
		}
		if w.Frame.Exclusion != "" {
			frameParts = append(frameParts, w.Frame.Exclusion)
		}
		parts = append(parts, strings.Join(frameParts, " "))
	}

	return strings.Join(parts, " ")
}

// WindowFrame represents the frame specification for a window function
type WindowFrame struct {
	Mode      string // RANGE/ROWS/GROUPS
	Start     *FrameBound
	End       *FrameBound
	Exclusion string // EXCLUDE CURRENT ROW/EXCLUDE GROUP/EXCLUDE TIES/EXCLUDE NO OTHERS
}

// FrameBound represents a boundary in a window frame
type FrameBound struct {
	Type      string // UNBOUNDED PRECEDING/PRECEDING/CURRENT ROW/FOLLOWING/UNBOUNDED FOLLOWING
	Value     int    // For PRECEDING/FOLLOWING
	IsCurrent bool   // For CURRENT ROW
}

// String returns the SQL representation of the frame bound
func (f *FrameBound) String() string {
	if f.IsCurrent {
		return "CURRENT ROW"
	}
	if f.Type == "UNBOUNDED PRECEDING" || f.Type == "UNBOUNDED FOLLOWING" {
		return f.Type
	}
	return fmt.Sprintf("%s %d", f.Type, f.Value)
}

// String returns the SQL representation of an aggregate function
func (a *AggregateFunction) String() string {
	var parts []string

	// Function name
	funcName := a.Name
	if a.IsDistinct {
		funcName += " DISTINCT"
	}

	// Arguments
	args := strings.Join(a.Arguments, ", ")
	parts = append(parts, fmt.Sprintf("%s(%s)", funcName, args))

	// WITHIN GROUP
	if a.WithinGroup != nil {
		orderByParts := make([]string, len(a.WithinGroup.Fields))
		for i, field := range a.WithinGroup.Fields {
			orderByParts[i] = field.Field
			if field.Direction != "" {
				orderByParts[i] += " " + field.Direction
			}
			if field.Nulls != "" {
				orderByParts[i] += " NULLS " + field.Nulls
			}
		}
		parts = append(parts, fmt.Sprintf("WITHIN GROUP (ORDER BY %s)", strings.Join(orderByParts, ", ")))
	}

	// FILTER
	if a.Filter != nil {
		sql, _ := a.Filter.ToSQL()
		parts = append(parts, fmt.Sprintf("FILTER (WHERE %s)", sql))
	}

	return strings.Join(parts, " ")
}

// JsonOperation represents a JSON/JSONB operation
type JsonOperation struct {
	Type    string // EXTRACT, CONTAINS, CONTAINS_ANY, CONTAINS_ALL
	Path    string
	Value   string
	IsJsonb bool
}

// ArrayOperation represents an array operation
type ArrayOperation struct {
	Type       string // CONTAINS, CONTAINED_BY, OVERLAP, LENGTH, APPEND, PREPEND, REMOVE, REPLACE
	Array      string
	Element    string
	NewElement string
	Dimension  int
}

// FullTextSearchOperation represents a full-text search operation
type FullTextSearchOperation struct {
	Type     string // SEARCH, RANK, HIGHLIGHT, TO_TSQUERY, TO_TSVECTOR
	Vector   string
	Query    string
	Config   string
	StartSel string
	StopSel  string
	Fields   []string
}

// String returns the SQL representation of a JSON operation
func (j *JsonOperation) String() string {
	switch j.Type {
	case "EXTRACT":
		if j.IsJsonb {
			return fmt.Sprintf("(%s #>> '%s')", j.Path, j.Value)
		}
		return fmt.Sprintf("(%s ->> '%s')", j.Path, j.Value)
	case "CONTAINS":
		if j.IsJsonb {
			return fmt.Sprintf("(%s @> %s)", j.Path, j.Value)
		}
		return fmt.Sprintf("(%s ? %s)", j.Path, j.Value)
	case "CONTAINS_ANY":
		if j.IsJsonb {
			return fmt.Sprintf("(%s ?| %s)", j.Path, j.Value)
		}
		return fmt.Sprintf("(%s ?| %s)", j.Path, j.Value)
	case "CONTAINS_ALL":
		if j.IsJsonb {
			return fmt.Sprintf("(%s ?& %s)", j.Path, j.Value)
		}
		return fmt.Sprintf("(%s ?& %s)", j.Path, j.Value)
	default:
		return ""
	}
}

// String returns the SQL representation of an array operation
func (a *ArrayOperation) String() string {
	switch a.Type {
	case "CONTAINS":
		return fmt.Sprintf("(%s @> %s)", a.Array, a.Element)
	case "CONTAINED_BY":
		return fmt.Sprintf("(%s <@ %s)", a.Array, a.Element)
	case "OVERLAP":
		return fmt.Sprintf("(%s && %s)", a.Array, a.Element)
	case "LENGTH":
		return fmt.Sprintf("array_length(%s, %d)", a.Array, a.Dimension)
	case "APPEND":
		return fmt.Sprintf("array_append(%s, %s)", a.Array, a.Element)
	case "PREPEND":
		return fmt.Sprintf("array_prepend(%s, %s)", a.Array, a.Element)
	case "REMOVE":
		return fmt.Sprintf("array_remove(%s, %s)", a.Array, a.Element)
	case "REPLACE":
		return fmt.Sprintf("array_replace(%s, %s, %s)", a.Array, a.Element, a.NewElement)
	default:
		return ""
	}
}

// String returns the SQL representation of a full-text search operation
func (f *FullTextSearchOperation) String() string {
	switch f.Type {
	case "SEARCH":
		return fmt.Sprintf("to_tsvector('%s', %s) @@ to_tsquery('%s', %s)", f.Config, f.Vector, f.Config, f.Query)
	case "RANK":
		return fmt.Sprintf("ts_rank(to_tsvector('%s', %s), to_tsquery('%s', %s))", f.Config, f.Vector, f.Config, f.Query)
	case "HIGHLIGHT":
		return fmt.Sprintf("ts_headline('%s', %s, to_tsquery('%s', %s), 'StartSel=%s,StopSel=%s')",
			f.Config, f.Vector, f.Config, f.Query, f.StartSel, f.StopSel)
	case "TO_TSQUERY":
		return fmt.Sprintf("to_tsquery('%s', %s)", f.Config, f.Query)
	case "TO_TSVECTOR":
		return fmt.Sprintf("to_tsvector('%s', %s)", f.Config, strings.Join(f.Fields, " || ' ' || "))
	default:
		return ""
	}
}
