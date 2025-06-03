package v2

import (
	"fmt"
	"strings"

	"git.qix.sx/gorgany/gorgany.git/db/sql/core"
)

// PostgresDialect implements SQLDialect for PostgreSQL
type PostgresDialect struct{}

// FormatSelect formats the SELECT clause
func (d *PostgresDialect) FormatSelect(fields []string, distinct bool, distinctOn []string) string {
	var parts []string
	parts = append(parts, "SELECT")

	if distinct {
		if len(distinctOn) > 0 {
			parts = append(parts, fmt.Sprintf("DISTINCT ON (%s)", strings.Join(distinctOn, ", ")))
		} else {
			parts = append(parts, "DISTINCT")
		}
	}

	parts = append(parts, strings.Join(fields, ", "))
	return strings.Join(parts, " ")
}

// FormatFrom formats the FROM clause
func (d *PostgresDialect) FormatFrom(table string, alias string) string {
	if alias != "" {
		return fmt.Sprintf("FROM %s AS %s", table, alias)
	}
	return fmt.Sprintf("FROM %s", table)
}

// FormatJoin formats a JOIN clause
func (d *PostgresDialect) FormatJoin(join *core.JoinClause) string {
	var parts []string
	parts = append(parts, join.Type, "JOIN")

	if join.IsSubquery {
		// Use the dialect to format the subquery
		parts = append(parts, fmt.Sprintf("(%s)", d.FormatQuery(join.Subquery)))
	} else {
		parts = append(parts, join.Table)
	}

	if join.Alias != "" {
		parts = append(parts, "AS", join.Alias)
	}

	if join.Condition != nil {
		sql, _ := join.Condition.ToSQL()
		parts = append(parts, "ON", sql)
	}

	return strings.Join(parts, " ")
}

// FormatWhere formats the WHERE clause
func (d *PostgresDialect) FormatWhere(where *core.WhereClause) string {
	if where == nil || len(where.Conditions) == 0 {
		return ""
	}

	var conditions []string
	for _, condition := range where.Conditions {
		sql, _ := condition.ToSQL()
		conditions = append(conditions, sql)
	}

	return fmt.Sprintf("WHERE %s", strings.Join(conditions, fmt.Sprintf(" %s ", where.Operator)))
}

// FormatOrderBy formats the ORDER BY clause
func (d *PostgresDialect) FormatOrderBy(field string, direction string) string {
	if direction == "" {
		return fmt.Sprintf("ORDER BY %s", field)
	}
	return fmt.Sprintf("ORDER BY %s %s", field, direction)
}

// FormatGroupBy formats the GROUP BY clause
func (d *PostgresDialect) FormatGroupBy(fields []string) string {
	return fmt.Sprintf("GROUP BY %s", strings.Join(fields, ", "))
}

// FormatHaving formats the HAVING clause
func (d *PostgresDialect) FormatHaving(having *core.HavingClause) string {
	if having == nil || having.Condition == nil {
		return ""
	}
	sql, _ := having.Condition.ToSQL()
	return fmt.Sprintf("HAVING %s", sql)
}

// FormatLimit formats the LIMIT clause
func (d *PostgresDialect) FormatLimit(limit int) string {
	return fmt.Sprintf("LIMIT %d", limit)
}

// FormatOffset formats the OFFSET clause
func (d *PostgresDialect) FormatOffset(offset int) string {
	return fmt.Sprintf("OFFSET %d", offset)
}

// FormatCTE formats a Common Table Expression
func (d *PostgresDialect) FormatCTE(name string, query *core.Query) string {
	return fmt.Sprintf("%s AS (%s)", name, d.FormatQuery(query))
}

// FormatUnion formats a UNION clause
func (d *PostgresDialect) FormatUnion(query *core.Query, all bool) string {
	if all {
		return fmt.Sprintf("UNION ALL %s", d.FormatQuery(query))
	}
	return fmt.Sprintf("UNION %s", d.FormatQuery(query))
}

// FormatWindow formats a window function definition
func (d *PostgresDialect) FormatWindow(name string, definition *core.WindowDefinition) string {
	return fmt.Sprintf("%s AS (%s)", name, definition.String())
}

// FormatSubquery formats a subquery
func (d *PostgresDialect) FormatSubquery(query *core.Query, alias string) string {
	return fmt.Sprintf("(%s) AS %s", d.FormatQuery(query), alias)
}

// FormatDistinctOn formats a DISTINCT ON clause
func (d *PostgresDialect) FormatDistinctOn(fields []string) string {
	return fmt.Sprintf("DISTINCT ON (%s)", strings.Join(fields, ", "))
}

// FormatReturning formats a RETURNING clause
func (d *PostgresDialect) FormatReturning(fields []string) string {
	return fmt.Sprintf("RETURNING %s", strings.Join(fields, ", "))
}

// FormatQuery formats a complete query
func (d *PostgresDialect) FormatQuery(query *core.Query) string {
	var parts []string

	// Add CTEs if any
	if len(query.CTEs) > 0 {
		cteParts := make([]string, len(query.CTEs))
		for i, cte := range query.CTEs {
			cteParts[i] = d.FormatCTE(cte.Name, cte.Query)
		}
		parts = append(parts, "WITH "+strings.Join(cteParts, ", "))
	}

	// Add SELECT clause
	if query.Select != nil {
		parts = append(parts, d.FormatSelect(
			query.Select.Fields,
			query.Select.Distinct,
			query.Select.DistinctOn,
		))
	}

	// Add FROM clause
	if query.From != nil {
		parts = append(parts, d.FormatFrom(query.From.Table, query.From.Alias))
	}

	// Add JOINs
	for _, join := range query.Joins {
		parts = append(parts, d.FormatJoin(join))
	}

	// Add WHERE clause
	if query.Where != nil {
		whereSQL := d.FormatWhere(query.Where)
		if whereSQL != "" {
			parts = append(parts, whereSQL)
		}
	}

	// Add GROUP BY clause
	if query.GroupBy != nil {
		parts = append(parts, d.FormatGroupBy(query.GroupBy.Fields))
	}

	// Add HAVING clause
	if query.Having != nil {
		havingSQL := d.FormatHaving(query.Having)
		if havingSQL != "" {
			parts = append(parts, havingSQL)
		}
	}

	// Add ORDER BY clause
	if query.OrderBy != nil {
		for _, field := range query.OrderBy.Fields {
			parts = append(parts, d.FormatOrderBy(field.Field, field.Direction))
		}
	}

	// Add LIMIT clause
	if query.Limit != nil {
		parts = append(parts, d.FormatLimit(*query.Limit))
	}

	// Add OFFSET clause
	if query.Offset != nil {
		parts = append(parts, d.FormatOffset(*query.Offset))
	}

	// Add UNION clauses
	for _, union := range query.Unions {
		parts = append(parts, d.FormatUnion(union.Query, union.All))
	}

	// Add RETURNING clause
	if len(query.Returning) > 0 {
		parts = append(parts, d.FormatReturning(query.Returning))
	}

	return strings.Join(parts, " ")
}
