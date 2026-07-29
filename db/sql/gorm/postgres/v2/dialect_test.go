package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// One case per SQLDialect method, pinning the exact SQL. Pure string assertions:
// this suite runs with no Postgres server anywhere.

func pgd() *PostgresDialect { return &PostgresDialect{} }

func TestPostgresName(t *testing.T) {
	assert.Equal(t, "postgres", pgd().Name())
	assert.Equal(t, DialectName, pgd().Name())
}

func TestPostgresQuoteIdentifier(t *testing.T) {
	tests := []struct{ in, want string }{
		{"id", `"id"`},
		{"a.b", `"a"."b"`},
		{"schema.tbl.col", `"schema"."tbl"."col"`},
		{`a"b`, `"a""b"`},
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, pgd().QuoteIdentifier(tt.in), "QuoteIdentifier(%q)", tt.in)
	}
}

func TestPostgresFormatSelect(t *testing.T) {
	sql, args, err := pgd().FormatSelect([]string{"id", "name"}, false, nil)
	require.NoError(t, err)
	assert.Equal(t, "SELECT id, name", sql)
	assert.Empty(t, args)

	sql, _, err = pgd().FormatSelect([]string{"id"}, true, nil)
	require.NoError(t, err)
	assert.Equal(t, "SELECT DISTINCT id", sql)

	// DISTINCT ON is native here — the whole point of the MySQL refusal.
	sql, _, err = pgd().FormatSelect([]string{"id", "user_id"}, true, []string{"user_id"})
	require.NoError(t, err)
	assert.Equal(t, "SELECT DISTINCT ON (user_id) id, user_id", sql)
}

func TestPostgresFormatFrom(t *testing.T) {
	sql, _, err := pgd().FormatFrom("users", "")
	require.NoError(t, err)
	assert.Equal(t, "FROM users", sql)

	sql, _, err = pgd().FormatFrom("users", "u")
	require.NoError(t, err)
	assert.Equal(t, "FROM users AS u", sql)
}

func TestPostgresFormatJoin(t *testing.T) {
	on := &dbCore.RawCondition{SQL: "users.id = orders.user_id"}

	for _, joinType := range []string{"INNER", "LEFT", "RIGHT", "FULL", "CROSS", "NATURAL"} {
		sql, _, err := pgd().FormatJoin(&dbCore.JoinClause{Type: joinType, Table: "orders", Condition: on})
		require.NoErrorf(t, err, "join type %s", joinType)
		assert.Equal(t, joinType+" JOIN orders ON users.id = orders.user_id", sql)
	}

	// No condition.
	sql, _, err := pgd().FormatJoin(&dbCore.JoinClause{Type: "CROSS", Table: "orders"})
	require.NoError(t, err)
	assert.Equal(t, "CROSS JOIN orders", sql)

	// Alias.
	sql, _, err = pgd().FormatJoin(&dbCore.JoinClause{Type: "INNER", Table: "orders", Alias: "o", Condition: on})
	require.NoError(t, err)
	assert.Equal(t, "INNER JOIN orders AS o ON users.id = orders.user_id", sql)
}

func TestPostgresFormatJoinSubquery(t *testing.T) {
	sub := NewBuilder().Select("user_id").From("orders").Build()

	sql, _, err := pgd().FormatJoin(&dbCore.JoinClause{
		Type:       "LATERAL",
		IsSubquery: true,
		Alias:      "o",
		Subquery:   sub,
		Condition:  &dbCore.RawCondition{SQL: "true"},
	})
	require.NoError(t, err)
	assert.Equal(t, "LATERAL JOIN (SELECT user_id FROM orders) AS o ON true", sql)

	// Without a condition.
	sql, _, err = pgd().FormatJoin(&dbCore.JoinClause{
		Type: "INNER", IsSubquery: true, Alias: "o", Subquery: sub,
	})
	require.NoError(t, err)
	assert.Equal(t, "INNER JOIN (SELECT user_id FROM orders) AS o", sql)
}

func TestPostgresFormatWhere(t *testing.T) {
	sql, args, err := pgd().FormatWhere(&dbCore.WhereClause{
		Operator: "AND",
		Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "age", Operator: ">", Right: 18},
			&dbCore.IsNullCondition{Field: "deleted_at"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE age > ? AND deleted_at IS NULL", sql)
	assert.Equal(t, []any{18}, args)

	// OR operator.
	sql, _, err = pgd().FormatWhere(&dbCore.WhereClause{
		Operator: "OR",
		Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
			&dbCore.BinaryCondition{Left: "b", Operator: "=", Right: 2},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE a = ? OR b = ?", sql)

	// Empty and nil.
	sql, _, err = pgd().FormatWhere(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)

	sql, _, err = pgd().FormatWhere(&dbCore.WhereClause{Operator: "AND"})
	require.NoError(t, err)
	assert.Empty(t, sql)
}

// TestPostgresFormatWhereSkipsEmptyConditionSQL covers a condition that renders
// nothing; it must be dropped, not joined as an empty term.
func TestPostgresFormatWhereSkipsEmptyConditionSQL(t *testing.T) {
	sql, _, err := pgd().FormatWhere(&dbCore.WhereClause{
		Operator: "AND",
		Conditions: []dbCore.Condition{
			&dbCore.RawCondition{SQL: ""},
			&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "WHERE a = ?", sql)
}

func TestPostgresFormatHaving(t *testing.T) {
	sql, args, err := pgd().FormatHaving(&dbCore.HavingClause{
		Condition: &dbCore.BinaryCondition{Left: "COUNT(*)", Operator: ">", Right: 5},
	})
	require.NoError(t, err)
	assert.Equal(t, "HAVING COUNT(*) > ?", sql)
	assert.Equal(t, []any{5}, args)

	sql, _, err = pgd().FormatHaving(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)
}

func TestPostgresFormatLimitOffset(t *testing.T) {
	sql, _, err := pgd().FormatLimit(10)
	require.NoError(t, err)
	assert.Equal(t, "LIMIT 10", sql)

	sql, _, err = pgd().FormatOffset(20)
	require.NoError(t, err)
	assert.Equal(t, "OFFSET 20", sql)
}

// TestPostgresBareOffsetIsLegal is the counterpart to the MySQL behaviour: unlike
// MySQL, Postgres accepts OFFSET with no LIMIT, so no LIMIT is synthesised.
func TestPostgresBareOffsetIsLegal(t *testing.T) {
	sql, _, err := NewBuilder().Select("id").From("users").Offset(20).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users OFFSET 20", sql)
	assert.NotContains(t, sql, "LIMIT")
}

func TestPostgresFormatCTEUnionWindowSubqueryDistinctOnReturning(t *testing.T) {
	inner := NewBuilder().Select("id").From("orders").Build()

	sql, _, err := pgd().FormatCTE("c", inner)
	require.NoError(t, err)
	assert.Equal(t, "c AS (SELECT id FROM orders)", sql)

	sql, _, err = pgd().FormatUnion(inner, false)
	require.NoError(t, err)
	assert.Equal(t, "UNION SELECT id FROM orders", sql)

	sql, _, err = pgd().FormatUnion(inner, true)
	require.NoError(t, err)
	assert.Equal(t, "UNION ALL SELECT id FROM orders", sql)

	sql, _, err = pgd().FormatWindow("w", &dbCore.WindowDefinition{PartitionBy: []string{"user_id"}})
	require.NoError(t, err)
	assert.Contains(t, sql, "w AS (")

	sql, _, err = pgd().FormatSubquery(inner, "o")
	require.NoError(t, err)
	assert.Equal(t, "(SELECT id FROM orders) AS o", sql)

	sql, _, err = pgd().FormatSubquery(inner, "")
	require.NoError(t, err)
	assert.Equal(t, "(SELECT id FROM orders)", sql)

	sql, _, err = pgd().FormatDistinctOn([]string{"a", "b"})
	require.NoError(t, err)
	assert.Equal(t, "DISTINCT ON (a, b)", sql)

	sql, _, err = pgd().FormatReturning([]string{"id", "name"})
	require.NoError(t, err)
	assert.Equal(t, "RETURNING id, name", sql)
}

func TestPostgresFormatQueryInsertVariants(t *testing.T) {
	// Plain multi-row insert.
	sql, args, err := NewBuilder().Insert("users").Columns("id", "name").
		Values(1, "ann").Values(2, "bo").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users (id, name) VALUES (?, ?), (?, ?)", sql)
	assert.Equal(t, []any{1, "ann", 2, "bo"}, args)

	// No columns given.
	sql, _, err = NewBuilder().Insert("users").Values(1).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users VALUES (?)", sql)

	// INSERT ... SELECT.
	src := NewBuilder().Select("id").From("staging").Build()
	sql, _, err = NewBuilder().Insert("users").Columns("id").FromSelect(src).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users (id) SELECT id FROM staging", sql)

	// ON CONFLICT with no column list.
	sql, _, err = NewBuilder().Insert("users").Columns("id").Values(1).
		OnConflict().DoNothing().ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users (id) VALUES (?) ON CONFLICT DO NOTHING", sql)
}

func TestPostgresFormatQueryUpdateAndDelete(t *testing.T) {
	sql, args, err := NewBuilder().Update("users").
		SetMap(map[string]interface{}{"name": "ann", "age": 30}).Eq("id", 1).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "UPDATE users SET age = ?, name = ? WHERE id = ?", sql)
	assert.Equal(t, []any{30, "ann", 1}, args)

	// UPDATE without WHERE.
	sql, _, err = NewBuilder().Update("users").Set("a", 1).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "UPDATE users SET a = ?", sql)

	sql, args, err = NewBuilder().Delete("users").Eq("id", 1).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM users WHERE id = ?", sql)
	assert.Equal(t, []any{1}, args)
}

func TestPostgresFullSelectClauseOrder(t *testing.T) {
	cte := NewBuilder().Select("user_id").From("orders").Build()
	union := NewBuilder().Select("id").From("archived").Build()

	sql, args, err := NewBuilder().
		WithCTE("recent", cte).
		Select("u.id").
		From("users u").
		LeftJoin("orders o", &dbCore.RawCondition{SQL: "o.user_id = u.id"}).
		Eq("u.status", "active").
		GroupBy("u.id").
		Having(&dbCore.BinaryCondition{Left: "COUNT(o.id)", Operator: ">", Right: 2}).
		OrderBy("u.id", "desc").
		Limit(10).
		Offset(20).
		Union(union).
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t,
		"WITH recent AS (SELECT user_id FROM orders) "+
			"SELECT u.id FROM users u "+
			"LEFT JOIN orders o ON o.user_id = u.id "+
			"WHERE u.status = ? "+
			"GROUP BY u.id "+
			"HAVING COUNT(o.id) > ? "+
			`ORDER BY "u"."id" DESC `+
			"LIMIT 10 OFFSET 20 "+
			"UNION SELECT id FROM archived",
		sql)
	assert.Equal(t, []any{"active", 2}, args)
}

func TestPostgresReturningOnSelect(t *testing.T) {
	sql, _, err := NewBuilder().Select("id").From("users").Returning("id").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users RETURNING id", sql)
}

func TestPostgresFormatQueryDispatchesByStatementKind(t *testing.T) {
	sql, _, err := pgd().FormatQuery(&dbCore.Query{Insert: &dbCore.InsertClause{Table: "t"}})
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO t", sql)

	sql, _, err = pgd().FormatQuery(&dbCore.Query{
		Update: &dbCore.UpdateClause{Table: "t", Values: map[string]interface{}{"a": 1}},
	})
	require.NoError(t, err)
	assert.Equal(t, "UPDATE t SET a = ?", sql)

	sql, _, err = pgd().FormatQuery(&dbCore.Query{Delete: &dbCore.DeleteClause{Table: "t"}})
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM t", sql)

	sql, _, err = pgd().FormatQuery(&dbCore.Query{})
	require.NoError(t, err)
	assert.Equal(t, "SELECT *", sql)
}

func TestPostgresSubqueryInFromRequiresQuery(t *testing.T) {
	_, _, err := pgd().FormatQuery(&dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"id"}},
		From:   &dbCore.FromClause{IsSubquery: true, Alias: "o"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subquery")
}

func TestPostgresOrderByMultipleFieldsSingleKeyword(t *testing.T) {
	sql, args, err := NewBuilder().
		Select("id").
		From("members").
		OrderBy("last_name", "asc").
		OrderBy("members.created_at", "desc").
		OrderByRaw("first_name || ' ' || last_name", "asc").
		ToSQL()

	require.NoError(t, err)
	assert.Equal(t,
		`SELECT id FROM members ORDER BY "last_name" ASC, "members"."created_at" DESC, `+
			`first_name || ' ' || last_name ASC`,
		sql)
	assert.Empty(t, args)
}

// TestPostgresConditionHelperFunctions covers the And/Or/Equal/... helpers this
// package exports alongside the dialect.
func TestPostgresConditionHelperFunctions(t *testing.T) {
	tests := []struct {
		name string
		cond dbCore.Condition
		want string
	}{
		{"Equal", Equal("a", 1), "a = ?"},
		{"NotEqual", NotEqual("a", 1), "a != ?"},
		{"GreaterThan", GreaterThan("a", 1), "a > ?"},
		{"LessThan", LessThan("a", 1), "a < ?"},
		{"In", In("a", 1, 2), "a IN (?, ?)"},
		{"Between", Between("a", 1, 2), "a BETWEEN ? AND ?"},
		{"Raw", Raw("a = 1"), "a = 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, _ := tt.cond.ToSQL()
			assert.Equal(t, tt.want, sql)
		})
	}

	sql, args := And(Equal("a", 1), Equal("b", 2)).ToSQL()
	assert.Equal(t, "(a = ? AND b = ?)", sql)
	assert.Equal(t, []any{1, 2}, args)

	sql, args = Or(Equal("a", 1), Equal("b", 2)).ToSQL()
	assert.Equal(t, "(a = ? OR b = ?)", sql)
	assert.Equal(t, []any{1, 2}, args)
}
