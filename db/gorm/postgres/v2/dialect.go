package v2

import (
	"fmt"
	"strings"

	"git.qix.sx/gorgany/gorgany.git/db/gorm/postgres/v2/core"
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
		parts = append(parts, fmt.Sprintf("(%s)", join.Subquery))
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
func (d *PostgresDialect) FormatWhere(condition core.Condition) string {
	if condition == nil {
		return ""
	}
	sql, _ := condition.ToSQL()
	return fmt.Sprintf("WHERE %s", sql)
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
func (d *PostgresDialect) FormatHaving(condition core.Condition) string {
	if condition == nil {
		return ""
	}
	sql, _ := condition.ToSQL()
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
	return fmt.Sprintf("%s AS (%s)", name, query)
}

// FormatUnion formats a UNION clause
func (d *PostgresDialect) FormatUnion(query *core.Query, all bool) string {
	if all {
		return fmt.Sprintf("UNION ALL %s", query)
	}
	return fmt.Sprintf("UNION %s", query)
}

// FormatWindow formats a window function definition
func (d *PostgresDialect) FormatWindow(name string, definition *core.WindowDefinition) string {
	return fmt.Sprintf("%s AS (%s)", name, definition.String())
}

// FormatSubquery formats a subquery
func (d *PostgresDialect) FormatSubquery(query *core.Query, alias string) string {
	return fmt.Sprintf("(%s) AS %s", query, alias)
}

// FormatDistinctOn formats a DISTINCT ON clause
func (d *PostgresDialect) FormatDistinctOn(fields []string) string {
	return fmt.Sprintf("DISTINCT ON (%s)", strings.Join(fields, ", "))
}

// FormatReturning formats a RETURNING clause
func (d *PostgresDialect) FormatReturning(fields []string) string {
	return fmt.Sprintf("RETURNING %s", strings.Join(fields, ", "))
}
