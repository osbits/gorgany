package v2

import (
	"fmt"
	"github.com/gorganyio/gorgany/db/sql/core"
	"strings"
)

// CompositeCondition represents a composite condition (AND/OR)
type CompositeCondition struct {
	Operator   string
	Conditions []core.Condition
}

// ToSQL returns the SQL representation of the composite condition
func (c *CompositeCondition) ToSQL() (string, []interface{}) {
	if len(c.Conditions) == 0 {
		return "", nil
	}

	var args []interface{}
	conditions := make([]string, len(c.Conditions))

	for i, condition := range c.Conditions {
		sql, conditionArgs := condition.ToSQL()
		conditions[i] = sql
		args = append(args, conditionArgs...)
	}

	return strings.Join(conditions, fmt.Sprintf(" %s ", c.Operator)), args
}

// SimpleCondition represents a simple condition (field operator value)
type SimpleCondition struct {
	Field    string
	Operator string
	Value    interface{}
}

// ToSQL returns the SQL representation of the simple condition
func (c *SimpleCondition) ToSQL() (string, []interface{}) {
	return fmt.Sprintf("%s %s ?", c.Field, c.Operator), []interface{}{c.Value}
}

// InCondition represents an IN condition
type InCondition struct {
	Field  string
	Values []interface{}
}

// ToSQL returns the SQL representation of the IN condition
func (c *InCondition) ToSQL() (string, []interface{}) {
	placeholders := make([]string, len(c.Values))
	for i := range c.Values {
		placeholders[i] = "?"
	}
	return fmt.Sprintf("%s IN (%s)", c.Field, strings.Join(placeholders, ", ")), c.Values
}

// BetweenCondition represents a BETWEEN condition
type BetweenCondition struct {
	Field string
	Start interface{}
	End   interface{}
}

// ToSQL returns the SQL representation of the BETWEEN condition
func (c *BetweenCondition) ToSQL() (string, []interface{}) {
	return fmt.Sprintf("%s BETWEEN ? AND ?", c.Field), []interface{}{c.Start, c.End}
}

// RawCondition represents a raw SQL condition
type RawCondition struct {
	SQL  string
	Args []interface{}
}

// ToSQL returns the SQL representation of the raw condition
func (c *RawCondition) ToSQL() (string, []interface{}) {
	return c.SQL, c.Args
}

// Helper functions for creating conditions

// Equal creates an equality condition
func Equal(field string, value interface{}) core.Condition {
	return &SimpleCondition{
		Field:    field,
		Operator: "=",
		Value:    value,
	}
}

// NotEqual creates a not-equal condition
func NotEqual(field string, value interface{}) core.Condition {
	return &SimpleCondition{
		Field:    field,
		Operator: "!=",
		Value:    value,
	}
}

// GreaterThan creates a greater-than condition
func GreaterThan(field string, value interface{}) core.Condition {
	return &SimpleCondition{
		Field:    field,
		Operator: ">",
		Value:    value,
	}
}

// LessThan creates a less-than condition
func LessThan(field string, value interface{}) core.Condition {
	return &SimpleCondition{
		Field:    field,
		Operator: "<",
		Value:    value,
	}
}

// In creates an IN condition
func In(field string, values ...interface{}) core.Condition {
	return &InCondition{
		Field:  field,
		Values: values,
	}
}

// Between creates a BETWEEN condition
func Between(field string, start, end interface{}) core.Condition {
	return &BetweenCondition{
		Field: field,
		Start: start,
		End:   end,
	}
}

// And creates an AND condition
func And(conditions ...core.Condition) core.Condition {
	return &CompositeCondition{
		Operator:   "AND",
		Conditions: conditions,
	}
}

// Or creates an OR condition
func Or(conditions ...core.Condition) core.Condition {
	return &CompositeCondition{
		Operator:   "OR",
		Conditions: conditions,
	}
}

// Raw creates a raw SQL condition
func Raw(sql string, args ...interface{}) core.Condition {
	return &RawCondition{
		SQL:  sql,
		Args: args,
	}
}
