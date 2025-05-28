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

// OrderByField represents a field in an ORDER BY clause
type OrderByField struct {
	Field     string
	Direction string
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
	OrderBy     *OrderByClause
	Frame       *WindowFrame
}

// WindowFrame represents a window frame specification
type WindowFrame struct {
	Mode      string // RANGE, ROWS, GROUPS
	Start     *FrameBound
	End       *FrameBound
	Exclusion string // EXCLUDE CURRENT ROW, EXCLUDE GROUP, EXCLUDE TIES, EXCLUDE NO OTHERS
}

// FrameBound represents a window frame boundary
type FrameBound struct {
	Type      string // UNBOUNDED PRECEDING, PRECEDING, CURRENT ROW, FOLLOWING, UNBOUNDED FOLLOWING
	Value     int
	IsCurrent bool
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
		parts = append(parts, "PARTITION BY "+strings.Join(wd.PartitionBy, ", "))
	}

	if wd.OrderBy != nil {
		parts = append(parts, "ORDER BY "+wd.OrderBy.String())
	}

	if wd.Frame != nil {
		parts = append(parts, wd.Frame.String())
	}

	return strings.Join(parts, " ")
}

// String returns the SQL representation of the window frame
func (wf *WindowFrame) String() string {
	var parts []string
	parts = append(parts, wf.Mode)

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
	if fb.IsCurrent {
		return "CURRENT ROW"
	}

	switch fb.Type {
	case "UNBOUNDED PRECEDING":
		return "UNBOUNDED PRECEDING"
	case "UNBOUNDED FOLLOWING":
		return "UNBOUNDED FOLLOWING"
	case "PRECEDING":
		return fmt.Sprintf("%d PRECEDING", fb.Value)
	case "FOLLOWING":
		return fmt.Sprintf("%d FOLLOWING", fb.Value)
	default:
		return "CURRENT ROW"
	}
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
