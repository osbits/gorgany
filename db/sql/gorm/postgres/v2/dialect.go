package v2

import (
	"fmt"
	"regexp"
	"strings"

	dbCore "github.com/osbits/gorgany/db/sql/core"
)

// DialectName is the registry key and error-message name for this dialect.
const DialectName = "postgres"

// PostgresDialect implements the SQLDialect interface for PostgreSQL
type PostgresDialect struct{}

var _ dbCore.SQLDialect = (*PostgresDialect)(nil)

// Name identifies the dialect.
func (d *PostgresDialect) Name() string { return DialectName }

// FormatSelect formats the SELECT clause
func (d *PostgresDialect) FormatSelect(fields []string, distinct bool, distinctOn []string) (string, []interface{}, error) {
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
	return strings.Join(parts, " "), nil, nil
}

// FormatFrom formats the FROM clause
func (d *PostgresDialect) FormatFrom(table string, alias string) (string, []interface{}, error) {
	if alias != "" {
		return fmt.Sprintf("FROM %s AS %s", table, alias), nil, nil
	}
	return fmt.Sprintf("FROM %s", table), nil, nil
}

// FormatJoin formats a JOIN clause
func (d *PostgresDialect) FormatJoin(join *dbCore.JoinClause) (string, []interface{}, error) {
	var parts []string
	parts = append(parts, join.Type, "JOIN")

	if join.IsSubquery {
		sql, args, err := d.FormatSubquery(join.Subquery, join.Alias)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, sql)
		if join.Condition != nil {
			conditionSQL, conditionArgs := join.Condition.ToSQL()
			parts = append(parts, "ON", conditionSQL)
			args = append(args, conditionArgs...)
		}
		return strings.Join(parts, " "), args, nil
	}

	parts = append(parts, join.Table)
	if join.Alias != "" {
		parts = append(parts, "AS", join.Alias)
	}
	if join.Condition != nil {
		conditionSQL, args := join.Condition.ToSQL()
		parts = append(parts, "ON", conditionSQL)
		return strings.Join(parts, " "), args, nil
	}
	return strings.Join(parts, " "), nil, nil
}

// FormatWhere formats the WHERE clause
func (d *PostgresDialect) FormatWhere(where *dbCore.WhereClause) (string, []interface{}, error) {
	if where == nil || len(where.Conditions) == 0 {
		return "", nil, nil
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

	return fmt.Sprintf("WHERE %s", strings.Join(conditions, fmt.Sprintf(" %s ", where.Operator))), allArgs, nil
}

// simpleIdentifier matches a bare column or a dotted table.column reference,
// e.g. "created_at" or "members.created_at".
var simpleIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)*$`)

// QuoteIdentifier renders a simple or dotted identifier as a quoted Postgres
// identifier ("tbl"."col"), doubling any embedded quote.
func (d *PostgresDialect) QuoteIdentifier(field string) string {
	parts := strings.Split(field, ".")
	for i, p := range parts {
		parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
	}
	return strings.Join(parts, ".")
}

// quoteIdentifier is retained as a package-level helper for callers inside this
// package; it delegates to PostgresDialect.QuoteIdentifier.
func quoteIdentifier(field string) string {
	return (&PostgresDialect{}).QuoteIdentifier(field)
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
		return fmt.Sprintf("%s %s", d.QuoteIdentifier(f.Field), direction), nil
	}
	return fmt.Sprintf("? %s", direction), []interface{}{f.Field}
}

// FormatOrderBy formats the ORDER BY clause for a single field. It is the
// interface entry point; it delegates to orderByField and applies the same
// hardening (untrusted, non-identifier fields are bound, not interpolated).
func (d *PostgresDialect) FormatOrderBy(field string, direction string) (string, []interface{}, error) {
	sql, args := d.orderByField(dbCore.OrderByField{Field: field, Direction: direction})
	return "ORDER BY " + sql, args, nil
}

// FormatGroupBy formats the GROUP BY clause, including the grouping-set
// modifiers Postgres spells as prefix functions: ROLLUP (a, b), CUBE (a, b) and
// GROUPING SETS ((a), (a, b)).
//
// Before v2 this took only the field list, so a query built with Rollup(),
// Cube() or GroupingSets() and no plain GroupBy() fields emitted a bare
// "GROUP BY " — invalid SQL that the server rejected.
func (d *PostgresDialect) FormatGroupBy(groupBy *dbCore.GroupByClause) (string, []interface{}, error) {
	if groupBy == nil {
		return "", nil, nil
	}

	terms := make([]string, 0, 4)
	if len(groupBy.Fields) > 0 {
		terms = append(terms, strings.Join(groupBy.Fields, ", "))
	}
	if len(groupBy.Rollup) > 0 {
		terms = append(terms, fmt.Sprintf("ROLLUP (%s)", strings.Join(groupBy.Rollup, ", ")))
	}
	if len(groupBy.Cube) > 0 {
		terms = append(terms, fmt.Sprintf("CUBE (%s)", strings.Join(groupBy.Cube, ", ")))
	}
	if len(groupBy.Sets) > 0 {
		sets := make([]string, len(groupBy.Sets))
		for i, set := range groupBy.Sets {
			sets[i] = "(" + strings.Join(set, ", ") + ")"
		}
		terms = append(terms, fmt.Sprintf("GROUPING SETS (%s)", strings.Join(sets, ", ")))
	}

	if len(terms) == 0 {
		return "", nil, nil
	}

	return "GROUP BY " + strings.Join(terms, ", "), nil, nil
}

// FormatHaving formats the HAVING clause
func (d *PostgresDialect) FormatHaving(condition *dbCore.HavingClause) (string, []interface{}, error) {
	if condition == nil {
		return "", nil, nil
	}
	sql, args := condition.Condition.ToSQL()
	return fmt.Sprintf("HAVING %s", sql), args, nil
}

// FormatLimit formats the LIMIT clause
func (d *PostgresDialect) FormatLimit(limit int) (string, []interface{}, error) {
	return fmt.Sprintf("LIMIT %d", limit), nil, nil
}

// FormatOffset formats the OFFSET clause
func (d *PostgresDialect) FormatOffset(offset int) (string, []interface{}, error) {
	return fmt.Sprintf("OFFSET %d", offset), nil, nil
}

// FormatCTE formats a Common Table Expression
func (d *PostgresDialect) FormatCTE(name string, query *dbCore.Query) (string, []interface{}, error) {
	sql, args, err := d.FormatQuery(query)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%s AS (%s)", name, sql), args, nil
}

// FormatUnion formats a UNION clause
func (d *PostgresDialect) FormatUnion(query *dbCore.Query, all bool) (string, []interface{}, error) {
	sql, args, err := d.FormatQuery(query)
	if err != nil {
		return "", nil, err
	}
	if all {
		return fmt.Sprintf("UNION ALL %s", sql), args, nil
	}
	return fmt.Sprintf("UNION %s", sql), args, nil
}

// FormatWindow formats a window function definition
func (d *PostgresDialect) FormatWindow(name string, definition *dbCore.WindowDefinition) (string, []interface{}, error) {
	s, _ := definition.ToSQL()
	return fmt.Sprintf("%s AS (%s)", name, s), nil, nil
}

// FormatSubquery formats a subquery
func (d *PostgresDialect) FormatSubquery(query *dbCore.Query, alias string) (string, []interface{}, error) {
	sql, args, err := d.FormatQuery(query)
	if err != nil {
		return "", nil, err
	}
	if alias != "" {
		return fmt.Sprintf("(%s) AS %s", sql, alias), args, nil
	}
	return fmt.Sprintf("(%s)", sql), args, nil
}

// FormatDistinctOn formats a DISTINCT ON clause
func (d *PostgresDialect) FormatDistinctOn(fields []string) (string, []interface{}, error) {
	return fmt.Sprintf("DISTINCT ON (%s)", strings.Join(fields, ", ")), nil, nil
}

// FormatReturning formats a RETURNING clause
func (d *PostgresDialect) FormatReturning(fields []string) (string, []interface{}, error) {
	return fmt.Sprintf("RETURNING %s", strings.Join(fields, ", ")), nil, nil
}

// FormatQuery converts a query to SQL and returns both the SQL string and arguments
func (d *PostgresDialect) FormatQuery(q *dbCore.Query) (string, []interface{}, error) {
	// Handle INSERT queries
	if q.Insert != nil {
		return d.formatInsert(q)
	}
	// Handle UPDATE queries
	if q.Update != nil {
		return d.formatUpdate(q)
	}
	// Handle DELETE queries
	if q.Delete != nil {
		return d.formatDelete(q)
	}
	// Handle SELECT queries
	return d.formatSelect(q)
}

// formatInsert generates SQL for INSERT queries
func (d *PostgresDialect) formatInsert(q *dbCore.Query) (string, []interface{}, error) {
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
		selectSQL, selectArgs, err := d.formatSelect(q.Insert.FromQuery)
		if err != nil {
			return "", nil, err
		}
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
			for _, field := range dbCore.SortedKeys(q.Insert.OnConflict.SetValues) {
				updates = append(updates, field+" = "+"?")
				args = append(args, q.Insert.OnConflict.SetValues[field])
			}

			sqlBuilder.WriteString(strings.Join(updates, ", "))
		}
	}

	// Add RETURNING clause if specified
	if len(q.Returning) > 0 {
		returningSQL, returningArgs, err := d.FormatReturning(q.Returning)
		if err != nil {
			return "", nil, err
		}
		sqlBuilder.WriteString(" ")
		sqlBuilder.WriteString(returningSQL)
		args = append(args, returningArgs...)
	}

	return sqlBuilder.String(), args, nil
}

// formatUpdate generates SQL for UPDATE queries
func (d *PostgresDialect) formatUpdate(q *dbCore.Query) (string, []interface{}, error) {
	var sqlBuilder strings.Builder
	var args []interface{}

	sqlBuilder.WriteString("UPDATE ")
	sqlBuilder.WriteString(q.Update.Table)
	sqlBuilder.WriteString(" SET ")

	// Add SET values
	updates := make([]string, 0, len(q.Update.Values))
	for _, field := range dbCore.SortedKeys(q.Update.Values) {
		updates = append(updates, field+" = "+"?")
		args = append(args, q.Update.Values[field])
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
		returningSQL, returningArgs, err := d.FormatReturning(q.Returning)
		if err != nil {
			return "", nil, err
		}
		sqlBuilder.WriteString(" ")
		sqlBuilder.WriteString(returningSQL)
		args = append(args, returningArgs...)
	}

	return sqlBuilder.String(), args, nil
}

// formatDelete generates SQL for DELETE queries
func (d *PostgresDialect) formatDelete(q *dbCore.Query) (string, []interface{}, error) {
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
		returningSQL, returningArgs, err := d.FormatReturning(q.Returning)
		if err != nil {
			return "", nil, err
		}
		sqlBuilder.WriteString(" ")
		sqlBuilder.WriteString(returningSQL)
		args = append(args, returningArgs...)
	}

	return sqlBuilder.String(), args, nil
}

// formatSelect formats a complete SELECT query
func (d *PostgresDialect) formatSelect(query *dbCore.Query) (string, []interface{}, error) {
	var parts []string
	var allArgs []interface{}

	// Add CTEs if any
	if len(query.CTEs) > 0 {
		cteParts := make([]string, len(query.CTEs))
		for i, cte := range query.CTEs {
			cteSQL, cteArgs, err := d.FormatCTE(cte.Name, cte.Query)
			if err != nil {
				return "", nil, err
			}
			cteParts[i] = cteSQL
			allArgs = append(allArgs, cteArgs...)
		}
		parts = append(parts, "WITH "+strings.Join(cteParts, ", "))
	}

	// Add SELECT clause
	if query.Select != nil {
		selectSQL, selectArgs, err := d.FormatSelect(
			query.Select.Fields,
			query.Select.Distinct,
			query.Select.DistinctOn,
		)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, selectSQL)
		allArgs = append(allArgs, selectArgs...)
	} else {
		parts = append(parts, "SELECT *")
	}

	// Add FROM clause
	if query.From != nil {
		fromSQL, fromArgs, err := d.FormatFrom(query.From.Table, query.From.Alias)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, fromSQL)
		allArgs = append(allArgs, fromArgs...)
	}

	// Add JOINs
	for _, join := range query.Joins {
		joinSQL, joinArgs, err := d.FormatJoin(join)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, joinSQL)
		allArgs = append(allArgs, joinArgs...)
	}

	// Add WHERE clause
	if query.Where != nil {
		whereSQL, whereArgs, err := d.FormatWhere(query.Where)
		if err != nil {
			return "", nil, err
		}
		if whereSQL != "" {
			parts = append(parts, whereSQL)
			allArgs = append(allArgs, whereArgs...)
		}
	}

	// Add GROUP BY clause
	if query.GroupBy != nil {
		groupBySQL, groupByArgs, err := d.FormatGroupBy(query.GroupBy)
		if err != nil {
			return "", nil, err
		}
		if groupBySQL != "" {
			parts = append(parts, groupBySQL)
			allArgs = append(allArgs, groupByArgs...)
		}
	}

	// Add HAVING clause
	if query.Having != nil {
		havingSQL, havingArgs, err := d.FormatHaving(query.Having)
		if err != nil {
			return "", nil, err
		}
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
		limitSQL, limitArgs, err := d.FormatLimit(*query.Limit)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, limitSQL)
		allArgs = append(allArgs, limitArgs...)
	}

	// Add OFFSET clause
	if query.Offset != nil {
		offsetSQL, offsetArgs, err := d.FormatOffset(*query.Offset)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, offsetSQL)
		allArgs = append(allArgs, offsetArgs...)
	}

	// Add UNION clauses
	for _, union := range query.Unions {
		unionSQL, unionArgs, err := d.FormatUnion(union.Query, union.All)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, unionSQL)
		allArgs = append(allArgs, unionArgs...)
	}

	// Add RETURNING clause
	if len(query.Returning) > 0 {
		returningSQL, returningArgs, err := d.FormatReturning(query.Returning)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, returningSQL)
		allArgs = append(allArgs, returningArgs...)
	}

	return strings.Join(parts, " "), allArgs, nil
}
