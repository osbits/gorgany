package core

import (
	"fmt"
	"strings"
)

// BinaryCondition represents a condition with two operands and an operator
type BinaryCondition struct {
	Left     interface{}
	Operator string
	Right    interface{}
}

// ToSQL returns the SQL representation of the binary condition
func (c *BinaryCondition) ToSQL() (string, []interface{}) {
	var args []interface{}
	var leftSQL, rightSQL string

	// Handle left operand
	switch v := c.Left.(type) {
	case string:
		leftSQL = v
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		leftSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		leftSQL = "?"
		args = append(args, v)
	}

	// Handle right operand
	switch v := c.Right.(type) {
	case string:
		rightSQL = "?"
		args = append(args, v)
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		rightSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		rightSQL = "?"
		args = append(args, v)
	}

	return fmt.Sprintf("%s %s %s", leftSQL, c.Operator, rightSQL), args
}

// UnaryCondition represents a condition with one operand and an operator
type UnaryCondition struct {
	Operator string
	Operand  interface{}
}

// ToSQL returns the SQL representation of the unary condition
func (c *UnaryCondition) ToSQL() (string, []interface{}) {
	var args []interface{}
	var operandSQL string

	switch v := c.Operand.(type) {
	case string:
		operandSQL = v
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		operandSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		operandSQL = "?"
		args = append(args, v)
	}

	return fmt.Sprintf("%s %s", c.Operator, operandSQL), args
}

// InCondition represents an IN condition
type InCondition struct {
	Field      interface{}
	Values     []interface{}
	Not        bool
	IsSubquery bool
	Subquery   *Query
}

// ToSQL returns the SQL representation of the IN condition
func (c *InCondition) ToSQL() (string, []interface{}) {
	var args []interface{}
	var fieldSQL string

	// Handle field
	switch v := c.Field.(type) {
	case string:
		fieldSQL = v
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		fieldSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		fieldSQL = "?"
		args = append(args, v)
	}

	operator := "IN"
	if c.Not {
		operator = "NOT IN"
	}

	if c.IsSubquery {
		sql, subArgs := buildSubquerySQL(c.Subquery)
		return fmt.Sprintf("%s %s (%s)", fieldSQL, operator, sql), append(args, subArgs...)
	}

	placeholders := make([]string, len(c.Values))
	for i := range c.Values {
		placeholders[i] = "?"
		args = append(args, c.Values[i])
	}

	return fmt.Sprintf("%s %s (%s)", fieldSQL, operator, strings.Join(placeholders, ", ")), args
}

// BetweenCondition represents a BETWEEN condition
type BetweenCondition struct {
	Field interface{}
	Lower interface{}
	Upper interface{}
	Not   bool
}

// ToSQL returns the SQL representation of the BETWEEN condition
func (c *BetweenCondition) ToSQL() (string, []interface{}) {
	var args []interface{}
	var fieldSQL string

	// Handle field
	switch v := c.Field.(type) {
	case string:
		fieldSQL = v
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		fieldSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		fieldSQL = "?"
		args = append(args, v)
	}

	operator := "BETWEEN"
	if c.Not {
		operator = "NOT BETWEEN"
	}

	// Handle lower bound
	var lowerSQL string
	switch v := c.Lower.(type) {
	case string:
		lowerSQL = v
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		lowerSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		lowerSQL = "?"
		args = append(args, v)
	}

	// Handle upper bound
	var upperSQL string
	switch v := c.Upper.(type) {
	case string:
		upperSQL = v
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		upperSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		upperSQL = "?"
		args = append(args, v)
	}

	return fmt.Sprintf("%s %s %s AND %s", fieldSQL, operator, lowerSQL, upperSQL), args
}

// ExistsCondition represents an EXISTS condition
type ExistsCondition struct {
	Query *Query
	Not   bool
}

// ToSQL returns the SQL representation of the EXISTS condition
func (c *ExistsCondition) ToSQL() (string, []interface{}) {
	sql, args := buildSubquerySQL(c.Query)
	operator := "EXISTS"
	if c.Not {
		operator = "NOT EXISTS"
	}
	return fmt.Sprintf("%s (%s)", operator, sql), args
}

// LikeCondition represents a LIKE condition
type LikeCondition struct {
	Field   interface{}
	Pattern interface{}
	Not     bool
	Escape  string
}

// ToSQL returns the SQL representation of the LIKE condition
func (c *LikeCondition) ToSQL() (string, []interface{}) {
	var args []interface{}
	var fieldSQL string

	// Handle field
	switch v := c.Field.(type) {
	case string:
		fieldSQL = v
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		fieldSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		fieldSQL = "?"
		args = append(args, v)
	}

	operator := "LIKE"
	if c.Not {
		operator = "NOT LIKE"
	}

	// Handle pattern
	var patternSQL string
	switch v := c.Pattern.(type) {
	case string:
		patternSQL = "?"
		args = append(args, v)
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		patternSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		patternSQL = "?"
		args = append(args, v)
	}

	if c.Escape != "" {
		return fmt.Sprintf("%s %s %s ESCAPE '%s'", fieldSQL, operator, patternSQL, c.Escape), args
	}
	return fmt.Sprintf("%s %s %s", fieldSQL, operator, patternSQL), args
}

// IsNullCondition represents an IS NULL condition
type IsNullCondition struct {
	Field interface{}
	Not   bool
}

// ToSQL returns the SQL representation of the IS NULL condition
func (c *IsNullCondition) ToSQL() (string, []interface{}) {
	var args []interface{}
	var fieldSQL string

	// Handle field
	switch v := c.Field.(type) {
	case string:
		fieldSQL = v
	case *Query:
		sql, subArgs := buildSubquerySQL(v)
		fieldSQL = fmt.Sprintf("(%s)", sql)
		args = append(args, subArgs...)
	default:
		fieldSQL = "?"
		args = append(args, v)
	}

	operator := "IS NULL"
	if c.Not {
		operator = "IS NOT NULL"
	}

	return fmt.Sprintf("%s %s", fieldSQL, operator), args
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

// buildSubquerySQL builds SQL for a subquery and returns its SQL and arguments
func buildSubquerySQL(query *Query) (string, []interface{}) {
	var sqlBuilder strings.Builder
	var args []interface{}

	// Add SELECT clause
	if query.Select != nil {
		if query.Select.Distinct {
			if len(query.Select.DistinctOn) > 0 {
				sqlBuilder.WriteString(fmt.Sprintf("SELECT DISTINCT ON (%s) %s",
					strings.Join(query.Select.DistinctOn, ", "),
					strings.Join(query.Select.Fields, ", ")))
			} else {
				sqlBuilder.WriteString(fmt.Sprintf("SELECT DISTINCT %s",
					strings.Join(query.Select.Fields, ", ")))
			}
		} else {
			sqlBuilder.WriteString(fmt.Sprintf("SELECT %s",
				strings.Join(query.Select.Fields, ", ")))
		}
	}

	// Add FROM clause
	if query.From != nil {
		sqlBuilder.WriteString(" FROM ")
		if query.From.IsSubquery {
			subquerySQL, subqueryArgs := buildSubquerySQL(query.From.Subquery)
			sqlBuilder.WriteString(fmt.Sprintf("(%s) AS %s", subquerySQL, query.From.Alias))
			args = append(args, subqueryArgs...)
		} else {
			sqlBuilder.WriteString(query.From.Table)
		}
	}

	// Add JOINs
	for _, join := range query.Joins {
		sqlBuilder.WriteString(fmt.Sprintf(" %s JOIN ", join.Type))
		if join.IsSubquery {
			subquerySQL, subqueryArgs := buildSubquerySQL(join.Subquery)
			sqlBuilder.WriteString(fmt.Sprintf("(%s) AS %s", subquerySQL, join.Alias))
			args = append(args, subqueryArgs...)
		} else {
			sqlBuilder.WriteString(join.Table)
		}

		if join.Condition != nil {
			conditionSQL, conditionArgs := join.Condition.ToSQL()
			sqlBuilder.WriteString(fmt.Sprintf(" ON %s", conditionSQL))
			args = append(args, conditionArgs...)
		}
	}

	// Add WHERE clause
	if query.Where != nil {
		whereSQL, whereArgs := query.Where.ToSQL()
		if whereSQL != "" {
			sqlBuilder.WriteString(" WHERE ")
			sqlBuilder.WriteString(whereSQL)
			args = append(args, whereArgs...)
		}
	}

	// Add GROUP BY clause
	if query.GroupBy != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" GROUP BY %s",
			strings.Join(query.GroupBy.Fields, ", ")))
	}

	// Add HAVING clause
	if query.Having != nil {
		havingSQL, havingArgs := query.Having.ToSQL()
		if havingSQL != "" {
			sqlBuilder.WriteString(" HAVING ")
			sqlBuilder.WriteString(havingSQL)
			args = append(args, havingArgs...)
		}
	}

	// Add ORDER BY clause
	if query.OrderBy != nil {
		sqlBuilder.WriteString(" ORDER BY ")
		orderByParts := make([]string, len(query.OrderBy.Fields))
		for i, field := range query.OrderBy.Fields {
			if field.Direction != "" {
				orderByParts[i] = fmt.Sprintf("%s %s", field.Field, field.Direction)
			} else {
				orderByParts[i] = field.Field
			}
		}
		sqlBuilder.WriteString(strings.Join(orderByParts, ", "))
	}

	// Add LIMIT clause
	if query.Limit != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" LIMIT %d", *query.Limit))
	}

	// Add OFFSET clause
	if query.Offset != nil {
		sqlBuilder.WriteString(fmt.Sprintf(" OFFSET %d", *query.Offset))
	}

	return sqlBuilder.String(), args
}
