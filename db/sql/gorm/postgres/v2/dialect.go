package v2

import (
	"fmt"
	"regexp"
	"strings"

	dbCore "github.com/osbits/gorgany/db/sql/core"
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

// simpleIdentifier matches a bare column or a dotted table.column reference,
// e.g. "created_at" or "members.created_at".
var simpleIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)*$`)

// quoteIdentifier renders a simple or dotted identifier as a quoted Postgres
// identifier ("tbl"."col"), doubling any embedded quote.
func quoteIdentifier(field string) string {
	parts := strings.Split(field, ".")
	for i, p := range parts {
		parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

// normalizeOrderDirection whitelists the sort direction to ASC or DESC, defaulting
// to ASC for anything unrecognized so a caller cannot inject via the direction slot.
func normalizeOrderDirection(direction string) string {
	if strings.EqualFold(strings.TrimSpace(direction), "desc") {
		return "DESC"
	}
	return "ASC"
}

// orderByField renders one ORDER BY entry, returning any bound args.
//
// The direction is always whitelisted to ASC/DESC. The field is handled per the
// hybrid hardening the framework uses everywhere else (see FormatHaving, which
// binds values as placeholders):
//   - Raw expressions from trusted callers are emitted verbatim.
//   - A simple/dotted identifier is emitted as a quoted identifier ("tbl"."col").
//   - Anything else is treated as untrusted data and BOUND as a placeholder
//     (ORDER BY ? ...) rather than interpolated, so a sub-select / boolean- or
//     time-based payload cannot break out of the ORDER BY position. (Binding a
//     non-identifier degrades to a constant sort key, i.e. a harmless no-op sort —
//     the point is that it can never be executed as SQL.)
func (d *PostgresDialect) orderByField(f dbCore.OrderByField) (string, []interface{}) {
	direction := normalizeOrderDirection(f.Direction)

	if f.Raw {
		return fmt.Sprintf("%s %s", f.Field, direction), nil
	}
	if simpleIdentifier.MatchString(f.Field) {
		return fmt.Sprintf("%s %s", quoteIdentifier(f.Field), direction), nil
	}
	return fmt.Sprintf("? %s", direction), []interface{}{f.Field}
}

// FormatOrderBy formats the ORDER BY clause for a single field. It is the
// interface entry point; it delegates to orderByField and applies the same
// hardening (untrusted, non-identifier fields are bound, not interpolated).
func (d *PostgresDialect) FormatOrderBy(field string, direction string) (string, []interface{}) {
	sql, args := d.orderByField(dbCore.OrderByField{Field: field, Direction: direction})
	return "ORDER BY " + sql, args
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
		// Handle SELECT queries
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

	// Add ORDER BY clause. Each field is hardened (quoted identifier, bound
	// non-identifier, or trusted raw) and the entries are comma-joined into a
	// single ORDER BY.
	if query.OrderBy != nil && len(query.OrderBy.Fields) > 0 {
		orderParts := make([]string, 0, len(query.OrderBy.Fields))
		for _, field := range query.OrderBy.Fields {
			fieldSQL, fieldArgs := d.orderByField(field)
			orderParts = append(orderParts, fieldSQL)
			allArgs = append(allArgs, fieldArgs...)
		}
		parts = append(parts, "ORDER BY "+strings.Join(orderParts, ", "))
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
