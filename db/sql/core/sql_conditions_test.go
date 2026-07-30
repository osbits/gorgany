package core_test

// The condition types had no tests of their own until the engine-agnostic copies in
// db/sql/gorm/postgres/v2 were removed. That package's
// TestPostgresConditionHelperFunctions was the only thing pinning the rendered SQL of
// a condition family, and it pinned the duplicates rather than these originals, so the
// coverage moves here with the deletion.
//
// Everything is a pure string assertion — `?` placeholders, no dialect, no database —
// which is the whole reason these types never belonged in an engine package.

import (
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
)

func TestConditionsRenderExpectedSQL(t *testing.T) {
	tests := []struct {
		name     string
		cond     dbCore.Condition
		wantSQL  string
		wantArgs []interface{}
	}{
		{
			name:     "Equal",
			cond:     &dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
			wantSQL:  "a = ?",
			wantArgs: []interface{}{1},
		},
		{
			name:     "NotEqual",
			cond:     &dbCore.BinaryCondition{Left: "a", Operator: "!=", Right: 1},
			wantSQL:  "a != ?",
			wantArgs: []interface{}{1},
		},
		{
			name:     "GreaterThan",
			cond:     &dbCore.BinaryCondition{Left: "a", Operator: ">", Right: 1},
			wantSQL:  "a > ?",
			wantArgs: []interface{}{1},
		},
		{
			name:     "LessThan",
			cond:     &dbCore.BinaryCondition{Left: "a", Operator: "<", Right: 1},
			wantSQL:  "a < ?",
			wantArgs: []interface{}{1},
		},
		{
			name:     "In",
			cond:     &dbCore.InCondition{Field: "a", Values: []interface{}{1, 2}},
			wantSQL:  "a IN (?, ?)",
			wantArgs: []interface{}{1, 2},
		},
		{
			name:     "NotIn",
			cond:     &dbCore.InCondition{Field: "a", Values: []interface{}{1, 2}, Not: true},
			wantSQL:  "a NOT IN (?, ?)",
			wantArgs: []interface{}{1, 2},
		},
		{
			name:     "Between",
			cond:     &dbCore.BetweenCondition{Field: "a", Lower: 1, Upper: 2},
			wantSQL:  "a BETWEEN ? AND ?",
			wantArgs: []interface{}{1, 2},
		},
		{
			name:     "NotBetween",
			cond:     &dbCore.BetweenCondition{Field: "a", Lower: 1, Upper: 2, Not: true},
			wantSQL:  "a NOT BETWEEN ? AND ?",
			wantArgs: []interface{}{1, 2},
		},
		{
			name:     "IsNull",
			cond:     &dbCore.IsNullCondition{Field: "a"},
			wantSQL:  "a IS NULL",
			wantArgs: nil,
		},
		{
			name:     "IsNotNull",
			cond:     &dbCore.IsNullCondition{Field: "a", Not: true},
			wantSQL:  "a IS NOT NULL",
			wantArgs: nil,
		},
		{
			name:     "Like",
			cond:     &dbCore.LikeCondition{Field: "a", Pattern: "x%"},
			wantSQL:  "a LIKE ?",
			wantArgs: []interface{}{"x%"},
		},
		{
			name:     "Raw",
			cond:     &dbCore.RawCondition{SQL: "a = 1"},
			wantSQL:  "a = 1",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := tt.cond.ToSQL()
			assert.Equal(t, tt.wantSQL, sql)
			if tt.wantArgs == nil {
				assert.Empty(t, args)
				return
			}
			assert.Equal(t, tt.wantArgs, args)
		})
	}
}

func TestCompositeConditionGroupsAndParenthesises(t *testing.T) {
	and := &dbCore.CompositeCondition{
		Operator: "AND",
		Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
			&dbCore.BinaryCondition{Left: "b", Operator: "=", Right: 2},
		},
	}
	sql, args := and.ToSQL()
	assert.Equal(t, "(a = ? AND b = ?)", sql)
	assert.Equal(t, []interface{}{1, 2}, args)

	or := &dbCore.CompositeCondition{
		Operator: "OR",
		Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
			&dbCore.BinaryCondition{Left: "b", Operator: "=", Right: 2},
		},
	}
	sql, args = or.ToSQL()
	assert.Equal(t, "(a = ? OR b = ?)", sql)
	assert.Equal(t, []interface{}{1, 2}, args)

	// A single operand is rendered bare: the parentheses would change nothing but the
	// SQL a caller has to match.
	single := &dbCore.CompositeCondition{
		Operator: "AND",
		Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
		},
	}
	sql, args = single.ToSQL()
	assert.Equal(t, "a = ?", sql)
	assert.Equal(t, []interface{}{1}, args)

	// No operands contributes no SQL, which is what lets a builder append an empty
	// composite without emitting a dangling WHERE.
	empty := &dbCore.CompositeCondition{Operator: "AND"}
	sql, args = empty.ToSQL()
	assert.Empty(t, sql)
	assert.Empty(t, args)
}

// TestBetweenBoundsOnlyBindNonStrings pins the one place where these types do not
// behave like the removed postgres/v2 copies, which always bound both bounds as
// parameters. Here a string bound is treated as a literal SQL fragment, so a caller
// passing a date straight out of a query parameter puts it into the SQL text instead
// of a placeholder. MIGRATION_v2.md §10 documents it; this test is what makes a
// silent change to it fail.
func TestBetweenBoundsOnlyBindNonStrings(t *testing.T) {
	bound := &dbCore.BetweenCondition{Field: "age", Lower: 18, Upper: 65}
	sql, args := bound.ToSQL()
	assert.Equal(t, "age BETWEEN ? AND ?", sql)
	assert.Equal(t, []interface{}{18, 65}, args)

	literal := &dbCore.BetweenCondition{Field: "age", Lower: "18", Upper: "65"}
	sql, args = literal.ToSQL()
	assert.Equal(t, "age BETWEEN 18 AND 65", sql)
	assert.Empty(t, args)

	// BinaryCondition does not share the behaviour: only Left is an identifier, and a
	// string Right binds.
	binary := &dbCore.BinaryCondition{Left: "name", Operator: "=", Right: "ada"}
	sql, args = binary.ToSQL()
	assert.Equal(t, "name = ?", sql)
	assert.Equal(t, []interface{}{"ada"}, args)
}

// TestRawConditionIdentifierPlaceholders covers the handling the removed postgres/v2
// RawCondition did not have: it returned SQL and args untouched, so "?.id" bound the
// table name as a value and produced SQL the server rejects.
//
// Note that ToSQL consumes the receiver's Args, so a RawCondition is single-use; each
// case below therefore builds its own.
func TestRawConditionIdentifierPlaceholders(t *testing.T) {
	// "?.<column>" consumes one arg as the table or alias.
	one := &dbCore.RawCondition{SQL: "?.id = ?", Args: []interface{}{"users", 5}}
	sql, args := one.ToSQL()
	assert.Equal(t, "users.id = ?", sql)
	assert.Equal(t, []interface{}{5}, args)

	// "?.?" consumes two, for a dynamic column as well.
	two := &dbCore.RawCondition{SQL: "?.? = ?", Args: []interface{}{"users", "id", 5}}
	sql, args = two.ToSQL()
	assert.Equal(t, "users.id = ?", sql)
	assert.Equal(t, []interface{}{5}, args)

	// Raw SQL with no "?." sequence takes the fast path and is passed through, which is
	// what keeps every pre-existing raw condition rendering exactly as before.
	plain := &dbCore.RawCondition{SQL: "a = ? AND b = ?", Args: []interface{}{1, 2}}
	sql, args = plain.ToSQL()
	assert.Equal(t, "a = ? AND b = ?", sql)
	assert.Equal(t, []interface{}{1, 2}, args)
}
