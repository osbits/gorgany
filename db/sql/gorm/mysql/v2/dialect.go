// Package v2 implements the MySQL datasource and SQL dialect.
//
// Target: MySQL 8.0 or newer, default charset utf8mb4 and collation
// utf8mb4_unicode_ci. The dialect assumes sql_mode includes ONLY_FULL_GROUP_BY
// (MySQL 8's default) and never emits SQL that relies on loose grouping.
//
// The governing rule is that every construct MySQL cannot express returns an
// explicit dbCore.UnsupportedError rather than emitting SQL the server will
// reject. See docs/DIALECTS.md for the full table.
package v2

import (
	"fmt"
	"regexp"
	"strings"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// DialectName is the registry key and error-message name for this dialect.
const DialectName = "mysql"

// MySQLDialect implements dbCore.SQLDialect for MySQL 8.0+.
type MySQLDialect struct {
	// AllowUnfaithfulUpsert permits translating Postgres' ON CONFLICT (cols) DO
	// UPDATE into MySQL's ON DUPLICATE KEY UPDATE.
	//
	// It is off by default because the translation is *valid but wrong* SQL, which is
	// the failure mode the dialect's governing rule exists to prevent — and a worse
	// one than invalid SQL, because the server accepts it. ON DUPLICATE KEY UPDATE
	// fires on a duplicate in ANY unique index or the primary key, not the conflict
	// target the caller named, so the target column list has nowhere to go and is
	// dropped. On a table with more than one unique index MySQL's own manual advises
	// against the clause entirely, because it behaves like
	// `UPDATE ... WHERE a=1 OR b=2 LIMIT 1` and which row is updated is not the
	// caller's to control.
	//
	// Set it only for a table you know has exactly one unique constraint, having read
	// the caveat in docs/DIALECTS.md. ON CONFLICT DO NOTHING is unaffected: its
	// self-assignment translation is faithful.
	AllowUnfaithfulUpsert bool
}

var _ dbCore.SQLDialect = (*MySQLDialect)(nil)

// Name identifies the dialect.
func (d *MySQLDialect) Name() string { return DialectName }

func unsupported(construct, hint string) error {
	return dbCore.Unsupported(DialectName, construct, hint)
}

// simpleIdentifier matches a bare column or a dotted table.column reference,
// e.g. "created_at" or "members.created_at".
var simpleIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*(\.[A-Za-z_][A-Za-z0-9_$]*)*$`)

// QuoteIdentifier renders a simple or dotted identifier using MySQL backticks
// (`tbl`.`col`), doubling any embedded backtick.
//
// MySQL only accepts double-quoted identifiers under ANSI_QUOTES, which is not
// set by default and would break string literals elsewhere, so backticks are the
// only portable choice.
func (d *MySQLDialect) QuoteIdentifier(field string) string {
	parts := strings.Split(field, ".")
	for i, p := range parts {
		parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
	}
	return strings.Join(parts, ".")
}

// FormatSelect formats the SELECT clause.
//
// DISTINCT ON is Postgres-only; MySQL has no equivalent, so it is refused rather
// than degraded to a plain DISTINCT, which would return a different row set.
func (d *MySQLDialect) FormatSelect(fields []string, distinct bool, distinctOn []string) (string, []any, error) {
	if len(distinctOn) > 0 {
		return "", nil, unsupported("DISTINCT ON",
			"rewrite as a ROW_NUMBER() window function in a subquery filtered to rn = 1")
	}

	var parts []string
	if distinct {
		parts = append(parts, "SELECT DISTINCT")
	} else {
		parts = append(parts, "SELECT")
	}
	parts = append(parts, strings.Join(fields, ", "))
	return strings.Join(parts, " "), nil, nil
}

// FormatFrom formats the FROM clause.
func (d *MySQLDialect) FormatFrom(table string, alias string) (string, []any, error) {
	if alias != "" {
		return fmt.Sprintf("FROM %s AS %s", table, alias), nil, nil
	}
	return fmt.Sprintf("FROM %s", table), nil, nil
}

// formatFromClause renders a FROM clause, including the subquery form that
// FormatFrom's (table, alias) signature cannot express.
func (d *MySQLDialect) formatFromClause(from *dbCore.FromClause) (string, []any, error) {
	if from == nil {
		return "", nil, nil
	}

	if from.IsSubquery {
		if from.Subquery == nil {
			return "", nil, fmt.Errorf("mysql: FROM is marked as a subquery but carries no query")
		}
		// MySQL requires a derived table to be aliased.
		if from.Alias == "" {
			return "", nil, fmt.Errorf("mysql: a derived table in FROM requires an alias")
		}
		sql, args, err := d.FormatSubquery(from.Subquery, from.Alias)
		if err != nil {
			return "", nil, err
		}
		return "FROM " + sql, args, nil
	}

	return d.FormatFrom(from.Table, from.Alias)
}

// FormatJoin formats a JOIN clause.
//
// FULL OUTER JOIN is refused: MySQL has no such join type and the workaround is a
// UNION of a LEFT and a RIGHT join, which changes the shape of the query enough
// that the dialect must not do it silently.
//
// LATERAL is emitted as-is; it requires MySQL 8.0.14 or newer.
func (d *MySQLDialect) FormatJoin(join *dbCore.JoinClause) (string, []any, error) {
	if join == nil {
		return "", nil, nil
	}

	switch strings.ToUpper(strings.TrimSpace(join.Type)) {
	case "FULL", "FULL OUTER":
		return "", nil, unsupported("FULL OUTER JOIN",
			"emulate with a LEFT JOIN unioned with a RIGHT JOIN")
	}

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

// ilikeRE matches the ILIKE operator as a standalone word inside a raw condition.
var ilikeRE = regexp.MustCompile(`(?i)\bILIKE\b`)

// FormatWhere formats the WHERE clause.
//
// MySQL has no ILIKE. It is rewritten to LIKE, which is case-insensitive under
// the utf8mb4_unicode_ci collation this dialect targets. On a case-sensitive or
// binary collation the comparison becomes case-sensitive — that is a property of
// the column's collation, not of the rewrite, and is called out in
// docs/DIALECTS.md.
func (d *MySQLDialect) FormatWhere(where *dbCore.WhereClause) (string, []any, error) {
	if where == nil || len(where.Conditions) == 0 {
		return "", nil, nil
	}

	allArgs := make([]any, 0)
	var conditions []string
	for _, condition := range where.Conditions {
		// Refuse before rendering. A condition whose identifier slot holds something that is
		// not an identifier renders as a bound value — a comparison against the *text* of a
		// predicate rather than the predicate — which is safe but silently wrong. On this path
		// the caller can be told, so it is.
		if err := dbCore.ValidateCondition(condition); err != nil {
			return "", nil, fmt.Errorf("cannot render WHERE: %w", err)
		}

		sql, args := condition.ToSQL()
		if sql == "" {
			continue
		}

		conditions = append(conditions, ilikeRE.ReplaceAllString(sql, "LIKE"))

		if len(args) > 0 {
			allArgs = append(allArgs, args...)
		}
	}

	if len(conditions) == 0 {
		return "", nil, nil
	}

	return fmt.Sprintf("WHERE %s", strings.Join(conditions, fmt.Sprintf(" %s ", where.Operator))), allArgs, nil
}

// normalizeOrderDirection whitelists the sort direction to ASC or DESC, defaulting
// to ASC for anything unrecognized so a caller cannot inject via the direction slot.
func normalizeOrderDirection(direction string) string {
	if strings.EqualFold(strings.TrimSpace(direction), "desc") {
		return "DESC"
	}
	return "ASC"
}

// orderByField renders one ORDER BY entry, applying the same hardening the
// Postgres dialect uses: the direction is whitelisted, a trusted raw expression
// is emitted verbatim, a simple/dotted identifier is backtick-quoted, and
// anything else is BOUND as a placeholder rather than interpolated, so
// request-derived input cannot break out of the ORDER BY position.
func (d *MySQLDialect) orderByField(f dbCore.OrderByField) (string, []any) {
	direction := normalizeOrderDirection(f.Direction)

	if f.Raw {
		return fmt.Sprintf("%s %s", f.Field, direction), nil
	}
	if simpleIdentifier.MatchString(f.Field) {
		return fmt.Sprintf("%s %s", d.QuoteIdentifier(f.Field), direction), nil
	}
	return fmt.Sprintf("? %s", direction), []any{f.Field}
}

// FormatOrderBy formats the ORDER BY clause for a single field.
func (d *MySQLDialect) FormatOrderBy(field string, direction string) (string, []any, error) {
	sql, args := d.orderByField(dbCore.OrderByField{Field: field, Direction: direction})
	return "ORDER BY " + sql, args, nil
}

// FormatGroupBy formats the GROUP BY clause.
//
// ROLLUP exists on MySQL but in a different syntactic position: a trailing
// WITH ROLLUP modifier rather than Postgres' prefix ROLLUP (a, b). CUBE and
// GROUPING SETS have no MySQL equivalent at all and are refused.
//
// Because WITH ROLLUP modifies the whole grouping list, it cannot be combined
// with a separate plain field list the way Postgres allows; the rollup columns
// become the grouping list.
func (d *MySQLDialect) FormatGroupBy(groupBy *dbCore.GroupByClause) (string, []any, error) {
	if groupBy == nil {
		return "", nil, nil
	}

	if len(groupBy.Cube) > 0 {
		return "", nil, unsupported("CUBE",
			"enumerate the grouping combinations as a UNION ALL of GROUP BY queries")
	}
	if len(groupBy.Sets) > 0 {
		return "", nil, unsupported("GROUPING SETS",
			"enumerate the grouping combinations as a UNION ALL of GROUP BY queries")
	}

	if len(groupBy.Rollup) > 0 {
		fields := append(append([]string{}, groupBy.Fields...), groupBy.Rollup...)
		return "GROUP BY " + strings.Join(fields, ", ") + " WITH ROLLUP", nil, nil
	}

	if len(groupBy.Fields) == 0 {
		return "", nil, nil
	}

	return "GROUP BY " + strings.Join(groupBy.Fields, ", "), nil, nil
}

// FormatHaving formats the HAVING clause.
func (d *MySQLDialect) FormatHaving(condition *dbCore.HavingClause) (string, []any, error) {
	if condition == nil || condition.Condition == nil {
		return "", nil, nil
	}
	if err := dbCore.ValidateCondition(condition.Condition); err != nil {
		return "", nil, fmt.Errorf("cannot render HAVING: %w", err)
	}
	sql, args := condition.Condition.ToSQL()
	return fmt.Sprintf("HAVING %s", ilikeRE.ReplaceAllString(sql, "LIKE")), args, nil
}

// FormatLimit formats the LIMIT clause.
func (d *MySQLDialect) FormatLimit(limit int) (string, []any, error) {
	return fmt.Sprintf("LIMIT %d", limit), nil, nil
}

// FormatOffset formats the OFFSET clause.
//
// MySQL accepts `LIMIT n OFFSET m` but rejects a bare OFFSET with no LIMIT.
// formatSelect therefore synthesises the maximum LIMIT MySQL documents for that
// idiom when an offset is set without one.
func (d *MySQLDialect) FormatOffset(offset int) (string, []any, error) {
	return fmt.Sprintf("OFFSET %d", offset), nil, nil
}

// FormatCTE formats a Common Table Expression. CTEs require MySQL 8.0+.
func (d *MySQLDialect) FormatCTE(name string, query *dbCore.Query) (string, []any, error) {
	sql, args, err := d.FormatQuery(query)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%s AS (%s)", name, sql), args, nil
}

// FormatUnion formats a UNION clause.
func (d *MySQLDialect) FormatUnion(query *dbCore.Query, all bool) (string, []any, error) {
	sql, args, err := d.FormatQuery(query)
	if err != nil {
		return "", nil, err
	}
	if all {
		return fmt.Sprintf("UNION ALL %s", sql), args, nil
	}
	return fmt.Sprintf("UNION %s", sql), args, nil
}

// FormatWindow formats a named window definition. Window functions require
// MySQL 8.0+.
func (d *MySQLDialect) FormatWindow(name string, definition *dbCore.WindowDefinition) (string, []any, error) {
	if definition == nil {
		return "", nil, nil
	}
	s, _ := definition.ToSQL()
	return fmt.Sprintf("%s AS (%s)", name, s), nil, nil
}

// FormatSubquery formats a subquery.
func (d *MySQLDialect) FormatSubquery(query *dbCore.Query, alias string) (string, []any, error) {
	sql, args, err := d.FormatQuery(query)
	if err != nil {
		return "", nil, err
	}
	if alias != "" {
		return fmt.Sprintf("(%s) AS %s", sql, alias), args, nil
	}
	return fmt.Sprintf("(%s)", sql), args, nil
}

// FormatDistinctOn always fails: MySQL has no DISTINCT ON.
func (d *MySQLDialect) FormatDistinctOn(fields []string) (string, []any, error) {
	return "", nil, unsupported("DISTINCT ON",
		"rewrite as a ROW_NUMBER() window function in a subquery filtered to rn = 1")
}

// FormatReturning always fails: MySQL has no RETURNING.
//
// After an INSERT, read the generated key with LAST_INSERT_ID(); for UPDATE and
// DELETE, SELECT the affected rows before or after the statement inside a
// transaction.
func (d *MySQLDialect) FormatReturning(fields []string) (string, []any, error) {
	return "", nil, unsupported("RETURNING",
		"read the generated key with LAST_INSERT_ID(), or SELECT the rows inside the same transaction")
}

// FormatQuery renders a complete query.
func (d *MySQLDialect) FormatQuery(q *dbCore.Query) (string, []any, error) {
	if q == nil {
		return "", nil, fmt.Errorf("mysql: cannot format a nil query")
	}

	if q.Insert != nil {
		return d.formatInsert(q)
	}
	if q.Update != nil {
		return d.formatUpdate(q)
	}
	if q.Delete != nil {
		return d.formatDelete(q)
	}
	return d.formatSelect(q)
}

// formatInsert generates SQL for INSERT queries.
//
// Postgres' ON CONFLICT (cols) DO UPDATE SET / DO NOTHING becomes MySQL's
// ON DUPLICATE KEY UPDATE. The two are not equivalent: MySQL keys off *any*
// unique index rather than the named conflict target, so the column list is
// dropped. DO NOTHING has no direct MySQL spelling and is expressed as the
// idiomatic self-assignment of the first insert column, which makes the
// statement a documented no-op on conflict.
func (d *MySQLDialect) formatInsert(q *dbCore.Query) (string, []any, error) {
	if len(q.Returning) > 0 {
		if _, _, err := d.FormatReturning(q.Returning); err != nil {
			return "", nil, err
		}
	}

	var sqlBuilder strings.Builder
	var args []any

	sqlBuilder.WriteString("INSERT INTO ")
	sqlBuilder.WriteString(q.Insert.Table)

	if len(q.Insert.Columns) > 0 {
		sqlBuilder.WriteString(" (")
		sqlBuilder.WriteString(strings.Join(q.Insert.Columns, ", "))
		sqlBuilder.WriteString(")")
	}

	if q.Insert.FromQuery != nil {
		selectSQL, selectArgs, err := d.formatSelect(q.Insert.FromQuery)
		if err != nil {
			return "", nil, err
		}
		sqlBuilder.WriteString(" ")
		sqlBuilder.WriteString(selectSQL)
		args = append(args, selectArgs...)
	} else if len(q.Insert.Values) > 0 {
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

	if q.Insert.OnConflict != nil {
		switch q.Insert.OnConflict.Action {
		case "DO NOTHING":
			column, err := noopUpdateColumn(q.Insert)
			if err != nil {
				return "", nil, err
			}
			quoted := d.QuoteIdentifier(column)
			sqlBuilder.WriteString(" ON DUPLICATE KEY UPDATE ")
			sqlBuilder.WriteString(quoted + " = " + quoted)

		case "DO UPDATE":
			if !d.AllowUnfaithfulUpsert {
				// The remedy names the config key, not just the struct field. It used to
				// name only MySQLDialect.AllowUnfaithfulUpsert — which an app using the
				// ORM had no way to set, because Dialect() built the dialect itself (H3).
				return "", nil, unsupported("ON CONFLICT ... DO UPDATE",
					"MySQL's ON DUPLICATE KEY UPDATE fires on any unique index rather than the "+
						"conflict target you named, so the translation is not faithful; set "+
						"databases.<name>.allow_unfaithful_upsert: true (or "+
						"MySQLDialect.AllowUnfaithfulUpsert, when you build the dialect yourself) "+
						"if the table has exactly one unique constraint, or do the read-then-write "+
						"explicitly in a transaction")
			}
			if len(q.Insert.OnConflict.SetValues) == 0 {
				return "", nil, fmt.Errorf("mysql: ON DUPLICATE KEY UPDATE requires at least one assignment")
			}
			sqlBuilder.WriteString(" ON DUPLICATE KEY UPDATE ")
			updates := make([]string, 0, len(q.Insert.OnConflict.SetValues))
			for _, field := range dbCore.SortedKeys(q.Insert.OnConflict.SetValues) {
				updates = append(updates, field+" = ?")
				args = append(args, q.Insert.OnConflict.SetValues[field])
			}
			sqlBuilder.WriteString(strings.Join(updates, ", "))

		case "":
			// OnConflict() was called without DoNothing()/DoUpdate(). Emitting a
			// bare ON DUPLICATE KEY UPDATE is a syntax error, so say so.
			return "", nil, fmt.Errorf("mysql: OnConflict requires DoNothing() or DoUpdate(...)")

		default:
			return "", nil, fmt.Errorf("mysql: unknown ON CONFLICT action %q", q.Insert.OnConflict.Action)
		}
	}

	return sqlBuilder.String(), args, nil
}

// noopUpdateColumn picks the column used to express DO NOTHING as a
// self-assignment.
func noopUpdateColumn(insert *dbCore.InsertClause) (string, error) {
	if len(insert.Columns) > 0 {
		return insert.Columns[0], nil
	}
	if len(insert.OnConflict.Columns) > 0 {
		return insert.OnConflict.Columns[0], nil
	}
	return "", fmt.Errorf("mysql: DO NOTHING needs at least one insert column or conflict target to build the no-op assignment")
}

// formatUpdate generates SQL for UPDATE queries.
func (d *MySQLDialect) formatUpdate(q *dbCore.Query) (string, []any, error) {
	if len(q.Returning) > 0 {
		if _, _, err := d.FormatReturning(q.Returning); err != nil {
			return "", nil, err
		}
	}
	if len(q.Update.Values) == 0 {
		return "", nil, fmt.Errorf("mysql: UPDATE requires at least one SET assignment")
	}

	var sqlBuilder strings.Builder
	var args []any

	sqlBuilder.WriteString("UPDATE ")
	sqlBuilder.WriteString(q.Update.Table)
	sqlBuilder.WriteString(" SET ")

	updates := make([]string, 0, len(q.Update.Values))
	for _, field := range dbCore.SortedKeys(q.Update.Values) {
		updates = append(updates, field+" = ?")
		args = append(args, q.Update.Values[field])
	}
	sqlBuilder.WriteString(strings.Join(updates, ", "))

	if q.Where != nil {
		whereSQL, whereArgs := q.Where.ToSQL()
		if whereSQL != "" {
			sqlBuilder.WriteString(" WHERE ")
			sqlBuilder.WriteString(ilikeRE.ReplaceAllString(whereSQL, "LIKE"))
			args = append(args, whereArgs...)
		}
	}

	return sqlBuilder.String(), args, nil
}

// formatDelete generates SQL for DELETE queries.
func (d *MySQLDialect) formatDelete(q *dbCore.Query) (string, []any, error) {
	if len(q.Returning) > 0 {
		if _, _, err := d.FormatReturning(q.Returning); err != nil {
			return "", nil, err
		}
	}

	var sqlBuilder strings.Builder
	var args []any

	sqlBuilder.WriteString("DELETE FROM ")
	sqlBuilder.WriteString(q.Delete.Table)

	if q.Where != nil {
		whereSQL, whereArgs := q.Where.ToSQL()
		if whereSQL != "" {
			sqlBuilder.WriteString(" WHERE ")
			sqlBuilder.WriteString(ilikeRE.ReplaceAllString(whereSQL, "LIKE"))
			args = append(args, whereArgs...)
		}
	}

	return sqlBuilder.String(), args, nil
}

// mysqlMaxLimit is the sentinel row count MySQL's own documentation prescribes
// for "all rows after an offset", since a bare OFFSET with no LIMIT is a syntax
// error. It is 2^64-1.
const mysqlMaxLimit = "18446744073709551615"

// formatSelect renders a complete SELECT.
func (d *MySQLDialect) formatSelect(query *dbCore.Query) (string, []any, error) {
	var parts []string
	var allArgs []any

	if len(query.Returning) > 0 {
		if _, _, err := d.FormatReturning(query.Returning); err != nil {
			return "", nil, err
		}
	}

	// CTEs (MySQL 8.0+).
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

	if query.From != nil {
		fromSQL, fromArgs, err := d.formatFromClause(query.From)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, fromSQL)
		allArgs = append(allArgs, fromArgs...)
	}

	for _, join := range query.Joins {
		joinSQL, joinArgs, err := d.FormatJoin(join)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, joinSQL)
		allArgs = append(allArgs, joinArgs...)
	}

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

	if query.OrderBy != nil && len(query.OrderBy.Fields) > 0 {
		orderParts := make([]string, 0, len(query.OrderBy.Fields))
		for _, field := range query.OrderBy.Fields {
			fieldSQL, fieldArgs := d.orderByField(field)
			orderParts = append(orderParts, fieldSQL)
			allArgs = append(allArgs, fieldArgs...)
		}
		parts = append(parts, "ORDER BY "+strings.Join(orderParts, ", "))
	}

	// MySQL rejects OFFSET without LIMIT, so synthesise the documented maximum.
	switch {
	case query.Limit != nil:
		limitSQL, limitArgs, err := d.FormatLimit(*query.Limit)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, limitSQL)
		allArgs = append(allArgs, limitArgs...)
	case query.Offset != nil:
		parts = append(parts, "LIMIT "+mysqlMaxLimit)
	}

	if query.Offset != nil {
		offsetSQL, offsetArgs, err := d.FormatOffset(*query.Offset)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, offsetSQL)
		allArgs = append(allArgs, offsetArgs...)
	}

	for _, union := range query.Unions {
		unionSQL, unionArgs, err := d.FormatUnion(union.Query, union.All)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, unionSQL)
		allArgs = append(allArgs, unionArgs...)
	}

	return strings.Join(parts, " "), allArgs, nil
}
