package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refusal must reach the caller from every position it can occur in, never
// getting swallowed into partially rendered SQL. Each case plants an unsupported
// construct in one clause and asserts the whole render fails.

// distinctOnQuery builds a query the MySQL dialect must refuse.
func distinctOnQuery() *dbCore.Query {
	return &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields:     []string{"id"},
			Distinct:   true,
			DistinctOn: []string{"user_id"},
		},
		From: &dbCore.FromClause{Table: "orders"},
	}
}

func TestRefusalPropagatesFromEveryClause(t *testing.T) {
	tests := map[string]*dbCore.Query{
		"CTE": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "users"},
			CTEs:   []*dbCore.CTEClause{{Name: "c", Query: distinctOnQuery()}},
		},
		"FROM subquery": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From: &dbCore.FromClause{
				IsSubquery: true, Alias: "o", Subquery: distinctOnQuery(),
			},
		},
		"JOIN subquery": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "users"},
			Joins: []*dbCore.JoinClause{{
				Type: "INNER", IsSubquery: true, Alias: "o", Subquery: distinctOnQuery(),
			}},
		},
		"UNION": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "users"},
			Unions: []*dbCore.UnionClause{{Query: distinctOnQuery()}},
		},
		"GROUP BY CUBE": {
			Select:  &dbCore.SelectClause{Fields: []string{"id"}},
			From:    &dbCore.FromClause{Table: "users"},
			GroupBy: &dbCore.GroupByClause{Cube: []string{"a"}},
		},
		"SELECT DISTINCT ON": distinctOnQuery(),
		"RETURNING":          {Select: &dbCore.SelectClause{Fields: []string{"id"}}, Returning: []string{"id"}},
		"INSERT RETURNING": {
			Insert:    &dbCore.InsertClause{Table: "t", Columns: []string{"a"}, Values: [][]any{{1}}},
			Returning: []string{"id"},
		},
		"INSERT ... SELECT with DISTINCT ON": {
			Insert: &dbCore.InsertClause{Table: "t", Columns: []string{"id"}, FromQuery: distinctOnQuery()},
		},
		"UPDATE RETURNING": {
			Update:    &dbCore.UpdateClause{Table: "t", Values: map[string]any{"a": 1}},
			Returning: []string{"id"},
		},
		"DELETE RETURNING": {
			Delete:    &dbCore.DeleteClause{Table: "t"},
			Returning: []string{"id"},
		},
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			sql, args, err := d().FormatQuery(query)

			require.Error(t, err)
			assert.True(t, dbCore.IsUnsupported(err), "expected an UnsupportedError, got %v", err)
			assert.Empty(t, sql, "no partially rendered SQL may escape a refusal")
			assert.Nil(t, args)
		})
	}
}

func TestFormatCTEAndUnionPropagateRefusals(t *testing.T) {
	_, _, err := d().FormatCTE("c", distinctOnQuery())
	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))

	_, _, err = d().FormatUnion(distinctOnQuery(), true)
	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))

	_, _, err = d().FormatSubquery(distinctOnQuery(), "o")
	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))

	_, _, err = d().FormatJoin(&dbCore.JoinClause{
		Type: "INNER", IsSubquery: true, Alias: "o", Subquery: distinctOnQuery(),
	})
	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))
}

func TestFormatFromClauseErrors(t *testing.T) {
	_, _, err := d().formatFromClause(&dbCore.FromClause{IsSubquery: true, Alias: "o"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no query")

	_, _, err = d().formatFromClause(&dbCore.FromClause{
		IsSubquery: true,
		Subquery:   &dbCore.Query{Select: &dbCore.SelectClause{Fields: []string{"id"}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alias")

	sql, _, err := d().formatFromClause(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)
}

func TestInsertGuards(t *testing.T) {
	// DO UPDATE with no assignments cannot render valid SQL.
	_, _, err := d().FormatQuery(&dbCore.Query{
		Insert: &dbCore.InsertClause{
			Table:      "t",
			Columns:    []string{"a"},
			Values:     [][]any{{1}},
			OnConflict: &dbCore.OnConflictClause{Action: "DO UPDATE"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one assignment")

	// An unrecognized action.
	_, _, err = d().FormatQuery(&dbCore.Query{
		Insert: &dbCore.InsertClause{
			Table:      "t",
			Columns:    []string{"a"},
			Values:     [][]any{{1}},
			OnConflict: &dbCore.OnConflictClause{Action: "DO SOMETHING"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DO SOMETHING")

	// DO NOTHING with neither insert columns nor a conflict target has no column
	// to build the no-op self-assignment from.
	_, _, err = d().FormatQuery(&dbCore.Query{
		Insert: &dbCore.InsertClause{
			Table:      "t",
			Values:     [][]any{{1}},
			OnConflict: &dbCore.OnConflictClause{Action: "DO NOTHING"},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-op assignment")

	// DO NOTHING falls back to the conflict target when there are no columns.
	sql, _, err := d().FormatQuery(&dbCore.Query{
		Insert: &dbCore.InsertClause{
			Table:  "t",
			Values: [][]any{{1}},
			OnConflict: &dbCore.OnConflictClause{
				Action:  "DO NOTHING",
				Columns: []string{"id"},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO t VALUES (?) ON DUPLICATE KEY UPDATE `id` = `id`", sql)
}

func TestInsertWithoutColumnsOrConflict(t *testing.T) {
	sql, args, err := d().FormatQuery(&dbCore.Query{
		Insert: &dbCore.InsertClause{Table: "t", Values: [][]any{{1, 2}}},
	})
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO t VALUES (?, ?)", sql)
	assert.Equal(t, []any{1, 2}, args)
}

func TestInsertMultipleRows(t *testing.T) {
	sql, args, err := NewBuilder().Insert("t").Columns("a").Values(1).Values(2).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "INSERT INTO t (a) VALUES (?), (?)", sql)
	assert.Equal(t, []any{1, 2}, args)
}

func TestUpdateAndDeleteRewriteILIKEInWhere(t *testing.T) {
	sql, _, err := NewBuilder().Update("users").Set("a", 1).
		Where(&dbCore.RawCondition{SQL: "name ILIKE 'a%'"}).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "UPDATE users SET a = ? WHERE name LIKE 'a%'", sql)

	sql, _, err = NewBuilder().Delete("users").
		Where(&dbCore.RawCondition{SQL: "name ILIKE 'a%'"}).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "DELETE FROM users WHERE name LIKE 'a%'", sql)
}

func TestSelectStarAndEmptyQuery(t *testing.T) {
	sql, _, err := d().FormatQuery(&dbCore.Query{})
	require.NoError(t, err)
	assert.Equal(t, "SELECT *", sql)
}

func TestWhereWithOnlyEmptyConditionsEmitsNothing(t *testing.T) {
	sql, _, err := d().FormatWhere(&dbCore.WhereClause{
		Operator:   "AND",
		Conditions: []dbCore.Condition{&dbCore.RawCondition{SQL: ""}},
	})
	require.NoError(t, err)
	assert.Empty(t, sql, "a WHERE whose every condition renders empty must be omitted")
}

func TestUnsupportedErrorFormatting(t *testing.T) {
	err := dbCore.Unsupported("mysql", "RETURNING", "use LAST_INSERT_ID()")
	assert.Equal(t, "mysql does not support RETURNING; use LAST_INSERT_ID()", err.Error())

	bare := dbCore.Unsupported("mysql", "CUBE", "")
	assert.Equal(t, "mysql does not support CUBE", bare.Error())

	assert.True(t, dbCore.IsUnsupported(err))
	assert.False(t, dbCore.IsUnsupported(nil))
	assert.False(t, dbCore.IsUnsupported(assert.AnError))
}
