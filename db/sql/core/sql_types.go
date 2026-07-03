package core

import (
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
	Insert    *InsertClause
	Update    *UpdateClause
	Delete    *DeleteClause
}

// SelectClause represents the SELECT part of a query
type SelectClause struct {
	Fields     []string
	Distinct   bool
	DistinctOn []string
}

// FromClause represents the FROM part of a query
type FromClause struct {
	Table      string
	Alias      string
	Subquery   *Query
	IsSubquery bool
}

// InsertClause represents an INSERT operation
type InsertClause struct {
	Table      string
	Columns    []string
	Values     [][]interface{}
	FromQuery  *Query // For INSERT ... SELECT ...
	OnConflict *OnConflictClause
}

// UpdateClause represents an UPDATE operation
type UpdateClause struct {
	Table  string
	Values map[string]interface{}
}

// DeleteClause represents a DELETE operation
type DeleteClause struct {
	Table string
}

// OnConflictClause represents an ON CONFLICT clause for PostgreSQL
type OnConflictClause struct {
	Columns   []string
	Action    string // "DO NOTHING" or "DO UPDATE"
	SetValues map[string]interface{}
}

// WhereClause represents the WHERE part of a query
type WhereClause struct {
	Operator   string
	Conditions []Condition
}

// ToSQL returns the SQL representation of the WHERE clause
func (w *WhereClause) ToSQL() (string, []interface{}) {
	if len(w.Conditions) == 0 {
		return "", nil
	}

	var args []interface{}
	conditions := make([]string, len(w.Conditions))

	for i, condition := range w.Conditions {
		sql, conditionArgs := condition.ToSQL()
		conditions[i] = sql
		args = append(args, conditionArgs...)
	}

	return strings.Join(conditions, fmt.Sprintf(" %s ", w.Operator)), args
}

// JoinClause represents a JOIN in a query
type JoinClause struct {
	Type       string
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

// OrderByField represents a field in an ORDER BY clause.
//
// Field carries a column name (bare "col" or dotted "table.col"). By default the
// dialect treats Field as untrusted data: a simple identifier is emitted as a
// quoted identifier, and anything else is bound as a placeholder rather than
// interpolated, so a request-supplied value cannot inject through the ORDER BY
// position. Set Raw when the caller is trusted and Field is a deliberate SQL
// expression (e.g. "first_name || ' ' || last_name") that must be emitted verbatim.
type OrderByField struct {
	Field     string
	Direction string
	// Raw marks Field as a trusted SQL expression to emit verbatim (not quoted,
	// not parameterized). Never set this from request-derived input.
	Raw bool
}

// GroupByClause represents the GROUP BY part of a query
type GroupByClause struct {
	Fields []string
	Sets   [][]string
	Rollup []string
	Cube   []string
}

// HavingClause represents the HAVING part of a query
type HavingClause struct {
	Condition Condition
}

// ToSQL returns the SQL representation of the HAVING clause
func (h *HavingClause) ToSQL() (string, []interface{}) {
	if h.Condition == nil {
		return "", nil
	}
	return h.Condition.ToSQL()
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

// WindowDefinition represents a window function definition
type WindowDefinition struct {
	PartitionBy []string
	OrderBy     []OrderByField
	Frame       *WindowFrame
}

// ToSQL returns the SQL representation of the window definition
func (w *WindowDefinition) ToSQL() (string, []interface{}) {
	var parts []string
	var args []interface{}

	if len(w.PartitionBy) > 0 {
		parts = append(parts, fmt.Sprintf("PARTITION BY %s", strings.Join(w.PartitionBy, ", ")))
	}

	if len(w.OrderBy) > 0 {
		orderParts := make([]string, len(w.OrderBy))
		for i, field := range w.OrderBy {
			orderParts[i] = fmt.Sprintf("%s %s", field.Field, field.Direction)
		}
		parts = append(parts, fmt.Sprintf("ORDER BY %s", strings.Join(orderParts, ", ")))
	}

	if w.Frame != nil {
		frameSQL, frameArgs := w.Frame.ToSQL()
		parts = append(parts, frameSQL)
		args = append(args, frameArgs...)
	}

	return strings.Join(parts, " "), args
}

// WindowFrame represents a window frame specification
type WindowFrame struct {
	Type      string // ROWS, RANGE, or GROUPS
	Start     *FrameBound
	End       *FrameBound
	Exclusion string // EXCLUDE CURRENT ROW, EXCLUDE GROUP, EXCLUDE TIES, or EXCLUDE NO OTHERS
}

// ToSQL returns the SQL representation of the window frame
func (f *WindowFrame) ToSQL() (string, []interface{}) {
	var parts []string
	var args []interface{}

	parts = append(parts, f.Type)

	if f.Start != nil {
		startSQL, startArgs := f.Start.ToSQL()
		parts = append(parts, startSQL)
		args = append(args, startArgs...)
	}

	if f.End != nil {
		endSQL, endArgs := f.End.ToSQL()
		parts = append(parts, "AND", endSQL)
		args = append(args, endArgs...)
	}

	if f.Exclusion != "" {
		parts = append(parts, f.Exclusion)
	}

	return strings.Join(parts, " "), args
}

// FrameBound represents a window frame boundary
type FrameBound struct {
	Type  string // UNBOUNDED PRECEDING, PRECEDING, CURRENT ROW, FOLLOWING, or UNBOUNDED FOLLOWING
	Value interface{}
}

// ToSQL returns the SQL representation of the frame boundary
func (b *FrameBound) ToSQL() (string, []interface{}) {
	if b.Value == nil {
		return b.Type, nil
	}
	return fmt.Sprintf("%s %v", b.Type, b.Value), []interface{}{b.Value}
}

// Condition represents a SQL condition
type Condition interface {
	ToSQL() (string, []interface{})
}

// AggregateFunction represents a SQL aggregate function
type AggregateFunction struct {
	Name        string
	Arguments   []string
	Filter      Condition
	WithinGroup *OrderByClause
	IsDistinct  bool
}

// String returns the SQL representation of the aggregate function
func (af *AggregateFunction) String() string {
	var parts []string
	parts = append(parts, af.Name)

	if af.IsDistinct {
		parts = append(parts, "DISTINCT")
	}

	if len(af.Arguments) > 0 {
		parts = append(parts, "("+strings.Join(af.Arguments, ", ")+")")
	} else {
		parts = append(parts, "()")
	}

	if af.Filter != nil {
		sql, _ := af.Filter.ToSQL()
		parts = append(parts, "FILTER (WHERE "+sql+")")
	}

	if af.WithinGroup != nil {
		parts = append(parts, "WITHIN GROUP (ORDER BY "+af.WithinGroup.String()+")")
	}

	return strings.Join(parts, " ")
}

// String returns the SQL representation of the window definition
func (wd *WindowDefinition) String() string {
	var parts []string

	if len(wd.PartitionBy) > 0 {
		parts = append(parts, fmt.Sprintf("PARTITION BY %s", strings.Join(wd.PartitionBy, ", ")))
	}

	if len(wd.OrderBy) > 0 {
		orderParts := make([]string, len(wd.OrderBy))
		for i, field := range wd.OrderBy {
			orderParts[i] = fmt.Sprintf("%s %s", field.Field, field.Direction)
		}
		parts = append(parts, fmt.Sprintf("ORDER BY %s", strings.Join(orderParts, ", ")))
	}

	if wd.Frame != nil {
		parts = append(parts, wd.Frame.String())
	}

	return strings.Join(parts, " ")
}

// String returns the SQL representation of the window frame
func (wf *WindowFrame) String() string {
	var parts []string
	parts = append(parts, wf.Type)

	if wf.Start != nil {
		parts = append(parts, "BETWEEN "+wf.Start.String())
		if wf.End != nil {
			parts = append(parts, "AND "+wf.End.String())
		}
	} else if wf.End != nil {
		parts = append(parts, wf.End.String())
	}

	if wf.Exclusion != "" {
		parts = append(parts, wf.Exclusion)
	}

	return strings.Join(parts, " ")
}

// String returns the SQL representation of the frame bound
func (fb *FrameBound) String() string {
	if fb.Value == nil {
		return fb.Type
	}
	return fmt.Sprintf("%s %v", fb.Type, fb.Value)
}

// String returns the SQL representation of the ORDER BY clause
func (ob *OrderByClause) String() string {
	if len(ob.Fields) == 0 {
		return ""
	}

	parts := make([]string, len(ob.Fields))
	for i, field := range ob.Fields {
		if field.Direction != "" {
			parts[i] = fmt.Sprintf("%s %s", field.Field, field.Direction)
		} else {
			parts[i] = field.Field
		}
	}

	return strings.Join(parts, ", ")
}
