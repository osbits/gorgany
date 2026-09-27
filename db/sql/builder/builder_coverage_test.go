package builder_test

// This file exercises every IQueryBuilder method against real dialects, so the
// engine-agnostic builder and every shipped dialect are covered by pure string
// assertions with no database anywhere. SQL Server brackets every identifier it
// emits, so its expectations are spelled out beside the ones Postgres and MySQL
// share.
//
// It lives in the external test package builder_test, which is what lets it import
// the concrete dialects: v2 imports builder, and builder never imports this test
// package, so there is no cycle.

import (
	"testing"

	"github.com/osbits/gorgany/v2/db/sql/builder"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	mysql "github.com/osbits/gorgany/v2/db/sql/gorm/mysql/v2"
	postgres "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pg() *builder.Builder         { return builder.New(&postgres.PostgresDialect{}) }
func mysqlB() *builder.Builder     { return builder.New(&mysql.MySQLDialect{}) }
func sqlserverB() *builder.Builder { return builder.New(&sqlserver.SQLServerDialect{}) }

func subquery(dialect dbCore.SQLDialect) *dbCore.Query {
	return builder.New(dialect).Select("user_id").From("orders").Build()
}

// ------------------------------------------------------------ condition helpers

func TestConditionHelpersRenderOnEveryDialect(t *testing.T) {
	tests := []struct {
		name          string
		build         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder
		wantSQL       string
		wantSQLServer string
		wantArg       []any
	}{
		{
			name:          "Eq",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Eq("id", 1) },
			wantSQL:       "WHERE id = ?",
			wantSQLServer: "WHERE [id] = ?",
			wantArg:       []any{1},
		},
		{
			name:          "Neq",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Neq("id", 1) },
			wantSQL:       "WHERE id != ?",
			wantSQLServer: "WHERE [id] != ?",
			wantArg:       []any{1},
		},
		{
			name:          "Gt",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Gt("age", 18) },
			wantSQL:       "WHERE age > ?",
			wantSQLServer: "WHERE [age] > ?",
			wantArg:       []any{18},
		},
		{
			name:          "Gte",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Gte("age", 18) },
			wantSQL:       "WHERE age >= ?",
			wantSQLServer: "WHERE [age] >= ?",
			wantArg:       []any{18},
		},
		{
			name:          "Lt",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Lt("age", 65) },
			wantSQL:       "WHERE age < ?",
			wantSQLServer: "WHERE [age] < ?",
			wantArg:       []any{65},
		},
		{
			name:          "Lte",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Lte("age", 65) },
			wantSQL:       "WHERE age <= ?",
			wantSQLServer: "WHERE [age] <= ?",
			wantArg:       []any{65},
		},
		{
			name:          "In",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.In("status", "a", "b") },
			wantSQL:       "WHERE status IN (?, ?)",
			wantSQLServer: "WHERE [status] IN (?, ?)",
			wantArg:       []any{"a", "b"},
		},
		{
			name:          "NotIn",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.NotIn("status", "a") },
			wantSQL:       "WHERE status NOT IN (?)",
			wantSQLServer: "WHERE [status] NOT IN (?)",
			wantArg:       []any{"a"},
		},
		{
			name:          "Between",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Between("age", 18, 65) },
			wantSQL:       "WHERE age BETWEEN ? AND ?",
			wantSQLServer: "WHERE [age] BETWEEN ? AND ?",
			wantArg:       []any{18, 65},
		},
		{
			name:          "NotBetween",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.NotBetween("age", 18, 65) },
			wantSQL:       "WHERE age NOT BETWEEN ? AND ?",
			wantSQLServer: "WHERE [age] NOT BETWEEN ? AND ?",
			wantArg:       []any{18, 65},
		},
		{
			name:          "Like",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Like("name", "%a%") },
			wantSQL:       "WHERE name LIKE ?",
			wantSQLServer: "WHERE [name] LIKE ?",
			wantArg:       []any{"%a%"},
		},
		{
			name:          "NotLike",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.NotLike("name", "%a%") },
			wantSQL:       "WHERE name NOT LIKE ?",
			wantSQLServer: "WHERE [name] NOT LIKE ?",
			wantArg:       []any{"%a%"},
		},
		{
			name: "LikeEscape",
			build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return b.LikeEscape("name", "100!%", "!")
			},
			wantSQL:       "WHERE name LIKE ? ESCAPE '!'",
			wantSQLServer: "WHERE [name] LIKE ? ESCAPE '!'",
			wantArg:       []any{"100!%"},
		},
		{
			name: "NotLikeEscape",
			build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return b.NotLikeEscape("name", "100!%", "!")
			},
			wantSQL:       "WHERE name NOT LIKE ? ESCAPE '!'",
			wantSQLServer: "WHERE [name] NOT LIKE ? ESCAPE '!'",
			wantArg:       []any{"100!%"},
		},
		{
			name:          "IsNull",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.IsNull("deleted_at") },
			wantSQL:       "WHERE deleted_at IS NULL",
			wantSQLServer: "WHERE [deleted_at] IS NULL",
			wantArg:       nil,
		},
		{
			name:          "IsNotNull",
			build:         func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.IsNotNull("deleted_at") },
			wantSQL:       "WHERE deleted_at IS NOT NULL",
			wantSQLServer: "WHERE [deleted_at] IS NOT NULL",
			wantArg:       nil,
		},
	}

	dialects := map[string]dbCore.SQLDialect{
		"postgres":  &postgres.PostgresDialect{},
		"mysql":     &mysql.MySQLDialect{},
		"sqlserver": &sqlserver.SQLServerDialect{},
	}

	for dialectName, dialect := range dialects {
		for _, tt := range tests {
			t.Run(dialectName+"/"+tt.name, func(t *testing.T) {
				sql, args, err := tt.build(
					builder.New(dialect).Select("id").From("users"),
				).ToSQL()

				require.NoError(t, err)
				want := "SELECT id FROM users " + tt.wantSQL
				if dialectName == "sqlserver" {
					want = "SELECT [id] FROM [users] " + tt.wantSQLServer
				}
				assert.Equal(t, want, sql)
				if tt.wantArg == nil {
					assert.Empty(t, args)
				} else {
					assert.Equal(t, tt.wantArg, args)
				}
			})
		}
	}
}

func TestSubqueryConditionHelpers(t *testing.T) {
	dialects := map[string]dbCore.SQLDialect{
		"postgres":  &postgres.PostgresDialect{},
		"mysql":     &mysql.MySQLDialect{},
		"sqlserver": &sqlserver.SQLServerDialect{},
	}

	// On SQL Server the subquery renders through the dialect too, so it is bracketed as well.
	want := func(dialect, predicate, sqlserverPredicate string) string {
		if dialect == "sqlserver" {
			return "SELECT [id] FROM [users] WHERE " + sqlserverPredicate + " (SELECT [user_id] FROM [orders])"
		}
		return "SELECT id FROM users WHERE " + predicate + " (SELECT user_id FROM orders)"
	}

	for name, dialect := range dialects {
		t.Run(name+"/InSubquery", func(t *testing.T) {
			sql, _, err := builder.New(dialect).Select("id").From("users").
				InSubquery("id", subquery(dialect)).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, want(name, "id IN", "[id] IN"), sql)
		})

		t.Run(name+"/NotInSubquery", func(t *testing.T) {
			sql, _, err := builder.New(dialect).Select("id").From("users").
				NotInSubquery("id", subquery(dialect)).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, want(name, "id NOT IN", "[id] NOT IN"), sql)
		})

		t.Run(name+"/Exists", func(t *testing.T) {
			sql, _, err := builder.New(dialect).Select("id").From("users").
				Exists(subquery(dialect)).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, want(name, "EXISTS", "EXISTS"), sql)
		})

		t.Run(name+"/NotExists", func(t *testing.T) {
			sql, _, err := builder.New(dialect).Select("id").From("users").
				NotExists(subquery(dialect)).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, want(name, "NOT EXISTS", "NOT EXISTS"), sql)
		})
	}
}

// ---------------------------------------------------------------- join helpers

func TestJoinHelpers(t *testing.T) {
	on := &dbCore.RawCondition{SQL: "users.id = orders.user_id"}

	// sqlserver is what SQL Server renders after "SELECT [id] FROM [users] ", or "" where it
	// refuses the join: T-SQL has no NATURAL JOIN.
	tests := []struct {
		name      string
		build     func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder
		want      string
		sqlserver string
	}{
		{
			name:      "InnerJoin",
			build:     func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.InnerJoin("orders", on) },
			want:      "INNER JOIN orders ON users.id = orders.user_id",
			sqlserver: "INNER JOIN [orders] ON users.id = orders.user_id",
		},
		{
			name:      "LeftJoin",
			build:     func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.LeftJoin("orders", on) },
			want:      "LEFT JOIN orders ON users.id = orders.user_id",
			sqlserver: "LEFT JOIN [orders] ON users.id = orders.user_id",
		},
		{
			name:      "RightJoin",
			build:     func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.RightJoin("orders", on) },
			want:      "RIGHT JOIN orders ON users.id = orders.user_id",
			sqlserver: "RIGHT JOIN [orders] ON users.id = orders.user_id",
		},
		{
			name:      "CrossJoin",
			build:     func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.CrossJoin("orders") },
			want:      "CROSS JOIN orders",
			sqlserver: "CROSS JOIN [orders]",
		},
		{
			name:  "NaturalJoin",
			build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.NaturalJoin("orders") },
			want:  "NATURAL JOIN orders",
		},
	}

	for _, tt := range tests {
		t.Run("postgres/"+tt.name, func(t *testing.T) {
			sql, _, err := tt.build(pg().Select("id").From("users")).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, "SELECT id FROM users "+tt.want, sql)
		})
		t.Run("mysql/"+tt.name, func(t *testing.T) {
			sql, _, err := tt.build(mysqlB().Select("id").From("users")).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, "SELECT id FROM users "+tt.want, sql)
		})
		t.Run("sqlserver/"+tt.name, func(t *testing.T) {
			sql, _, err := tt.build(sqlserverB().Select("id").From("users")).ToSQL()
			if tt.sqlserver == "" {
				require.Error(t, err)
				assert.True(t, dbCore.IsUnsupported(err))
				assert.Empty(t, sql)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "SELECT [id] FROM [users] "+tt.sqlserver, sql)
		})
	}
}

// TestFullJoinDivergesByDialect is the clearest example of the seam doing its job:
// identical builder calls, supported on one engine and explicitly refused on the
// other.
func TestFullJoinDivergesByDialect(t *testing.T) {
	on := &dbCore.RawCondition{SQL: "users.id = orders.user_id"}

	sql, _, err := pg().Select("id").From("users").FullJoin("orders", on).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users FULL JOIN orders ON users.id = orders.user_id", sql)

	sql, _, err = mysqlB().Select("id").From("users").FullJoin("orders", on).ToSQL()
	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))
	assert.Empty(t, sql)

	sql, _, err = sqlserverB().Select("id").From("users").FullJoin("orders", on).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT [id] FROM [users] FULL JOIN [orders] ON users.id = orders.user_id", sql)
}

func TestLateralJoin(t *testing.T) {
	sql, _, err := pg().Select("u.id").From("users u").
		LateralJoin(subquery(&postgres.PostgresDialect{}), "o", &dbCore.RawCondition{SQL: "true"}).
		ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT u.id FROM users u LATERAL JOIN (SELECT user_id FROM orders) AS o ON true", sql)

	// T-SQL spells LATERAL as APPLY, which takes no ON; the trivial ON true goes with it.
	sql, _, err = sqlserverB().Select("u.id").From("users u").
		LateralJoin(subquery(&sqlserver.SQLServerDialect{}), "o", &dbCore.RawCondition{SQL: "true"}).
		ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT [u].[id] FROM [users] AS [u] CROSS APPLY (SELECT [user_id] FROM [orders]) AS [o]", sql)
}

// ------------------------------------------------------- grouping-set helpers

func TestGroupingSetHelpersDivergeByDialect(t *testing.T) {
	t.Run("postgres/GroupingSets", func(t *testing.T) {
		sql, _, err := pg().Select("a", "b").From("t").GroupingSets([]string{"a"}, []string{"a", "b"}).ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT a, b FROM t GROUP BY GROUPING SETS ((a), (a, b))", sql)
	})

	t.Run("postgres/Cube", func(t *testing.T) {
		sql, _, err := pg().Select("a", "b").From("t").Cube("a", "b").ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT a, b FROM t GROUP BY CUBE (a, b)", sql)
	})

	t.Run("postgres/Rollup", func(t *testing.T) {
		sql, _, err := pg().Select("a").From("t").Rollup("a", "b").ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT a FROM t GROUP BY ROLLUP (a, b)", sql)
	})

	t.Run("mysql/Rollup uses WITH ROLLUP", func(t *testing.T) {
		sql, _, err := mysqlB().Select("a").From("t").Rollup("a", "b").ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT a FROM t GROUP BY a, b WITH ROLLUP", sql)
	})

	t.Run("mysql/Cube refused", func(t *testing.T) {
		_, _, err := mysqlB().Select("a").From("t").Cube("a").ToSQL()
		require.Error(t, err)
		assert.True(t, dbCore.IsUnsupported(err))
	})

	t.Run("mysql/GroupingSets refused", func(t *testing.T) {
		_, _, err := mysqlB().Select("a").From("t").GroupingSets([]string{"a"}).ToSQL()
		require.Error(t, err)
		assert.True(t, dbCore.IsUnsupported(err))
	})

	// SQL Server has the ISO forms Postgres has.
	t.Run("sqlserver/GroupingSets", func(t *testing.T) {
		sql, _, err := sqlserverB().Select("a", "b").From("t").GroupingSets([]string{"a"}, []string{"a", "b"}).ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT [a], [b] FROM [t] GROUP BY GROUPING SETS (([a]), ([a], [b]))", sql)
	})

	t.Run("sqlserver/Cube", func(t *testing.T) {
		sql, _, err := sqlserverB().Select("a", "b").From("t").Cube("a", "b").ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT [a], [b] FROM [t] GROUP BY CUBE ([a], [b])", sql)
	})

	t.Run("sqlserver/Rollup", func(t *testing.T) {
		sql, _, err := sqlserverB().Select("a").From("t").Rollup("a", "b").ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "SELECT [a] FROM [t] GROUP BY ROLLUP ([a], [b])", sql)
	})
}

// ------------------------------------------------------------ DISTINCT / window

func TestDistinctOnDivergesByDialect(t *testing.T) {
	q := pg().Select("id", "user_id").From("orders").DistinctOn("user_id").Build()
	q.Select.Distinct = true

	sql, _, err := (&postgres.PostgresDialect{}).FormatQuery(q)
	require.NoError(t, err)
	assert.Equal(t, "SELECT DISTINCT ON (user_id) id, user_id FROM orders", sql)

	sql, _, err = (&mysql.MySQLDialect{}).FormatQuery(q)
	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))
	assert.Empty(t, sql)

	sql, _, err = (&sqlserver.SQLServerDialect{}).FormatQuery(q)
	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))
	assert.Empty(t, sql)
}

func TestWindowAndOver(t *testing.T) {
	definition := &dbCore.WindowDefinition{
		PartitionBy: []string{"user_id"},
		OrderBy:     []dbCore.OrderByField{{Field: "created_at", Direction: "DESC"}},
	}

	b := pg().Window("w", definition)
	over := b.Over("w")
	assert.Contains(t, over, "w OVER (")
	assert.Contains(t, over, "PARTITION BY user_id")

	// An unknown window name yields an empty definition rather than panicking.
	assert.Equal(t, "missing OVER ()", b.Over("missing"))

	sql, _, err := (&postgres.PostgresDialect{}).FormatWindow("w", definition)
	require.NoError(t, err)
	assert.Contains(t, sql, "w AS (")

	sql, _, err = (&sqlserver.SQLServerDialect{}).FormatWindow("w", definition)
	require.NoError(t, err)
	assert.Equal(t, "[w] AS (PARTITION BY [user_id] ORDER BY [created_at] DESC)", sql)
}

// ------------------------------------------------------------------- Has* / misc

func TestHasPredicates(t *testing.T) {
	empty := pg()
	assert.False(t, empty.HasFrom())
	assert.False(t, empty.HasWhere())
	assert.False(t, empty.HasJoin())
	assert.False(t, empty.HasOrderBy())
	assert.False(t, empty.HasGroupBy())
	assert.False(t, empty.HasLimit())
	assert.False(t, empty.HasOffset())

	full := pg().
		From("users").
		Eq("id", 1).
		InnerJoin("orders", &dbCore.RawCondition{SQL: "true"}).
		OrderBy("id", "asc").
		GroupBy("id").
		Limit(1).
		Offset(2).(*builder.Builder)

	assert.True(t, full.HasFrom())
	assert.True(t, full.HasWhere())
	assert.True(t, full.HasJoin())
	assert.True(t, full.HasOrderBy())
	assert.True(t, full.HasGroupBy())
	assert.True(t, full.HasLimit())
	assert.True(t, full.HasOffset())

	// HasGroupBy is also true for each grouping-set variant on its own.
	assert.True(t, pg().Rollup("a").(*builder.Builder).HasGroupBy())
	assert.True(t, pg().Cube("a").(*builder.Builder).HasGroupBy())
	assert.True(t, pg().GroupingSets([]string{"a"}).(*builder.Builder).HasGroupBy())
}

func TestUnionAndUnionAll(t *testing.T) {
	other := pg().Select("id").From("archived_users").Build()

	sql, _, err := pg().Select("id").From("users").Union(other).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users UNION SELECT id FROM archived_users", sql)

	sql, _, err = pg().Select("id").From("users").UnionAll(other).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM users UNION ALL SELECT id FROM archived_users", sql)

	sql, _, err = sqlserverB().Select("id").From("users").Union(other).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT [id] FROM [users] UNION SELECT [id] FROM [archived_users]", sql)

	sql, _, err = sqlserverB().Select("id").From("users").UnionAll(other).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT [id] FROM [users] UNION ALL SELECT [id] FROM [archived_users]", sql)
}

// TestSubqueryInFrom is a regression test: formatSelect used to pass only
// From.Table and From.Alias to FormatFrom and never look at From.IsSubquery, so a
// query built with Builder.Subquery() rendered "FROM  AS o" — the subquery
// silently dropped, leaving invalid SQL.
func TestSubqueryInFrom(t *testing.T) {
	sql, _, err := pg().Select("o.user_id").Subquery(subquery(&postgres.PostgresDialect{}), "o").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT o.user_id FROM (SELECT user_id FROM orders) AS o", sql)

	sql, _, err = mysqlB().Select("o.user_id").Subquery(subquery(&mysql.MySQLDialect{}), "o").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT o.user_id FROM (SELECT user_id FROM orders) AS o", sql)

	sql, _, err = sqlserverB().Select("o.user_id").Subquery(subquery(&sqlserver.SQLServerDialect{}), "o").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT [o].[user_id] FROM (SELECT [user_id] FROM [orders]) AS [o]", sql)
}

func TestSubqueryInFromWithoutAlias(t *testing.T) {
	// Postgres tolerates an unaliased derived table in this position.
	sql, _, err := pg().Select("user_id").Subquery(subquery(&postgres.PostgresDialect{}), "").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT user_id FROM (SELECT user_id FROM orders)", sql)

	// MySQL and SQL Server require the alias, so they say so rather than emitting a syntax error.
	_, _, err = mysqlB().Select("user_id").Subquery(subquery(&mysql.MySQLDialect{}), "").ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alias")

	_, _, err = sqlserverB().Select("user_id").Subquery(subquery(&sqlserver.SQLServerDialect{}), "").ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alias")
}

func TestSubqueryInFromWithoutQueryIsAnError(t *testing.T) {
	for name, dialect := range map[string]dbCore.SQLDialect{
		"postgres":  &postgres.PostgresDialect{},
		"mysql":     &mysql.MySQLDialect{},
		"sqlserver": &sqlserver.SQLServerDialect{},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := dialect.FormatQuery(&dbCore.Query{
				Select: &dbCore.SelectClause{Fields: []string{"id"}},
				From:   &dbCore.FromClause{IsSubquery: true, Alias: "o"},
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "subquery")
		})
	}
}

func TestInsertBuilderGuardsWithoutInsertClause(t *testing.T) {
	// Columns/Values/FromSelect/OnConflict/DoNothing/DoUpdate are no-ops when no
	// Insert clause has been started, rather than nil-dereferencing.
	base := pg()

	for name, build := range map[string]func() dbCore.IQueryBuilder{
		"Columns":    func() dbCore.IQueryBuilder { return base.Columns("a") },
		"Values":     func() dbCore.IQueryBuilder { return base.Values(1) },
		"FromSelect": func() dbCore.IQueryBuilder { return base.FromSelect(subquery(&postgres.PostgresDialect{})) },
		"OnConflict": func() dbCore.IQueryBuilder { return base.OnConflict("a") },
		"DoNothing":  func() dbCore.IQueryBuilder { return base.DoNothing() },
		"DoUpdate":   func() dbCore.IQueryBuilder { return base.DoUpdate(map[string]interface{}{"a": 1}) },
	} {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				assert.Nil(t, build().Build().Insert)
			})
		})
	}
}

func TestUpdateBuilderGuardsWithoutUpdateClause(t *testing.T) {
	base := pg()

	require.NotPanics(t, func() {
		assert.Nil(t, base.Set("a", 1).Build().Update)
		assert.Nil(t, base.SetMap(map[string]interface{}{"a": 1}).Build().Update)
	})
}

func TestCloneDeepCopiesEveryClause(t *testing.T) {
	original := pg().
		Select("id").
		From("users").
		Eq("id", 1).
		InnerJoin("orders", &dbCore.RawCondition{SQL: "true"}).
		OrderBy("id", "asc").
		GroupBy("id").
		Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 1}).
		Limit(1).
		Offset(2).
		WithCTE("c", subquery(&postgres.PostgresDialect{})).
		Union(subquery(&postgres.PostgresDialect{})).
		Window("w", &dbCore.WindowDefinition{
			PartitionBy: []string{"user_id"},
			OrderBy:     []dbCore.OrderByField{{Field: "id"}},
			Frame: &dbCore.WindowFrame{
				Type:  "ROWS",
				Start: &dbCore.FrameBound{Type: "PRECEDING", Value: 1},
				End:   &dbCore.FrameBound{Type: "FOLLOWING", Value: 1},
			},
		}).
		Returning("id").(*builder.Builder)

	clone := original.Clone()

	// Mutating the clone's query must not reach the original.
	clone.Build().Select.Fields[0] = "mutated"
	assert.Equal(t, "mutated", clone.Build().Select.Fields[0])
	assert.Equal(t, "id", original.Build().Select.Fields[0])

	// Every clause survived the copy.
	cq := clone.Build()
	assert.NotNil(t, cq.From)
	assert.NotNil(t, cq.Where)
	assert.Len(t, cq.Joins, 1)
	assert.NotNil(t, cq.OrderBy)
	assert.NotNil(t, cq.GroupBy)
	assert.NotNil(t, cq.Having)
	assert.NotNil(t, cq.Limit)
	assert.NotNil(t, cq.Offset)
	assert.Len(t, cq.CTEs, 1)
	assert.Len(t, cq.Unions, 1)
	assert.Len(t, cq.Windows, 1)
	assert.NotNil(t, cq.Windows[0].Definition.Frame)
	assert.Equal(t, []string{"id"}, cq.Returning)
}

// TestCloneKeepsOrderByRaw: every clause method clones, so a clone that dropped
// OrderByField.Raw turned OrderByRaw(expr, dir).Limit(n) into an ORDER BY that bound or quoted
// expr instead of emitting it. TestOrderByRawIsEmittedVerbatim calls OrderByRaw last, which is
// why it never saw this.
func TestCloneKeepsOrderByRaw(t *testing.T) {
	const expression = "first_name || ' ' || last_name"

	for name, b := range map[string]*builder.Builder{"postgres": pg(), "mysql": mysqlB(), "sqlserver": sqlserverB()} {
		t.Run(name, func(t *testing.T) {
			sql, args, err := b.Select("id").From("users").
				OrderByRaw(expression, "desc").
				Limit(5).
				ToSQL()
			require.NoError(t, err)
			assert.Contains(t, sql, "ORDER BY "+expression+" DESC")
			assert.Empty(t, args, "a raw expression is emitted, not bound")
		})
	}

	// The flag itself, on both ORDER BY lists a clone copies.
	original := pg().Select("id").From("users").
		OrderByRaw(expression, "desc").
		OrderBy("id", "asc").
		Window("w", &dbCore.WindowDefinition{OrderBy: []dbCore.OrderByField{
			{Field: "LENGTH(name)", Direction: "DESC", Raw: true},
			{Field: "id", Direction: "ASC"},
		}}).(*builder.Builder)
	cq := original.Clone().Build()
	require.Len(t, cq.OrderBy.Fields, 2)
	assert.True(t, cq.OrderBy.Fields[0].Raw)
	assert.False(t, cq.OrderBy.Fields[1].Raw)
	require.Len(t, cq.Windows, 1)
	require.Len(t, cq.Windows[0].Definition.OrderBy, 2)
	assert.True(t, cq.Windows[0].Definition.OrderBy[0].Raw)
	assert.False(t, cq.Windows[0].Definition.OrderBy[1].Raw)

	// Both lists are copied whole, into slices of the clone's own: writing to the clone
	// leaves the original alone.
	cq.OrderBy.Fields[0].Field = "mutated"
	cq.Windows[0].Definition.OrderBy[0].Field = "mutated"
	oq := original.Build()
	assert.Equal(t, expression, oq.OrderBy.Fields[0].Field)
	assert.Equal(t, "LENGTH(name)", oq.Windows[0].Definition.OrderBy[0].Field)

	// And inside a subquery that an outer builder copies on every clause call.
	inner := pg().Select("user_id").From("orders").OrderByRaw("SUM(total)", "desc").Limit(1).Build()
	outer := pg().Subquery(inner, "top").Select("user_id").(*builder.Builder)
	assert.True(t, outer.Build().From.Subquery.OrderBy.Fields[0].Raw)
	sql, args, err := outer.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "ORDER BY SUM(total) DESC")
	assert.Empty(t, args)
}

func TestCloneCopiesInsertUpdateAndDelete(t *testing.T) {
	insert := pg().Insert("users").Columns("a").Values(1).
		OnConflict("a").DoUpdate(map[string]interface{}{"a": 2}).(*builder.Builder)
	assert.Equal(t, "users", insert.Clone().Build().Insert.Table)
	assert.Equal(t, "DO UPDATE", insert.Clone().Build().Insert.OnConflict.Action)

	insertFrom := pg().Insert("users").FromSelect(subquery(&postgres.PostgresDialect{})).(*builder.Builder)
	assert.NotNil(t, insertFrom.Clone().Build().Insert.FromQuery)

	update := pg().Update("users").Set("a", 1).(*builder.Builder)
	assert.Equal(t, "users", update.Clone().Build().Update.Table)

	del := pg().Delete("users").(*builder.Builder)
	assert.Equal(t, "users", del.Clone().Build().Delete.Table)

	fromSubquery := pg().Subquery(subquery(&postgres.PostgresDialect{}), "o").(*builder.Builder)
	assert.NotNil(t, fromSubquery.Clone().Build().From.Subquery)
}

func TestOrderByRawIsEmittedVerbatim(t *testing.T) {
	for name, b := range map[string]*builder.Builder{"postgres": pg(), "mysql": mysqlB(), "sqlserver": sqlserverB()} {
		t.Run(name, func(t *testing.T) {
			sql, args, err := b.Select("id").From("users").
				OrderByRaw("first_name || ' ' || last_name", "desc").ToSQL()
			require.NoError(t, err)
			assert.Contains(t, sql, "ORDER BY first_name || ' ' || last_name DESC")
			assert.Empty(t, args)
		})
	}
}

func TestSelectStarWhenNoFieldsGiven(t *testing.T) {
	sql, _, err := pg().From("users").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM users", sql)

	sql, _, err = mysqlB().From("users").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM users", sql)

	sql, _, err = sqlserverB().From("users").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM [users]", sql)
}

func TestPostgresReturningOnEveryStatementKind(t *testing.T) {
	sql, _, err := pg().Insert("users").Columns("a").Values(1).Returning("id").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users (a) VALUES (?) RETURNING id", sql)

	sql, _, err = pg().Update("users").Set("a", 1).Returning("id").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "UPDATE users SET a = ? RETURNING id", sql)

	sql, _, err = pg().Delete("users").Eq("id", 1).Returning("id").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM users WHERE id = ? RETURNING id", sql)
}

func TestPostgresOnConflictDoNothing(t *testing.T) {
	sql, args, err := pg().Insert("users").Columns("id").Values(1).
		OnConflict("id").DoNothing().ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users (id) VALUES (?) ON CONFLICT (id) DO NOTHING", sql)
	assert.Equal(t, []any{1}, args)
}

func TestPostgresInsertFromSelect(t *testing.T) {
	sql, _, err := pg().Insert("users").Columns("user_id").
		FromSelect(subquery(&postgres.PostgresDialect{})).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO users (user_id) SELECT user_id FROM orders", sql)
}

func TestPostgresDeleteWithoutWhere(t *testing.T) {
	sql, args, err := pg().Delete("users").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM users", sql)
	assert.Empty(t, args)

	sql, _, err = mysqlB().Delete("users").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM users", sql)

	sql, _, err = sqlserverB().Delete("users").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM [users]", sql)
}

func TestJoinWithAlias(t *testing.T) {
	for name, tt := range map[string]struct {
		dialect dbCore.SQLDialect
		want    string
	}{
		"postgres":  {&postgres.PostgresDialect{}, "SELECT id FROM users INNER JOIN orders AS o ON o.user_id = users.id"},
		"mysql":     {&mysql.MySQLDialect{}, "SELECT id FROM users INNER JOIN orders AS o ON o.user_id = users.id"},
		"sqlserver": {&sqlserver.SQLServerDialect{}, "SELECT [id] FROM [users] INNER JOIN [orders] AS [o] ON o.user_id = users.id"},
	} {
		t.Run(name, func(t *testing.T) {
			sql, _, err := builder.New(tt.dialect).Select("id").From("users").
				Join(&dbCore.JoinClause{
					Type:      "INNER",
					Table:     "orders",
					Alias:     "o",
					Condition: &dbCore.RawCondition{SQL: "o.user_id = users.id"},
				}).ToSQL()
			require.NoError(t, err)
			assert.Equal(t, tt.want, sql)
		})
	}
}

func TestFromWithAlias(t *testing.T) {
	for name, tt := range map[string]struct {
		dialect dbCore.SQLDialect
		want    string
	}{
		"postgres":  {&postgres.PostgresDialect{}, "FROM users AS u"},
		"mysql":     {&mysql.MySQLDialect{}, "FROM users AS u"},
		"sqlserver": {&sqlserver.SQLServerDialect{}, "FROM [users] AS [u]"},
	} {
		t.Run(name, func(t *testing.T) {
			sql, _, err := dialectFromWithAlias(tt.dialect)
			require.NoError(t, err)
			assert.Equal(t, tt.want, sql)
		})
	}
}

func dialectFromWithAlias(d dbCore.SQLDialect) (string, []any, error) {
	return d.FormatFrom("users", "u")
}

func TestJoinWithoutConditionOnEveryDialect(t *testing.T) {
	for name, tt := range map[string]struct {
		dialect dbCore.SQLDialect
		want    string
	}{
		"postgres":  {&postgres.PostgresDialect{}, "CROSS JOIN t"},
		"mysql":     {&mysql.MySQLDialect{}, "CROSS JOIN t"},
		"sqlserver": {&sqlserver.SQLServerDialect{}, "CROSS JOIN [t]"},
	} {
		t.Run(name, func(t *testing.T) {
			sql, _, err := tt.dialect.FormatJoin(&dbCore.JoinClause{Type: "CROSS", Table: "t"})
			require.NoError(t, err)
			assert.Equal(t, tt.want, sql)
		})
	}
}

func TestNilJoinIsIgnored(t *testing.T) {
	sql, _, err := (&mysql.MySQLDialect{}).FormatJoin(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)

	sql, _, err = (&sqlserver.SQLServerDialect{}).FormatJoin(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)
}

// TestSupportsReturningPerDialect pins the capability probe the ORM branches on when
// deciding how to read a generated key back. Asking the dialect is deliberate: a
// hand-maintained capability flag would eventually disagree with what
// FormatReturning actually does, and the disagreement would surface as invalid SQL.
func TestSupportsReturningPerDialect(t *testing.T) {
	assert.True(t, dbCore.SupportsReturning(&postgres.PostgresDialect{}),
		"Postgres supports RETURNING")
	assert.False(t, dbCore.SupportsReturning(&mysql.MySQLDialect{}),
		"MySQL has no RETURNING, which is why orm.Create needs a second path")
	assert.True(t, dbCore.SupportsReturning(&sqlserver.SQLServerDialect{}),
		"SQL Server spells RETURNING as OUTPUT INSERTED")
	assert.False(t, dbCore.SupportsReturning(nil))
}

// TestCapabilityProbesPerDialect pins that neither Postgres nor MySQL declares a bind-parameter
// limit or a trigger-sensitive RETURNING, so code that branches on either probe behaves on
// them exactly as it did before the probes existed, and that SQL Server declares both.
func TestCapabilityProbesPerDialect(t *testing.T) {
	for name, d := range map[string]dbCore.SQLDialect{
		"postgres": &postgres.PostgresDialect{},
		"mysql":    &mysql.MySQLDialect{},
		"nil":      nil,
	} {
		assert.Zerof(t, dbCore.BindParameterLimit(d), "%s declares no bind-parameter limit", name)
		assert.Falsef(t, dbCore.ReturningBlockedByTriggers(d), "%s RETURNING is not trigger-sensitive", name)
	}

	assert.Equal(t, 2098, dbCore.BindParameterLimit(&sqlserver.SQLServerDialect{}),
		"2100 per request, less the two sp_executesql takes")
	assert.True(t, dbCore.ReturningBlockedByTriggers(&sqlserver.SQLServerDialect{}),
		"OUTPUT without INTO is refused on a table with a trigger (Msg 334)")
}
