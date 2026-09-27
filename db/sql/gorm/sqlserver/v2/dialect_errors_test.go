package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refusal must reach the caller from every position it can occur in, never getting
// swallowed into partially rendered SQL. Each case plants an unsupported construct in one
// clause and asserts the whole render fails.

// distinctOnQuery builds a query the dialect must refuse.
func distinctOnQuery() *dbCore.Query {
	return &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"Id"}, Distinct: true, DistinctOn: []string{"CustomerId"}},
		From:   &dbCore.FromClause{Table: "orders"},
	}
}

func TestRefusalPropagatesFromEveryClause(t *testing.T) {
	selectID := func() *dbCore.SelectClause { return &dbCore.SelectClause{Fields: []string{"Id"}} }
	users := func() *dbCore.FromClause { return &dbCore.FromClause{Table: "users"} }
	regexCondition := &dbCore.BinaryCondition{Left: "Name", Operator: "~", Right: "^a"}

	tests := map[string]*dbCore.Query{
		"CTE":           {Select: selectID(), From: users(), CTEs: []*dbCore.CTEClause{{Name: "c", Query: distinctOnQuery()}}},
		"FROM subquery": {Select: selectID(), From: &dbCore.FromClause{IsSubquery: true, Alias: "o", Subquery: distinctOnQuery()}},
		"JOIN subquery": {Select: selectID(), From: users(), Joins: []*dbCore.JoinClause{{
			Type: "INNER", IsSubquery: true, Alias: "o", Subquery: distinctOnQuery(), Condition: &dbCore.RawCondition{SQL: "1=1"},
		}}},
		"APPLY subquery": {Select: selectID(), From: users(), Joins: []*dbCore.JoinClause{{
			Type: "LATERAL", IsLateral: true, IsSubquery: true, Alias: "o", Subquery: distinctOnQuery(),
		}}},
		"JOIN ON":            {Select: selectID(), From: users(), Joins: []*dbCore.JoinClause{{Type: "INNER", Table: "o", Condition: regexCondition}}},
		"UNION":              {Select: selectID(), From: users(), Unions: []*dbCore.UnionClause{{Query: distinctOnQuery()}}},
		"WHERE":              {Select: selectID(), From: users(), Where: &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{regexCondition}}},
		"WHERE subquery":     {Select: selectID(), From: users(), Where: &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{&dbCore.ExistsCondition{Query: distinctOnQuery()}}}},
		"HAVING":             {Select: selectID(), From: users(), Having: &dbCore.HavingClause{Condition: regexCondition}},
		"ORDER BY":           {Select: selectID(), From: users(), OrderBy: &dbCore.OrderByClause{Fields: []dbCore.OrderByField{{Field: "LEN(Name)"}}}},
		"SELECT FILTER":      {Select: &dbCore.SelectClause{Fields: []string{"COUNT(*) FILTER (WHERE a = 1)"}}, From: users()},
		"SELECT DISTINCT ON": distinctOnQuery(),
		"RETURNING":          {Select: selectID(), Returning: []string{"Id"}},
		"INSERT RETURNING":   {Insert: &dbCore.InsertClause{Table: "t", Columns: []string{"a"}, Values: [][]any{{1}}}, Returning: []string{"a + 1"}},
		"INSERT ... SELECT with DISTINCT ON": {
			Insert: &dbCore.InsertClause{Table: "t", Columns: []string{"Id"}, FromQuery: distinctOnQuery()},
		},
		"INSERT LIMIT":     {Insert: &dbCore.InsertClause{Table: "t", Columns: []string{"a"}, Values: [][]any{{1}}}, Limit: intp(1)},
		"UPSERT source":    {Insert: &dbCore.InsertClause{Table: "t", Columns: []string{"Id"}, FromQuery: distinctOnQuery(), OnConflict: &dbCore.OnConflictClause{Columns: []string{"Id"}, Action: "DO NOTHING"}}},
		"UPSERT target":    {Insert: &dbCore.InsertClause{Table: "t", Columns: []string{"a"}, Values: [][]any{{1}}, OnConflict: &dbCore.OnConflictClause{Action: "DO NOTHING"}}},
		"UPDATE RETURNING": {Update: &dbCore.UpdateClause{Table: "t", Values: map[string]any{"a": 1}}, Returning: []string{"COUNT(*)"}},
		"UPDATE WHERE":     {Update: &dbCore.UpdateClause{Table: "t", Values: map[string]any{"a": 1}}, Where: &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{regexCondition}}},
		"UPDATE ORDER BY":  {Update: &dbCore.UpdateClause{Table: "t", Values: map[string]any{"a": 1}}, OrderBy: &dbCore.OrderByClause{Fields: []dbCore.OrderByField{{Field: "a"}}}},
		"UPDATE CTE":       {Update: &dbCore.UpdateClause{Table: "t", Values: map[string]any{"a": 1}}, CTEs: []*dbCore.CTEClause{{Name: "c", Query: distinctOnQuery()}}},
		"DELETE RETURNING": {Delete: &dbCore.DeleteClause{Table: "t"}, Returning: []string{"INSERTED.Id"}},
		"DELETE WHERE":     {Delete: &dbCore.DeleteClause{Table: "t"}, Where: &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{regexCondition}}},
		"DELETE JOIN":      {Delete: &dbCore.DeleteClause{Table: "t"}, Joins: []*dbCore.JoinClause{{Type: "INNER", Table: "u", Condition: &dbCore.RawCondition{SQL: "1=1"}}}},
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			sql, args, err := d().FormatQuery(query)

			require.Error(t, err)
			assert.True(t, dbCore.IsUnsupported(errUnwrapAll(err)), "expected an UnsupportedError, got %v", err)
			assert.Empty(t, sql, "no partially rendered SQL may escape a refusal")
			assert.Nil(t, args)
		})
	}
}

// errUnwrapAll returns the innermost error: the condition context's refusals reach the caller
// wrapped in "cannot render WHERE: …", and dbCore.IsUnsupported does not unwrap.
func errUnwrapAll(err error) error {
	for {
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok || unwrapper.Unwrap() == nil {
			return err
		}
		err = unwrapper.Unwrap()
	}
}

func TestFormatCTEUnionSubqueryAndJoinPropagateRefusals(t *testing.T) {
	_, _, err := d().FormatCTE("c", distinctOnQuery())
	requireUnsupported(t, err, "DISTINCT ON")

	_, _, err = d().FormatUnion(distinctOnQuery(), true)
	requireUnsupported(t, err, "DISTINCT ON")

	_, _, err = d().FormatSubquery(distinctOnQuery(), "o")
	requireUnsupported(t, err, "DISTINCT ON")

	_, _, err = d().FormatJoin(&dbCore.JoinClause{
		Type: "INNER", IsSubquery: true, Alias: "o", Subquery: distinctOnQuery(), Condition: &dbCore.RawCondition{SQL: "1=1"},
	})
	requireUnsupported(t, err, "DISTINCT ON")

	_, _, err = d().FormatUnion(nil, false)
	require.Error(t, err)
	_, _, err = d().FormatSubquery(nil, "o")
	require.Error(t, err)
}

func TestFormatFromClauseErrors(t *testing.T) {
	_, _, err := d().formatFromClause(&dbCore.FromClause{IsSubquery: true, Alias: "o"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no query")

	_, _, err = d().formatFromClause(&dbCore.FromClause{
		IsSubquery: true,
		Subquery:   &dbCore.Query{Select: &dbCore.SelectClause{Fields: []string{"Id"}}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alias")

	sql, _, err := d().formatFromClause(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)
}

func TestSelectStarAndEmptyQuery(t *testing.T) {
	sql, args, err := d().FormatQuery(&dbCore.Query{})
	require.NoError(t, err)
	assert.Equal(t, "SELECT *", sql)
	assert.Nil(t, args)
}

func TestNilPartsAreErrorsNotPanics(t *testing.T) {
	for name, query := range map[string]*dbCore.Query{
		"nil CTE":   {CTEs: []*dbCore.CTEClause{nil}},
		"nil UNION": {Unions: []*dbCore.UnionClause{nil}},
		"nil union query": {
			Unions: []*dbCore.UnionClause{{}},
		},
		"nil condition": {Where: &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{nil}}},
	} {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				_, _, err := d().FormatQuery(query)
				require.Error(t, err)
			})
		})
	}
}

func TestUnsupportedErrorFormatting(t *testing.T) {
	err := unsupported("DISTINCT ON", "use ROW_NUMBER()")
	assert.Equal(t, "sqlserver does not support DISTINCT ON; use ROW_NUMBER()", err.Error())

	bare := unsupported("NATURAL JOIN", "")
	assert.Equal(t, "sqlserver does not support NATURAL JOIN", bare.Error())

	assert.True(t, dbCore.IsUnsupported(err))
	assert.False(t, dbCore.IsUnsupported(nil))
	assert.False(t, dbCore.IsUnsupported(assert.AnError))
}
