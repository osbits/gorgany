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
// It supports identifier placeholders used across the project to prevent
// identifiers (table/column) from being bound as string values:
//   - "?.id"  -> consumes 1 arg (table/alias), renders as <arg>.id
//   - "?.?"   -> consumes 2 args (table/alias, column), renders as <arg0>.<arg1>
//
// Remaining "?" placeholders are treated as value parameters and returned in args.
func (c *RawCondition) ToSQL() (string, []interface{}) {
	if c == nil {
		return "", nil
	}

	sql := c.SQL
	args := make([]interface{}, 0, len(c.Args))

	// Fast path: if there is no "?." pattern, keep legacy behavior
	if !strings.Contains(sql, "?.") {
		return sql, c.Args
	}

	// We'll build the final SQL by replacing identifier placeholders and
	// collecting remaining args for value placeholders.
	var sb strings.Builder
	runes := []rune(sql)
	for i := 0; i < len(runes); {
		// Detect "?" placeholder
		if runes[i] == '?' {
			// Check if it's an identifier placeholder followed by "."
			if i+1 < len(runes) && runes[i+1] == '.' {
				// Two forms are supported:
				// 1) "?.?" -> table/alias and column both dynamic (consume 2 args)
				// 2) "?.<word>" -> table/alias dynamic, column literal (consume 1 arg)
				if i+2 < len(runes) && runes[i+2] == '?' { // pattern "?.?"
					// Consume two args for identifiers
					if len(c.Args) < 2 {
						// Not enough args; fall back to legacy behavior
						return c.SQL, c.Args
					}
					tbl := fmt.Sprint(c.Args[0])
					col := fmt.Sprint(c.Args[1])
					c.Args = c.Args[2:]
					sb.WriteString(tbl)
					sb.WriteRune('.')
					sb.WriteString(col)
					// Skip "?.?"
					i += 3
					continue
				}

				// pattern "?.<word>"
				// Extract the literal column name following the dot
				j := i + 2 // start after "?."
				for j < len(runes) {
					r := runes[j]
					if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '"' {
						j++
						continue
					}
					break
				}
				if len(c.Args) < 1 {
					return c.SQL, c.Args
				}
				tbl := fmt.Sprint(c.Args[0])
				c.Args = c.Args[1:]
				sb.WriteString(tbl)
				sb.WriteRune('.')
				sb.WriteString(string(runes[i+2 : j]))
				i = j
				continue
			}

			// Value placeholder "?" -> keep as placeholder and collect next arg
			sb.WriteRune('?')
			if len(c.Args) > 0 {
				args = append(args, c.Args[0])
				c.Args = c.Args[1:]
			}
			i++
			continue
		}

		// Regular character
		sb.WriteRune(runes[i])
		i++
	}

	// Append any leftover args (shouldn't normally happen unless there were more
	// args than placeholders). Keep them to avoid silent loss.
	if len(c.Args) > 0 {
		args = append(args, c.Args...)
	}

	return sb.String(), args
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
