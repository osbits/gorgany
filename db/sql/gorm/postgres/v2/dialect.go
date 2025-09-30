package v2

import (
	"fmt"
	"strings"

	dbCore "github.com/gorganyio/gorgany/db/sql/core"
)

// PostgresDialect implements the SQLDialect interface for PostgreSQL
type PostgresDialect struct{}

// FormatSelect formats the SELECT clause
func (d *PostgresDialect) FormatSelect(fields []string, distinct bool, distinctOn []string) (string, []interface{}) {
	var parts []string
	if distinct {
		if len(distinctOn) > 0 {
			parts = append(parts, fmt.Sprintf("SELECT DISTINCT ON (%s)", strings.Join(distinctOn, ", ")))
		} else {
			parts = append(parts, "SELECT DISTINCT")
		}
	} else {
		parts = append(parts, "SELECT")
	}
	parts = append(parts, strings.Join(fields, ", "))
	return strings.Join(parts, " "), nil
}

// FormatFrom formats the FROM clause
func (d *PostgresDialect) FormatFrom(table string, alias string) (string, []interface{}) {
	if alias != "" {
		return fmt.Sprintf("FROM %s AS %s", table, alias), nil
	}
	return fmt.Sprintf("FROM %s", table), nil
}

// FormatJoin formats a JOIN clause
func (d *PostgresDialect) FormatJoin(join *dbCore.JoinClause) (string, []interface{}) {
	var parts []string
	parts = append(parts, join.Type, "JOIN")

	if join.IsSubquery {
		sql, args := d.FormatSubquery(join.Subquery, join.Alias)
		parts = append(parts, sql)
		if join.Condition != nil {
			conditionSQL, conditionArgs := join.Condition.ToSQL()
			parts = append(parts, "ON", conditionSQL)
			args = append(args, conditionArgs...)
		}
		return strings.Join(parts, " "), args
	}

	parts = append(parts, join.Table)
	if join.Alias != "" {
		parts = append(parts, "AS", join.Alias)
	}
	if join.Condition != nil {
		conditionSQL, args := join.Condition.ToSQL()
		parts = append(parts, "ON", conditionSQL)
		return strings.Join(parts, " "), args
	}
	return strings.Join(parts, " "), nil
}

// FormatWhere formats the WHERE clause
func (d *PostgresDialect) FormatWhere(where *dbCore.WhereClause) (string, []interface{}) {
	if where == nil || len(where.Conditions) == 0 {
		return "", nil
	}

	allArgs := make([]any, 0)
	var conditions []string
	for _, condition := range where.Conditions {
		sql, args := condition.ToSQL()
		if sql == "" {
			continue
		}

		conditions = append(conditions, sql)

		if len(args) > 0 {
			allArgs = append(allArgs, args...)
		}
	}

	return fmt.Sprintf("WHERE %s", strings.Join(conditions, fmt.Sprintf(" %s ", where.Operator))), allArgs
}

// FormatOrderBy formats the ORDER BY clause
func (d *PostgresDialect) FormatOrderBy(field string, direction string) (string, []interface{}) {
	return fmt.Sprintf("ORDER BY %s %s", field, direction), nil
}

// FormatGroupBy formats the GROUP BY clause
func (d *PostgresDialect) FormatGroupBy(fields []string) (string, []interface{}) {
	return fmt.Sprintf("GROUP BY %s", strings.Join(fields, ", ")), nil
}

// FormatHaving formats the HAVING clause
func (d *PostgresDialect) FormatHaving(condition *dbCore.HavingClause) (string, []interface{}) {
	if condition == nil {
		return "", nil
	}
	sql, args := condition.Condition.ToSQL()
	return fmt.Sprintf("HAVING %s", sql), args
}

// FormatLimit formats the LIMIT clause
func (d *PostgresDialect) FormatLimit(limit int) (string, []interface{}) {
	return fmt.Sprintf("LIMIT %d", limit), nil
}

// FormatOffset formats the OFFSET clause
func (d *PostgresDialect) FormatOffset(offset int) (string, []interface{}) {
	return fmt.Sprintf("OFFSET %d", offset), nil
}

// FormatCTE formats a Common Table Expression
func (d *PostgresDialect) FormatCTE(name string, query *dbCore.Query) (string, []interface{}) {
	sql, args := d.FormatQuery(query)
	return fmt.Sprintf("%s AS (%s)", name, sql), args
}

// FormatUnion formats a UNION clause
func (d *PostgresDialect) FormatUnion(query *dbCore.Query, all bool) (string, []interface{}) {
	sql, args := d.FormatQuery(query)
	if all {
		return fmt.Sprintf("UNION ALL %s", sql), args
	}
	return fmt.Sprintf("UNION %s", sql), args
}

// FormatWindow formats a window function definition
func (d *PostgresDialect) FormatWindow(name string, definition *dbCore.WindowDefinition) (string, []interface{}) {
	s, _ := definition.ToSQL()
	return fmt.Sprintf("%s AS (%s)", name, s), nil
}

// FormatSubquery formats a subquery
func (d *PostgresDialect) FormatSubquery(query *dbCore.Query, alias string) (string, []interface{}) {
	sql, args := d.FormatQuery(query)
	if alias != "" {
		return fmt.Sprintf("(%s) AS %s", sql, alias), args
	}
	return fmt.Sprintf("(%s)", sql), args
}

// FormatDistinctOn formats a DISTINCT ON clause
func (d *PostgresDialect) FormatDistinctOn(fields []string) (string, []interface{}) {
	return fmt.Sprintf("DISTINCT ON (%s)", strings.Join(fields, ", ")), nil
}

// FormatReturning formats a RETURNING clause
func (d *PostgresDialect) FormatReturning(fields []string) (string, []interface{}) {
	return fmt.Sprintf("RETURNING %s", strings.Join(fields, ", ")), nil
}

// FormatQuery converts a query to SQL and returns both the SQL string and arguments
func (d *PostgresDialect) FormatQuery(q *dbCore.Query) (string, []interface{}) {
	var sql string
	var args []interface{}

	// Handle INSERT queries
	if q.Insert != nil {
		sql, args = d.formatInsert(q)
	} else if q.Update != nil {
		// Handle UPDATE queries
		sql, args = d.formatUpdate(q)
	} else if q.Delete != nil {
		// Handle DELETE queries
		sql, args = d.formatDelete(q)
	} else {
		// Handle SELECT queries (existing code)
		sql, args = d.formatSelect(q)
	}

	return sql, args
}

// formatInsert generates SQL for INSERT queries
func (d *PostgresDialect) formatInsert(q *dbCore.Query) (string, []interface{}) {
	var sqlBuilder strings.Builder
	var args []interface{}

	sqlBuilder.WriteString("INSERT INTO ")
	sqlBuilder.WriteString(q.Insert.Table)

	// Add columns
	if len(q.Insert.Columns) > 0 {
		sqlBuilder.WriteString(" (")
		sqlBuilder.WriteString(strings.Join(q.Insert.Columns, ", "))
		sqlBuilder.WriteString(")")
	}

	// Add values or select
	if q.Insert.FromQuery != nil {
		// INSERT ... SELECT ...
		selectSQL, selectArgs := d.formatSelect(q.Insert.FromQuery)
		sqlBuilder.WriteString(" ")
		sqlBuilder.WriteString(selectSQL)
		args = append(args, selectArgs...)
	} else if len(q.Insert.Values) > 0 {
		// INSERT ... VALUES ...
		sqlBuilder.WriteString(" VALUES ")

		valueSets := make([]string, len(q.Insert.Values))
		for i, valueSet := range q.Insert.Values {
			placeholders := make([]string, len(valueSet))
			for j, value := range valueSet {
				placeholders[j] = "?"
				args = append(args, value)
			}
			valueSets[i] = "(" + strings.Join(placeholders, ", ") + ")"
		}

		sqlBuilder.WriteString(strings.Join(valueSets, ", "))
	}

	// Add ON CONFLICT clause if specified
	if q.Insert.OnConflict != nil {
		sqlBuilder.WriteString(" ON CONFLICT")

		if len(q.Insert.OnConflict.Columns) > 0 {
			sqlBuilder.WriteString(" (")
			sqlBuilder.WriteString(strings.Join(q.Insert.OnConflict.Columns, ", "))
			sqlBuilder.WriteString(")")
		}

		if q.Insert.OnConflict.Action == "DO NOTHING" {
			sqlBuilder.WriteString(" DO NOTHING")
		} else if q.Insert.OnConflict.Action == "DO UPDATE" {
			sqlBuilder.WriteString(" DO UPDATE SET ")

			updates := make([]string, 0, len(q.Insert.OnConflict.SetValues))
			for field, value := range q.Insert.OnConflict.SetValues {
				updates = append(updates, field+" = "+"?")
				args = append(args, value)
			}

			sqlBuilder.WriteString(strings.Join(updates, ", "))
		}
	}

	// Add RETURNING clause if specified
	if len(q.Returning) > 0 {
		sqlBuilder.WriteString(" RETURNING ")
		sqlBuilder.WriteString(strings.Join(q.Returning, ", "))
	}

	return sqlBuilder.String(), args
}

// formatUpdate generates SQL for UPDATE queries
func (d *PostgresDialect) formatUpdate(q *dbCore.Query) (string, []interface{}) {
	var sqlBuilder strings.Builder
	var args []interface{}

	sqlBuilder.WriteString("UPDATE ")
	sqlBuilder.WriteString(q.Update.Table)
	sqlBuilder.WriteString(" SET ")

	// Add SET values
	updates := make([]string, 0, len(q.Update.Values))
	for field, value := range q.Update.Values {
		updates = append(updates, field+" = "+"?")
		args = append(args, value)
	}

	sqlBuilder.WriteString(strings.Join(updates, ", "))

	// Add WHERE clause if specified
	if q.Where != nil {
		whereSQL, whereArgs := q.Where.ToSQL()
		if whereSQL != "" {
			sqlBuilder.WriteString(" WHERE ")
			sqlBuilder.WriteString(whereSQL)
			args = append(args, whereArgs...)
		}
	}

	// Add RETURNING clause if specified
	if len(q.Returning) > 0 {
		sqlBuilder.WriteString(" RETURNING ")
		sqlBuilder.WriteString(strings.Join(q.Returning, ", "))
	}

	return sqlBuilder.String(), args
}

// formatDelete generates SQL for DELETE queries
func (d *PostgresDialect) formatDelete(q *dbCore.Query) (string, []interface{}) {
	var sqlBuilder strings.Builder
	var args []interface{}

	sqlBuilder.WriteString("DELETE FROM ")
	sqlBuilder.WriteString(q.Delete.Table)

	// Add WHERE clause if specified
	if q.Where != nil {
		whereSQL, whereArgs := q.Where.ToSQL()
		if whereSQL != "" {
			sqlBuilder.WriteString(" WHERE ")
			sqlBuilder.WriteString(whereSQL)
			args = append(args, whereArgs...)
		}
	}

	// Add RETURNING clause if specified
	if len(q.Returning) > 0 {
		sqlBuilder.WriteString(" RETURNING ")
		sqlBuilder.WriteString(strings.Join(q.Returning, ", "))
	}

	return sqlBuilder.String(), args
}

// FormatQuery formats a complete query
func (d *PostgresDialect) formatSelect(query *dbCore.Query) (string, []interface{}) {
	var parts []string
	var allArgs []interface{}

	// Add CTEs if any
	if len(query.CTEs) > 0 {
		cteParts := make([]string, len(query.CTEs))
		for i, cte := range query.CTEs {
			cteSQL, cteArgs := d.FormatCTE(cte.Name, cte.Query)
			cteParts[i] = cteSQL
			allArgs = append(allArgs, cteArgs...)
		}
		parts = append(parts, "WITH "+strings.Join(cteParts, ", "))
	}

	// Add SELECT clause
	if query.Select != nil {
		selectSQL, selectArgs := d.FormatSelect(
			query.Select.Fields,
			query.Select.Distinct,
			query.Select.DistinctOn,
		)
		parts = append(parts, selectSQL)
		allArgs = append(allArgs, selectArgs...)
	} else {
		parts = append(parts, "SELECT *")
	}

	// Add FROM clause
	if query.From != nil {
		fromSQL, fromArgs := d.FormatFrom(query.From.Table, query.From.Alias)
		parts = append(parts, fromSQL)
		allArgs = append(allArgs, fromArgs...)
	}

	// Add JOINs
	for _, join := range query.Joins {
		joinSQL, joinArgs := d.FormatJoin(join)
		parts = append(parts, joinSQL)
		allArgs = append(allArgs, joinArgs...)
	}

	// Add WHERE clause
	if query.Where != nil {
		whereSQL, whereArgs := d.FormatWhere(query.Where)
		if whereSQL != "" {
			parts = append(parts, whereSQL)
			allArgs = append(allArgs, whereArgs...)
		}
	}

	// Add GROUP BY clause
	if query.GroupBy != nil {
		groupBySQL, groupByArgs := d.FormatGroupBy(query.GroupBy.Fields)
		parts = append(parts, groupBySQL)
		allArgs = append(allArgs, groupByArgs...)
	}

	// Add HAVING clause
	if query.Having != nil {
		havingSQL, havingArgs := d.FormatHaving(query.Having)
		if havingSQL != "" {
			parts = append(parts, havingSQL)
			allArgs = append(allArgs, havingArgs...)
		}
	}

	// Add ORDER BY clause
	if query.OrderBy != nil {
		for _, field := range query.OrderBy.Fields {
			orderBySQL, orderByArgs := d.FormatOrderBy(field.Field, field.Direction)
			parts = append(parts, orderBySQL)
			allArgs = append(allArgs, orderByArgs...)
		}
	}

	// Add LIMIT clause
	if query.Limit != nil {
		limitSQL, limitArgs := d.FormatLimit(*query.Limit)
		parts = append(parts, limitSQL)
		allArgs = append(allArgs, limitArgs...)
	}

	// Add OFFSET clause
	if query.Offset != nil {
		offsetSQL, offsetArgs := d.FormatOffset(*query.Offset)
		parts = append(parts, offsetSQL)
		allArgs = append(allArgs, offsetArgs...)
	}

	// Add UNION clauses
	for _, union := range query.Unions {
		unionSQL, unionArgs := d.FormatUnion(union.Query, union.All)
		parts = append(parts, unionSQL)
		allArgs = append(allArgs, unionArgs...)
	}

	// Add RETURNING clause
	if len(query.Returning) > 0 {
		returningSQL, returningArgs := d.FormatReturning(query.Returning)
		parts = append(parts, returningSQL)
		allArgs = append(allArgs, returningArgs...)
	}

	return strings.Join(parts, " "), allArgs
}
