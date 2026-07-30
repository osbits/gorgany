package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The Postgres dialect refuses nothing on grounds of engine capability — Postgres
// is the superset the builder was written against. Its error branches exist for
// the one structural failure it can hit: a FROM marked as a subquery that carries
// no query. These tests drive that failure through every clause position, proving
// no branch swallows it into partially rendered SQL.

// malformedQuery is a query whose FROM claims to be a subquery but has none.
func malformedQuery() *dbCore.Query {
	return &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"id"}},
		From:   &dbCore.FromClause{IsSubquery: true, Alias: "o"},
	}
}

func TestPostgresStructuralErrorPropagatesFromEveryClause(t *testing.T) {
	tests := map[string]*dbCore.Query{
		"CTE": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "users"},
			CTEs:   []*dbCore.CTEClause{{Name: "c", Query: malformedQuery()}},
		},
		"FROM subquery": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From: &dbCore.FromClause{
				IsSubquery: true, Alias: "o", Subquery: malformedQuery(),
			},
		},
		"JOIN subquery": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "users"},
			Joins: []*dbCore.JoinClause{{
				Type: "INNER", IsSubquery: true, Alias: "o", Subquery: malformedQuery(),
			}},
		},
		"JOIN subquery with condition": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "users"},
			Joins: []*dbCore.JoinClause{{
				Type: "INNER", IsSubquery: true, Alias: "o",
				Subquery:  malformedQuery(),
				Condition: &dbCore.RawCondition{SQL: "true"},
			}},
		},
		"UNION": {
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "users"},
			Unions: []*dbCore.UnionClause{{Query: malformedQuery()}},
		},
		"INSERT ... SELECT": {
			Insert: &dbCore.InsertClause{
				Table: "t", Columns: []string{"id"}, FromQuery: malformedQuery(),
			},
		},
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			sql, args, err := pgd().FormatQuery(query)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "subquery")
			assert.Empty(t, sql, "no partially rendered SQL may escape")
			assert.Nil(t, args)
		})
	}
}

func TestPostgresFormatHelpersPropagateStructuralErrors(t *testing.T) {
	_, _, err := pgd().FormatCTE("c", malformedQuery())
	require.Error(t, err)

	_, _, err = pgd().FormatUnion(malformedQuery(), false)
	require.Error(t, err)

	_, _, err = pgd().FormatUnion(malformedQuery(), true)
	require.Error(t, err)

	_, _, err = pgd().FormatSubquery(malformedQuery(), "o")
	require.Error(t, err)

	_, _, err = pgd().FormatJoin(&dbCore.JoinClause{
		Type: "INNER", IsSubquery: true, Alias: "o", Subquery: malformedQuery(),
	})
	require.Error(t, err)
}

func TestPostgresFormatFromClauseEdges(t *testing.T) {
	sql, _, err := pgd().formatFromClause(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)

	_, _, err = pgd().formatFromClause(&dbCore.FromClause{IsSubquery: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no query")

	// An unaliased derived table is legal on Postgres.
	sql, _, err = pgd().formatFromClause(&dbCore.FromClause{
		IsSubquery: true,
		Subquery: &dbCore.Query{
			Select: &dbCore.SelectClause{Fields: []string{"id"}},
			From:   &dbCore.FromClause{Table: "t"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "FROM (SELECT id FROM t)", sql)
}

// TestPostgresRETURNINGErrorBranchesAreWired proves that the RETURNING branches on
// INSERT/UPDATE/DELETE go through FormatReturning rather than string-concatenating
// it. That indirection is what makes the MySQL refusal reach the caller, and this
// pins that Postgres still emits the clause.
func TestPostgresRETURNINGGoesThroughFormatReturning(t *testing.T) {
	for name, build := range map[string]func() (string, []any, error){
		"INSERT": func() (string, []any, error) {
			return NewBuilder().Insert("t").Columns("a").Values(1).Returning("id", "name").ToSQL()
		},
		"UPDATE": func() (string, []any, error) {
			return NewBuilder().Update("t").Set("a", 1).Returning("id", "name").ToSQL()
		},
		"DELETE": func() (string, []any, error) {
			return NewBuilder().Delete("t").Returning("id", "name").ToSQL()
		},
	} {
		t.Run(name, func(t *testing.T) {
			sql, _, err := build()
			require.NoError(t, err)
			assert.Contains(t, sql, "RETURNING id, name")
		})
	}
}
