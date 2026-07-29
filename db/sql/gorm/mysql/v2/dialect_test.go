package v2

import (
	"strings"
	"testing"

	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This suite is pure string assertion: it runs with no MySQL server anywhere.
// There is one case per SQLDialect method, pinning either the exact SQL or the
// exact refusal.

func d() *MySQLDialect { return &MySQLDialect{} }

// ---------------------------------------------------------------- identifiers

func TestQuoteIdentifierUsesBackticks(t *testing.T) {
	tests := []struct{ in, want string }{
		{"id", "`id`"},
		{"a.b", "`a`.`b`"},
		{"schema.tbl.col", "`schema`.`tbl`.`col`"},
		// An embedded backtick is doubled, the MySQL escape.
		{"a`b", "`a``b`"},
		// A quote character has no special meaning inside backticks.
		{`a"b`, "`a\"b`"},
	}

	for _, tt := range tests {
		assert.Equalf(t, tt.want, d().QuoteIdentifier(tt.in), "QuoteIdentifier(%q)", tt.in)
	}
}

// TestQuoteIdentifierCannotBeBrokenOut is the security-relevant case: a payload
// in an identifier position stays inside the backtick quoting.
func TestQuoteIdentifierCannotBeBrokenOut(t *testing.T) {
	got := d().QuoteIdentifier("id` FROM users WHERE 1=1 -- ")
	assert.Equal(t, "`id`` FROM users WHERE 1=1 -- `", got)
	assert.Equal(t, 1, strings.Count(got, "FROM"))
}

func TestNameIsMySQL(t *testing.T) {
	assert.Equal(t, "mysql", d().Name())
	assert.Equal(t, DialectName, d().Name())
}

// ------------------------------------------------------------------- SELECT

func TestFormatSelect(t *testing.T) {
	sql, args, err := d().FormatSelect([]string{"id", "name"}, false, nil)
	require.NoError(t, err)
	assert.Equal(t, "SELECT id, name", sql)
	assert.Empty(t, args)

	sql, _, err = d().FormatSelect([]string{"id"}, true, nil)
	require.NoError(t, err)
	assert.Equal(t, "SELECT DISTINCT id", sql)
}

// TestFormatSelectRefusesDistinctOn: MySQL has no DISTINCT ON, and degrading it
// to a plain DISTINCT would return a different row set, so it must fail.
func TestFormatSelectRefusesDistinctOn(t *testing.T) {
	sql, _, err := d().FormatSelect([]string{"id"}, true, []string{"user_id"})

	requireUnsupported(t, err, "DISTINCT ON")
	assert.Empty(t, sql)
	assert.Contains(t, err.Error(), "ROW_NUMBER()")
}

func TestFormatDistinctOnAlwaysFails(t *testing.T) {
	sql, _, err := d().FormatDistinctOn([]string{"a"})
	requireUnsupported(t, err, "DISTINCT ON")
	assert.Empty(t, sql)
}

// --------------------------------------------------------------------- FROM

func TestFormatFrom(t *testing.T) {
	sql, _, err := d().FormatFrom("users", "")
	require.NoError(t, err)
	assert.Equal(t, "FROM users", sql)

	sql, _, err = d().FormatFrom("users", "u")
	require.NoError(t, err)
	assert.Equal(t, "FROM users AS u", sql)
}

// --------------------------------------------------------------------- JOIN

func TestFormatJoinSupportedTypes(t *testing.T) {
	for _, joinType := range []string{"INNER", "LEFT", "RIGHT", "CROSS"} {
		sql, _, err := d().FormatJoin(&dbCore.JoinClause{
			Type:      joinType,
			Table:     "orders",
			Condition: &dbCore.RawCondition{SQL: "users.id = orders.user_id"},
		})
		require.NoErrorf(t, err, "join type %s", joinType)
		assert.Equal(t, joinType+" JOIN orders ON users.id = orders.user_id", sql)
	}
}

// TestFormatJoinRefusesFullOuterJoin: MySQL has no FULL OUTER JOIN. The
// workaround is a UNION of a LEFT and a RIGHT join, which changes the query shape
// enough that the dialect must not substitute it silently.
func TestFormatJoinRefusesFullOuterJoin(t *testing.T) {
	for _, joinType := range []string{"FULL", "full", "FULL OUTER"} {
		sql, _, err := d().FormatJoin(&dbCore.JoinClause{Type: joinType, Table: "orders"})

		requireUnsupported(t, err, "FULL OUTER JOIN")
		assert.Empty(t, sql)
		assert.Contains(t, err.Error(), "LEFT JOIN")
	}
}

func TestFormatJoinLateralIsEmittedAsIs(t *testing.T) {
	// LATERAL requires MySQL 8.0.14+; nothing to rewrite.
	sql, _, err := d().FormatJoin(&dbCore.JoinClause{
		Type:       "LATERAL",
		IsSubquery: true,
		Alias:      "recent",
		Subquery: &dbCore.Query{
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "orders"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "LATERAL JOIN (SELECT id FROM orders) AS recent", sql)
}

// -------------------------------------------------------------------- WHERE

func TestFormatWhere(t *testing.T) {
	sql, args, err := d().FormatWhere(&dbCore.WhereClause{
		Operator: "AND",
		Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "age", Operator: ">", Right: 18},
			&dbCore.IsNullCondition{Field: "deleted_at"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE age > ? AND deleted_at IS NULL", sql)
	assert.Equal(t, []any{18}, args)
}

func TestFormatWhereEmptyClause(t *testing.T) {
	sql, _, err := d().FormatWhere(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)

	sql, _, err = d().FormatWhere(&dbCore.WhereClause{Operator: "AND"})
	require.NoError(t, err)
	assert.Empty(t, sql)
}

// TestFormatWhereRewritesILIKE: MySQL has no ILIKE. LIKE is case-insensitive
// under utf8mb4_unicode_ci, the collation this dialect targets.
func TestFormatWhereRewritesILIKE(t *testing.T) {
	sql, args, err := d().FormatWhere(&dbCore.WhereClause{
		Operator: "AND",
		Conditions: []dbCore.Condition{
			&dbCore.RawCondition{SQL: "name ILIKE ?", Args: []any{"%ann%"}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE name LIKE ?", sql)
	assert.Equal(t, []any{"%ann%"}, args)
	assert.NotContains(t, sql, "ILIKE")
}

func TestFormatWhereILIKERewriteIsWordBounded(t *testing.T) {
	// A column literally named "notilike" must not be mangled.
	sql, _, err := d().FormatWhere(&dbCore.WhereClause{
		Operator: "AND",
		Conditions: []dbCore.Condition{
			&dbCore.RawCondition{SQL: "notilike = 1 AND x ILIKE 'a'"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE notilike = 1 AND x LIKE 'a'", sql)
}

// ----------------------------------------------------------------- ORDER BY

func TestFormatOrderByQuotesAndNormalizes(t *testing.T) {
	sql, args, err := d().FormatOrderBy("created_at", "desc")
	require.NoError(t, err)
	assert.Equal(t, "ORDER BY `created_at` DESC", sql)
	assert.Empty(t, args)

	sql, _, err = d().FormatOrderBy("members.created_at", "asc")
	require.NoError(t, err)
	assert.Equal(t, "ORDER BY `members`.`created_at` ASC", sql)

	// Anything unrecognized in the direction slot degrades to ASC.
	sql, _, err = d().FormatOrderBy("id", "; DROP TABLE users")
	require.NoError(t, err)
	assert.Equal(t, "ORDER BY `id` ASC", sql)
}

// TestFormatOrderByBindsNonIdentifier mirrors the Postgres hardening: an
// untrusted non-identifier is bound as a placeholder, never interpolated.
func TestFormatOrderByBindsNonIdentifier(t *testing.T) {
	sql, args, err := d().FormatOrderBy("(SELECT 1)", "asc")
	require.NoError(t, err)
	assert.Equal(t, "ORDER BY ? ASC", sql)
	assert.Equal(t, []any{"(SELECT 1)"}, args)
	assert.NotContains(t, sql, "SELECT 1")
}

// ----------------------------------------------------------------- GROUP BY

func TestFormatGroupByPlainFields(t *testing.T) {
	sql, _, err := d().FormatGroupBy(&dbCore.GroupByClause{Fields: []string{"status", "type"}})
	require.NoError(t, err)
	assert.Equal(t, "GROUP BY status, type", sql)
}

// TestFormatGroupByRollupUsesTrailingWithRollup: ROLLUP exists on MySQL but in a
// different syntactic position — trailing WITH ROLLUP rather than Postgres'
// prefix ROLLUP (a, b).
func TestFormatGroupByRollupUsesTrailingWithRollup(t *testing.T) {
	sql, _, err := d().FormatGroupBy(&dbCore.GroupByClause{Rollup: []string{"region", "city"}})
	require.NoError(t, err)
	assert.Equal(t, "GROUP BY region, city WITH ROLLUP", sql)
	assert.NotContains(t, sql, "ROLLUP (")
}

func TestFormatGroupByRollupAppendsToPlainFields(t *testing.T) {
	sql, _, err := d().FormatGroupBy(&dbCore.GroupByClause{
		Fields: []string{"year"},
		Rollup: []string{"region"},
	})
	require.NoError(t, err)
	assert.Equal(t, "GROUP BY year, region WITH ROLLUP", sql)
}

func TestFormatGroupByRefusesCubeAndGroupingSets(t *testing.T) {
	sql, _, err := d().FormatGroupBy(&dbCore.GroupByClause{Cube: []string{"a", "b"}})
	requireUnsupported(t, err, "CUBE")
	assert.Empty(t, sql)
	assert.Contains(t, err.Error(), "UNION ALL")

	sql, _, err = d().FormatGroupBy(&dbCore.GroupByClause{Sets: [][]string{{"a"}, {"a", "b"}}})
	requireUnsupported(t, err, "GROUPING SETS")
	assert.Empty(t, sql)
}

func TestFormatGroupByEmptyEmitsNothing(t *testing.T) {
	for _, clause := range []*dbCore.GroupByClause{nil, {}} {
		sql, _, err := d().FormatGroupBy(clause)
		require.NoError(t, err)
		assert.Empty(t, sql, "must never emit a bare GROUP BY")
	}
}

// ------------------------------------------------------------------- HAVING

func TestFormatHaving(t *testing.T) {
	sql, args, err := d().FormatHaving(&dbCore.HavingClause{
		Condition: &dbCore.BinaryCondition{Left: "COUNT(*)", Operator: ">", Right: 5},
	})
	require.NoError(t, err)
	assert.Equal(t, "HAVING COUNT(*) > ?", sql)
	assert.Equal(t, []any{5}, args)

	sql, _, err = d().FormatHaving(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)
}

func TestFormatHavingRewritesILIKE(t *testing.T) {
	sql, _, err := d().FormatHaving(&dbCore.HavingClause{
		Condition: &dbCore.RawCondition{SQL: "MAX(name) ILIKE 'a%'"},
	})
	require.NoError(t, err)
	assert.Equal(t, "HAVING MAX(name) LIKE 'a%'", sql)
}

// ------------------------------------------------------------ LIMIT / OFFSET

func TestFormatLimitAndOffset(t *testing.T) {
	sql, _, err := d().FormatLimit(10)
	require.NoError(t, err)
	assert.Equal(t, "LIMIT 10", sql)

	sql, _, err = d().FormatOffset(20)
	require.NoError(t, err)
	assert.Equal(t, "OFFSET 20", sql)
}

// TestOffsetWithoutLimitSynthesisesMaxLimit: MySQL rejects a bare OFFSET, unlike
// Postgres, so the dialect supplies the row count MySQL's own docs prescribe.
func TestOffsetWithoutLimitSynthesisesMaxLimit(t *testing.T) {
	sql, _, err := NewBuilder().Select("id").From("users").Offset(20).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users LIMIT 18446744073709551615 OFFSET 20", sql)
}

func TestLimitAndOffsetTogether(t *testing.T) {
	sql, _, err := NewBuilder().Select("id").From("users").Limit(10).Offset(20).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users LIMIT 10 OFFSET 20", sql)
}

// ---------------------------------------------------------------------- CTE

func TestFormatCTE(t *testing.T) {
	sql, _, err := d().FormatCTE("recent", &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"id"}},
		From:   &dbCore.FromClause{Table: "orders"},
	})
	require.NoError(t, err)
	assert.Equal(t, "recent AS (SELECT id FROM orders)", sql)
}

// TestCTEsAreSupportedAsIs — WITH requires MySQL 8.0, which this dialect targets.
func TestCTEsAreSupportedAsIs(t *testing.T) {
	cte := NewBuilder().Select("user_id", "COUNT(*) AS n").From("orders").GroupBy("user_id").Build()

	sql, _, err := NewBuilder().
		WithCTE("user_orders", cte).
		Select("u.id", "uo.n").
		From("users u").
		InnerJoin("user_orders uo", &dbCore.RawCondition{SQL: "u.id = uo.user_id"}).
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t,
		"WITH user_orders AS (SELECT user_id, COUNT(*) AS n FROM orders GROUP BY user_id) "+
			"SELECT u.id, uo.n FROM users u INNER JOIN user_orders uo ON u.id = uo.user_id",
		sql)
}

// TestCTEErrorPropagates proves a refusal inside a CTE reaches the caller rather
// than being swallowed into invalid SQL.
func TestCTEErrorPropagates(t *testing.T) {
	cte := NewBuilder().Select("id").From("orders").DistinctOn("user_id").Build()
	cte.Select.Distinct = true

	sql, _, err := NewBuilder().WithCTE("c", cte).Select("id").From("users").ToSQL()

	requireUnsupported(t, err, "DISTINCT ON")
	assert.Empty(t, sql)
}

// -------------------------------------------------------------------- UNION

func TestFormatUnion(t *testing.T) {
	q := &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"id"}},
		From:   &dbCore.FromClause{Table: "archived_users"},
	}

	sql, _, err := d().FormatUnion(q, false)
	require.NoError(t, err)
	assert.Equal(t, "UNION SELECT id FROM archived_users", sql)

	sql, _, err = d().FormatUnion(q, true)
	require.NoError(t, err)
	assert.Equal(t, "UNION ALL SELECT id FROM archived_users", sql)
}

// ------------------------------------------------------------------- WINDOW

func TestFormatWindow(t *testing.T) {
	sql, _, err := d().FormatWindow("w", &dbCore.WindowDefinition{
		PartitionBy: []string{"user_id"},
		OrderBy:     []dbCore.OrderByField{{Field: "created_at", Direction: "DESC"}},
	})
	require.NoError(t, err)
	assert.Contains(t, sql, "w AS (")
	assert.Contains(t, sql, "PARTITION BY user_id")

	sql, _, err = d().FormatWindow("w", nil)
	require.NoError(t, err)
	assert.Empty(t, sql)
}

// ----------------------------------------------------------------- SUBQUERY

func TestFormatSubquery(t *testing.T) {
	q := &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"user_id"}},
		From:   &dbCore.FromClause{Table: "orders"},
	}

	sql, _, err := d().FormatSubquery(q, "o")
	require.NoError(t, err)
	assert.Equal(t, "(SELECT user_id FROM orders) AS o", sql)

	sql, _, err = d().FormatSubquery(q, "")
	require.NoError(t, err)
	assert.Equal(t, "(SELECT user_id FROM orders)", sql)
}

// ---------------------------------------------------------------- RETURNING

// TestFormatReturningAlwaysFails: MySQL has no RETURNING at all.
func TestFormatReturningAlwaysFails(t *testing.T) {
	sql, _, err := d().FormatReturning([]string{"id"})

	requireUnsupported(t, err, "RETURNING")
	assert.Empty(t, sql)
	assert.Contains(t, err.Error(), "LAST_INSERT_ID()")
}

func TestReturningRefusedOnEveryStatementKind(t *testing.T) {
	tests := map[string]func() (string, []any, error){
		"INSERT": func() (string, []any, error) {
			return NewBuilder().Insert("users").Columns("name").Values("ann").Returning("id").ToSQL()
		},
		"UPDATE": func() (string, []any, error) {
			return NewBuilder().Update("users").Set("name", "ann").Returning("id").ToSQL()
		},
		"DELETE": func() (string, []any, error) {
			return NewBuilder().Delete("users").Eq("id", 1).Returning("id").ToSQL()
		},
		"SELECT": func() (string, []any, error) {
			return NewBuilder().Select("id").From("users").Returning("id").ToSQL()
		},
	}

	for kind, build := range tests {
		t.Run(kind, func(t *testing.T) {
			sql, _, err := build()
			requireUnsupported(t, err, "RETURNING")
			assert.Empty(t, sql, "no partially rendered SQL may escape")
		})
	}
}

// ------------------------------------------------------------- INSERT/UPDATE

func TestFormatQueryInsert(t *testing.T) {
	sql, args, err := NewBuilder().
		Insert("users").
		Columns("id", "name").
		Values(1, "ann").
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users (id, name) VALUES (?, ?)", sql)
	assert.Equal(t, []any{1, "ann"}, args)
}

func TestFormatQueryInsertFromSelect(t *testing.T) {
	src := NewBuilder().Select("id", "name").From("staging_users").Build()

	sql, _, err := NewBuilder().Insert("users").Columns("id", "name").FromSelect(src).ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users (id, name) SELECT id, name FROM staging_users", sql)
}

// TestOnConflictDoUpdateIsRefusedByDefault is the Tier-D fix, and the one v2 rule
// that had been answered with documentation instead of an error.
//
// Translating ON CONFLICT (cols) DO UPDATE into ON DUPLICATE KEY UPDATE produces
// *valid but wrong* SQL: MySQL fires on a duplicate in any unique index rather than
// the conflict target named, so the target list is dropped. That is worse than
// invalid SQL, because the server accepts it and a doc warning does not stop it
// executing.
func TestOnConflictDoUpdateIsRefusedByDefault(t *testing.T) {
	sql, _, err := NewBuilder().
		Insert("users").
		Columns("id", "name").
		Values(1, "ann").
		OnConflict("id").
		DoUpdate(map[string]interface{}{"name": "ann"}).
		ToSQL()

	requireUnsupported(t, err, "ON CONFLICT ... DO UPDATE")
	assert.Empty(t, sql, "no unfaithful SQL may escape")
	assert.Contains(t, err.Error(), "AllowUnfaithfulUpsert",
		"the error must name the opt-in")
	assert.Contains(t, err.Error(), "exactly one unique constraint",
		"and the condition under which opting in is safe")
}

// TestOnConflictDoUpdateWithTheOptIn: a caller who has read the caveat gets the
// translation, unchanged from before.
func TestOnConflictDoUpdateWithTheOptIn(t *testing.T) {
	permissive := NewBuilderWithDialect(&MySQLDialect{AllowUnfaithfulUpsert: true})

	sql, args, err := permissive.
		Insert("users").
		Columns("id", "name").
		Values(1, "ann").
		OnConflict("id").
		DoUpdate(map[string]interface{}{"name": "ann", "seen_at": "now"}).
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t,
		"INSERT INTO users (id, name) VALUES (?, ?) ON DUPLICATE KEY UPDATE name = ?, seen_at = ?",
		sql)
	assert.Equal(t, []any{1, "ann", "ann", "now"}, args)
	assert.NotContains(t, sql, "ON CONFLICT",
		"the conflict target has nowhere to go in MySQL and is dropped")
}

// TestDoNothingBecomesSelfAssignment: MySQL has no DO NOTHING, so the idiomatic
// no-op is assigning a column to itself. This translation IS faithful, so it needs
// no opt-in — unlike DO UPDATE above.
func TestDoNothingBecomesSelfAssignment(t *testing.T) {
	sql, args, err := NewBuilder().
		Insert("users").
		Columns("id", "name").
		Values(1, "ann").
		OnConflict("id").
		DoNothing().
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t,
		"INSERT INTO users (id, name) VALUES (?, ?) ON DUPLICATE KEY UPDATE `id` = `id`",
		sql)
	assert.Equal(t, []any{1, "ann"}, args)
}

func TestOnConflictWithoutActionIsAnError(t *testing.T) {
	sql, _, err := NewBuilder().
		Insert("users").
		Columns("id").
		Values(1).
		OnConflict("id").
		ToSQL()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DoNothing()")
	assert.Empty(t, sql)
}

func TestFormatQueryUpdate(t *testing.T) {
	sql, args, err := NewBuilder().
		Update("users").
		SetMap(map[string]interface{}{"name": "ann", "age": 30}).
		Eq("id", 1).
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "UPDATE users SET age = ?, name = ? WHERE id = ?", sql)
	assert.Equal(t, []any{30, "ann", 1}, args)
}

func TestUpdateWithoutSetIsAnError(t *testing.T) {
	sql, _, err := NewBuilder().Update("users").Eq("id", 1).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SET")
	assert.Empty(t, sql)
}

func TestFormatQueryDelete(t *testing.T) {
	sql, args, err := NewBuilder().Delete("users").Eq("id", 1).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM users WHERE id = ?", sql)
	assert.Equal(t, []any{1}, args)
}

func TestFormatQueryRejectsNil(t *testing.T) {
	_, _, err := d().FormatQuery(nil)
	require.Error(t, err)
}

// --------------------------------------------------------- clause ordering

func TestFullSelectClauseOrder(t *testing.T) {
	sql, args, err := NewBuilder().
		Select("u.id", "COUNT(o.id) AS orders").
		From("users u").
		LeftJoin("orders o", &dbCore.RawCondition{SQL: "o.user_id = u.id"}).
		Eq("u.status", "active").
		GroupBy("u.id").
		Having(&dbCore.BinaryCondition{Left: "COUNT(o.id)", Operator: ">", Right: 2}).
		OrderBy("u.id", "desc").
		Limit(10).
		Offset(20).
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t,
		"SELECT u.id, COUNT(o.id) AS orders FROM users u "+
			"LEFT JOIN orders o ON o.user_id = u.id "+
			"WHERE u.status = ? "+
			"GROUP BY u.id "+
			"HAVING COUNT(o.id) > ? "+
			"ORDER BY `u`.`id` DESC "+
			"LIMIT 10 OFFSET 20",
		sql)
	assert.Equal(t, []any{"active", 2}, args)
}

// TestDeterministicOutput pins that repeated renders of the same query are
// byte-identical, which map-ranged SET clauses used to prevent.
func TestDeterministicOutput(t *testing.T) {
	build := func() string {
		sql, _, err := NewBuilder().
			Update("users").
			SetMap(map[string]interface{}{"c": 3, "a": 1, "b": 2}).
			ToSQL()
		require.NoError(t, err)
		return sql
	}

	first := build()
	for i := 0; i < 25; i++ {
		assert.Equal(t, first, build())
	}
}

// TestPlaceholderStyleIsQuestionMark documents why no placeholder rewriting was
// needed: the shared builder and conditions already emit MySQL's '?' rather than
// Postgres' $n.
func TestPlaceholderStyleIsQuestionMark(t *testing.T) {
	sql, args, err := NewBuilder().
		Select("id").
		From("users").
		Eq("a", 1).
		Eq("b", 2).
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users WHERE a = ? AND b = ?", sql)
	assert.Equal(t, 2, strings.Count(sql, "?"))
	assert.NotContains(t, sql, "$1")
	assert.Len(t, args, 2)
}

func requireUnsupported(t *testing.T, err error, construct string) {
	t.Helper()
	require.Error(t, err)
	require.Truef(t, dbCore.IsUnsupported(err), "expected an UnsupportedError, got %T: %v", err, err)

	var unsupportedErr *dbCore.UnsupportedError
	require.ErrorAs(t, err, &unsupportedErr)
	assert.Equal(t, DialectName, unsupportedErr.Dialect)
	assert.Equal(t, construct, unsupportedErr.Construct)
	assert.Contains(t, err.Error(), "mysql does not support "+construct)
}
