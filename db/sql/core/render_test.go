package core_test

// Tests for the render seam in render.go. Two promises are checked here. A nil context renders
// exactly what ToSQL does, over the whole golden corpus. And a context can express what an
// engine that brackets identifiers needs, which Postgres's and MySQL's rules cannot: 2024Orders
// is an identifier, Order is quoted, and nothing in an identifier slot reaches the SQL text
// unless the context accepted it.

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bracketSegment is one part of a dotted name under the bracketing rule: letters, digits,
// underscores and $, leading digit allowed. allDigits is the exception, because a bare number
// is a literal, not a name.
var (
	bracketSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_$]*$`)
	allDigits      = regexp.MustCompile(`^[0-9]+$`)
)

func bracketIsIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, part := range strings.Split(s, ".") {
		if !bracketSegment.MatchString(part) || allDigits.MatchString(part) {
			return false
		}
	}
	return true
}

// bracketQuote quotes each dotted part as [part], doubling any ] inside it so the name cannot
// close its own brackets.
func bracketQuote(identifier string) string {
	parts := strings.Split(identifier, ".")
	for i, part := range parts {
		parts[i] = "[" + strings.ReplaceAll(part, "]", "]]") + "]"
	}
	return strings.Join(parts, ".")
}

// bracketContext is a small stand-in for the context a bracket-quoting engine would pass. It
// sets only the identifier hooks, so each test adds the hook it is about.
func bracketContext(strict bool) *dbCore.RenderContext {
	return &dbCore.RenderContext{
		Dialect:         "bracketed",
		QuoteIdentifier: bracketQuote,
		IsIdentifier:    bracketIsIdentifier,
		Strict:          strict,
	}
}

func renderOK(t *testing.T, c dbCore.Condition, ctx *dbCore.RenderContext) (string, []any) {
	t.Helper()
	sql, args, err := dbCore.RenderCondition(c, ctx)
	require.NoError(t, err)
	return sql, args
}

func eq(field any, value any) dbCore.Condition {
	return &dbCore.BinaryCondition{Left: field, Operator: "=", Right: value}
}

// seamCorpus is the part of the golden corpus the seam renders: every SQL entry whose source is
// a condition or a clause. The window types have a ToSQL too, but they are not conditions and
// never reach RenderCondition, so they are left out by type; every other SQL entry must carry
// its source, since one that did not would drop out of the tests below unnoticed.
func seamCorpus(t *testing.T) []goldenEntry {
	t.Helper()
	var entries []goldenEntry
	for _, entry := range conditionCorpus() {
		if !strings.HasPrefix(entry.body, "sql:") {
			continue // a String() entry: no ToSQL, nothing the seam renders
		}
		switch entry.source.(type) {
		case *dbCore.WindowDefinition, *dbCore.WindowFrame, *dbCore.FrameBound:
			continue
		case nil:
			t.Fatalf("%s records SQL but carries no source to render again", entry.name)
		}
		entries = append(entries, entry)
	}
	require.NotEmpty(t, entries)
	return entries
}

// renderSeamEntry renders a corpus source through the seam function for its kind.
func renderSeamEntry(source dbCore.Condition, ctx *dbCore.RenderContext) (string, []any, error) {
	switch source := source.(type) {
	case *dbCore.WhereClause:
		return dbCore.RenderWhere(source, ctx)
	case *dbCore.HavingClause:
		return dbCore.RenderHaving(source, ctx)
	}
	return dbCore.RenderCondition(source, ctx)
}

// TestRenderConditionWithNilContextEqualsToSQL renders every entry of the golden corpus again,
// through the seam with a nil context, and requires the same SQL and args — nil against empty
// included — as ToSQL gave when the corpus was built. TestConditionToSQLMatchesGolden ties
// those to testdata/conditions.golden, so together they say the seam leaves Postgres and MySQL
// output where v2.4.3 had it. The seam's render is the second render of each source, which also
// exercises the RawCondition fix: at v2.4.3 the second render of a "?." RawCondition differed.
func TestRenderConditionWithNilContextEqualsToSQL(t *testing.T) {
	for _, entry := range seamCorpus(t) {
		sql, args, err := renderSeamEntry(entry.source, nil)
		require.NoErrorf(t, err, "%s: a nil context has no way to refuse", entry.name)
		assert.Equalf(t, entry.body, formatSQLEntryBody(sql, args),
			"%s: a nil context must render exactly what ToSQL renders", entry.name)
	}

	// With a nil context the result is ToSQL's for any condition, not only the built-in ones:
	// a struct embedding a built-in is not rendered through the ToSQLContext Go promotes to it.
	tenant := tenantScoped{BinaryCondition: &dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1}, tenant: 7}
	wantSQL, wantArgs := tenant.ToSQL()
	require.Equal(t, "(a = ? AND tenant_id = ?)", wantSQL)
	sql, args, err := dbCore.RenderCondition(tenant, nil)
	require.NoError(t, err)
	assert.Equal(t, wantSQL, sql)
	assert.Equal(t, wantArgs, args)
	sql, args, err = dbCore.RenderWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{tenant}}, nil)
	require.NoError(t, err)
	assert.Equal(t, wantSQL, sql)
	assert.Equal(t, wantArgs, args)
}

// TestRenderConditionWithEmptyContextMatchesGolden renders the corpus through a zero
// RenderContext — every hook nil, not Strict — and requires what the golden holds, except for
// the corrections any non-nil context makes on purpose (see RenderContext), each listed with
// what it renders instead. Every condition type renders a nil context and a non-nil one in
// separate code, and TestRenderConditionWithNilContextEqualsToSQL only ever reaches the nil
// side; this is what says the two still agree wherever they are meant to.
func TestRenderConditionWithEmptyContextMatchesGolden(t *testing.T) {
	const escapeHint = "; the escape must be exactly one character other than a single quote"
	corrected := map[string]string{
		"like/escape-multi-char-verbatim":   `err:  this dialect does not support LIKE ESCAPE "!!"` + escapeHint + "\n",
		"like/escape-quote-verbatim":        `err:  this dialect does not support LIKE ESCAPE "'"` + escapeHint + "\n",
		"composite/operator-verbatim":       "sql:  \"(a = ? OR b = ?)\"\nargs: [int(1), int(2)]\n",
		"composite/empty-child-not-skipped": "sql:  \"a = ?\"\nargs: [int(1)]\n",
		"where/empty-operator":              "sql:  \"a = ? AND b = ?\"\nargs: [int(1), int(2)]\n",
		"where/empty-part-not-skipped":      "sql:  \"a = ?\"\nargs: [int(1)]\n",
	}

	seen := map[string]bool{}
	for _, entry := range seamCorpus(t) {
		sql, args, err := renderSeamEntry(entry.source, &dbCore.RenderContext{})
		got := formatSQLEntryBody(sql, args)
		if err != nil {
			got = fmt.Sprintf("err:  %s\n", err)
		}

		want, isCorrected := corrected[entry.name]
		if !isCorrected {
			want = entry.body
		}
		seen[entry.name] = true
		assert.Equalf(t, want, got, "%s: a zero context renders what a nil one does, bar the listed corrections", entry.name)
	}
	for name := range corrected {
		assert.Truef(t, seen[name], "%s is listed as a correction but is not in the corpus", name)
	}
}

// tenantScoped is an app-defined condition that embeds a built-in one and adds a predicate of
// its own in an overriding ToSQL. Go promotes the embedded condition's ToSQLContext to it, and
// that renders the embedded condition alone.
type tenantScoped struct {
	*dbCore.BinaryCondition
	tenant int
}

func (c tenantScoped) ToSQL() (string, []interface{}) {
	sql, args := c.BinaryCondition.ToSQL()
	return "(" + sql + " AND tenant_id = ?)", append(args, c.tenant)
}

// guardedCondition embeds a built-in condition and overrides only Validate.
type guardedCondition struct{ *dbCore.BinaryCondition }

var errGuardRefuses = errors.New("the app refuses this condition")

func (guardedCondition) Validate() error { return errGuardRefuses }

// delegatingCondition holds a built-in in a named field and opts into the seam itself, which is
// how a condition type gets both the built-in's rendering and the context.
type delegatingCondition struct {
	inner  *dbCore.BinaryCondition
	tenant int
}

func (c delegatingCondition) ToSQL() (string, []interface{}) {
	sql, args, _ := c.ToSQLContext(nil)
	return sql, args
}

func (c delegatingCondition) ToSQLContext(ctx *dbCore.RenderContext) (string, []any, error) {
	sql, args, err := c.inner.ToSQLContext(ctx)
	if err != nil {
		return "", nil, err
	}
	return "(" + sql + " AND tenant_id = ?)", append(args, c.tenant), nil
}

// TestRenderConditionDoesNotTrustPromotedToSQLContext: a struct that embeds a built-in
// condition gets the built-in's ToSQLContext through Go's method promotion, whatever its own
// ToSQL and Validate say. RenderCondition renders such a struct through its ToSQL, validated
// under Strict, so the predicate an overriding ToSQL adds — a tenant filter, which keeps an
// UPDATE or DELETE to one tenant's rows — is never dropped for the embedded one.
func TestRenderConditionDoesNotTrustPromotedToSQLContext(t *testing.T) {
	tenant := tenantScoped{BinaryCondition: &dbCore.BinaryCondition{Left: "Id", Operator: "=", Right: 1}, tenant: 7}
	for name, ctx := range map[string]*dbCore.RenderContext{
		"zero":    {},
		"lenient": bracketContext(false),
		"strict":  bracketContext(true),
	} {
		sql, args := renderOK(t, tenant, ctx)
		assert.Equalf(t, "(Id = ? AND tenant_id = ?)", sql, "%s context", name)
		assert.Equalf(t, []any{1, 7}, args, "%s context", name)

		sql, args, err := dbCore.RenderWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{eq("Status", 2), tenant}}, ctx)
		require.NoError(t, err)
		assert.Containsf(t, sql, "tenant_id = ?", "%s context: the tenant filter must survive a WHERE", name)
		assert.Equalf(t, []any{2, 1, 7}, args, "%s context", name)
	}

	// The same holds for a pointer to the struct, and through a struct that embeds it in turn.
	pointer := &tenantScoped{BinaryCondition: &dbCore.BinaryCondition{Left: "Id", Operator: "=", Right: 1}, tenant: 7}
	sql, _ := renderOK(t, pointer, bracketContext(true))
	assert.Equal(t, "(Id = ? AND tenant_id = ?)", sql)
	deeper := struct{ tenantScoped }{tenant}
	sql, _ = renderOK(t, deeper, bracketContext(true))
	assert.Equal(t, "(Id = ? AND tenant_id = ?)", sql)

	// Under Strict the struct's own Validate runs, where the promoted ToSQLContext would have
	// skipped it; and the promoted built-in Validate refuses what the built-in's refuses.
	guarded := guardedCondition{&dbCore.BinaryCondition{Left: "Id", Operator: "=", Right: 1}}
	require.ErrorIs(t, dbCore.ValidateCondition(guarded), errGuardRefuses)
	_, _, err := dbCore.RenderCondition(guarded, bracketContext(true))
	assert.ErrorIs(t, err, errGuardRefuses)
	_, _, err = dbCore.RenderCondition(tenantScoped{BinaryCondition: eq("a = 1 OR 1=1", 1).(*dbCore.BinaryCondition)}, bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `the left side of a comparison is "a = 1 OR 1=1", which is not a column reference`)

	// A condition that holds the built-in in a named field and declares ToSQLContext itself is
	// given the context.
	delegating := delegatingCondition{inner: &dbCore.BinaryCondition{Left: "2024Orders.Id", Operator: "=", Right: 1}, tenant: 7}
	sql, args := renderOK(t, delegating, bracketContext(true))
	assert.Equal(t, "([2024Orders].[Id] = ? AND tenant_id = ?)", sql)
	assert.Equal(t, []any{1, 7}, args)
}

// TestRawConditionToSQLIsIdempotent pins the fix for the single-use RawCondition. ToSQL used to
// consume the receiver's Args, so the second render of a "?." condition — what rendering a
// cloned builder does, since clones share their conditions — substituted the wrong args.
func TestRawConditionToSQLIsIdempotent(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{"dot-column", "?.id = ?", []any{"users", 5}},
		{"dot-dot", "?.? = ?", []any{"users", "id", 5}},
		{"join between two dot placeholders", "?.id = ?.?", []any{"tags", "post_tags", "tag_id"}},
		{"leftover args", "?.id = ?", []any{"t", 1, 2}},
		{"fast path", "a = ? AND b = ?", []any{1, 2}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := &dbCore.RawCondition{SQL: tc.sql, Args: append([]any(nil), tc.args...)}

			firstSQL, firstArgs := raw.ToSQL()
			secondSQL, secondArgs := raw.ToSQL()
			assert.Equal(t, firstSQL, secondSQL)
			assert.Equal(t, firstArgs, secondArgs)
			assert.Equal(t, tc.args, raw.Args, "rendering must leave the receiver's Args alone")

			// What a render returns belongs to the caller. The fast path used to return the
			// receiver's own slice, so writing to it wrote into the condition.
			if len(firstArgs) > 0 {
				firstArgs[0] = "overwritten"
				assert.Equal(t, tc.args, raw.Args)
				_, thirdArgs := raw.ToSQL()
				assert.Equal(t, secondArgs, thirdArgs)
			}

			// The same holds under a context.
			ctx := bracketContext(true)
			ctxFirstSQL, ctxFirstArgs := renderOK(t, raw, ctx)
			ctxSecondSQL, ctxSecondArgs := renderOK(t, raw, ctx)
			assert.Equal(t, ctxFirstSQL, ctxSecondSQL)
			assert.Equal(t, ctxFirstArgs, ctxSecondArgs)
			assert.Equal(t, tc.args, raw.Args)
		})
	}

	// The shape the defect actually took: one condition held by two WHERE clauses, as a
	// builder and its clone hold it.
	shared := &dbCore.RawCondition{SQL: "?.id = ?.?", Args: []any{"tags", "post_tags", "tag_id"}}
	original := &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{shared}}
	clone := &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{shared}}

	originalSQL, originalArgs := original.ToSQL()
	cloneSQL, cloneArgs := clone.ToSQL()
	assert.Equal(t, "tags.id = post_tags.tag_id", originalSQL)
	assert.Equal(t, originalSQL, cloneSQL)
	assert.Equal(t, originalArgs, cloneArgs)
}

// TestRawConditionMalformedFallsBackToOriginalArgs: a "?." placeholder with no args left for it
// falls back to the SQL as written, and must hand back every arg as given. The fallback used to
// return the receiver's Args after the expansion had already consumed some of them.
func TestRawConditionMalformedFallsBackToOriginalArgs(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{"dot-dot one arg short", "?.? = 1", []any{"users"}},
		{"dot-column with no args", "?.id IS NULL", nil},
		{"runs out after consuming some", "?.id = ? AND ?.name = ?", []any{"users", 1}},
		{"dot-dot after a value", "? < ?.?", []any{5, "events"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := &dbCore.RawCondition{SQL: tc.sql, Args: append([]any(nil), tc.args...)}

			sql, args := raw.ToSQL()
			assert.Equal(t, tc.sql, sql)
			assert.Equal(t, tc.args, args, "every arg as given, not what the partial expansion left")
			assert.Equal(t, tc.args, raw.Args)

			sql, args = raw.ToSQL()
			assert.Equal(t, tc.sql, sql)
			assert.Equal(t, tc.args, args)

			// A context that is not Strict falls back the same way.
			sql, args = renderOK(t, raw, bracketContext(false))
			assert.Equal(t, tc.sql, sql)
			assert.Equal(t, tc.args, args)

			// A Strict one refuses: the "?." left behind would reach the server as a value
			// placeholder followed by ".<column>".
			_, _, err := dbCore.RenderCondition(raw, bracketContext(true))
			require.Error(t, err)
			assert.Contains(t, err.Error(), `runs out of args at a "?." placeholder`)
		})
	}
}

// TestRenderContextQuotesEveryIdentifierSlot walks every identifier slot in the family and
// checks the context both decides what an identifier is and quotes it. Value slots still bind.
func TestRenderContextQuotesEveryIdentifierSlot(t *testing.T) {
	tests := []struct {
		name     string
		cond     dbCore.Condition
		wantSQL  string
		wantArgs []any
	}{
		{"binary left, digit-leading", eq("2024Orders.Id", 1), "[2024Orders].[Id] = ?", []any{1}},
		{"binary left, reserved word", eq("Order", 1), "[Order] = ?", []any{1}},
		{"binary left, schema-qualified", eq("dbo.2024Orders.Id", 1), "[dbo].[2024Orders].[Id] = ?", []any{1}},
		{"binary left, Identifier", eq(dbCore.Identifier("users.Id"), 1), "[users].[Id] = ?", []any{1}},
		{"binary left, Raw is verbatim", eq(dbCore.Raw("lower(Email)"), "user@example.com"), "lower(Email) = ?", []any{"user@example.com"}},
		{"binary right, Identifier", eq("orders.UserId", dbCore.Identifier("2024Orders.Id")), "[orders].[UserId] = [2024Orders].[Id]", nil},
		{"binary right, string is a value", eq("Status", "Order"), "[Status] = ?", []any{"Order"}},
		{"binary right, Raw is verbatim", eq("CreatedAt", dbCore.Raw("SYSUTCDATETIME()")), "[CreatedAt] = SYSUTCDATETIME()", nil},
		{"unary operand", &dbCore.UnaryCondition{Operator: "NOT", Operand: "IsActive"}, "NOT [IsActive]", nil},
		{"in field", &dbCore.InCondition{Field: "2024Orders.Status", Values: []any{1, 2}}, "[2024Orders].[Status] IN (?, ?)", []any{1, 2}},
		{"between field", &dbCore.BetweenCondition{Field: dbCore.Identifier("2024Orders.CreatedAt"), Lower: 1, Upper: 2}, "[2024Orders].[CreatedAt] BETWEEN ? AND ?", []any{1, 2}},
		{"like field", &dbCore.LikeCondition{Field: "Name", Pattern: "a%"}, "[Name] LIKE ?", []any{"a%"}},
		{"is null field", &dbCore.IsNullCondition{Field: "2024Orders.DeletedAt", Not: true}, "[2024Orders].[DeletedAt] IS NOT NULL", nil},
		{"raw dot placeholders", &dbCore.RawCondition{SQL: "?.id = ?.?", Args: []any{"2024Orders", "post_tags", "tag_id"}}, "[2024Orders].[id] = [post_tags].[tag_id]", []any{}},
		{"raw dot placeholder, reserved literal column", &dbCore.RawCondition{SQL: "?.Order = ?", Args: []any{"t", 1}}, "[t].[Order] = ?", []any{1}},
		{"composite reaches nested operands", &dbCore.CompositeCondition{Operator: "OR", Conditions: []dbCore.Condition{
			eq("Order", 1),
			&dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{
				&dbCore.IsNullCondition{Field: "2024Orders.Id"},
				eq("User", 2),
			}},
		}}, "([Order] = ? OR ([2024Orders].[Id] IS NULL AND [User] = ?))", []any{1, 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := renderOK(t, tt.cond, bracketContext(true))
			assert.Equal(t, tt.wantSQL, sql)
			assert.Equal(t, tt.wantArgs, args)
		})
	}

	// The same slots through RenderWhere and RenderHaving.
	where := &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{eq("2024Orders.Id", 1), eq("Order", 2)}}
	sql, args, err := dbCore.RenderWhere(where, bracketContext(true))
	require.NoError(t, err)
	assert.Equal(t, "[2024Orders].[Id] = ? AND [Order] = ?", sql)
	assert.Equal(t, []any{1, 2}, args)

	having := &dbCore.HavingClause{Condition: &dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 1}}
	sql, args, err = dbCore.RenderHaving(having, bracketContext(true))
	require.NoError(t, err)
	assert.Equal(t, "COUNT(*) > ?", sql)
	assert.Equal(t, []any{1}, args)

	// A WHERE clause nested as a condition — both clause types satisfy Condition — is routed
	// through the context like any other, and parenthesised so its OR cannot bind across the
	// enclosing AND.
	nestedWhere := &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
		eq("TenantId", 7),
		&dbCore.WhereClause{Operator: "OR", Conditions: []dbCore.Condition{eq("2024Orders.Id", 1), eq("Order", 2)}},
	}}
	sql, args, err = dbCore.RenderWhere(nestedWhere, bracketContext(true))
	require.NoError(t, err)
	assert.Equal(t, "[TenantId] = ? AND ([2024Orders].[Id] = ? OR [Order] = ?)", sql)
	assert.Equal(t, []any{7, 1, 2}, args)
	sql, args = renderOK(t, &dbCore.HavingClause{Condition: eq("Order", 3)}, bracketContext(true))
	assert.Equal(t, "[Order] = ?", sql)
	assert.Equal(t, []any{3}, args)

	// Without a context the Postgres/MySQL rule applies: the digit-leading name is not an
	// identifier there, so it is bound, and nothing is quoted.
	sql, args = eq("2024Orders.Id", 1).ToSQL()
	assert.Equal(t, "? = ?", sql)
	assert.Equal(t, []any{"2024Orders.Id", 1}, args)

	// And a nested WHERE clause renders through its ToSQL, unparenthesised, as at v2.4.3.
	sql, args, err = dbCore.RenderWhere(nestedWhere, nil)
	require.NoError(t, err)
	assert.Equal(t, "TenantId = ? AND ? = ? OR Order = ?", sql)
	assert.Equal(t, []any{7, "2024Orders.Id", 1, 2}, args)
}

// TestRenderContextStrictRefusesNonIdentifier: under Strict a non-identifier in an identifier
// slot is an error instead of a bound value, and the refusal reads exactly as Validate's does,
// because both come from the same function.
func TestRenderContextStrictRefusesNonIdentifier(t *testing.T) {
	const hostile = "a = 1 OR 1=1"
	validating := map[string]dbCore.Condition{
		"binary left":    eq(hostile, true),
		"unary operand":  &dbCore.UnaryCondition{Operator: "NOT", Operand: hostile},
		"in field":       &dbCore.InCondition{Field: hostile, Values: []any{1}},
		"between field":  &dbCore.BetweenCondition{Field: dbCore.Identifier(hostile), Lower: 1, Upper: 2},
		"like field":     &dbCore.LikeCondition{Field: hostile, Pattern: "a%"},
		"is null field":  &dbCore.IsNullCondition{Field: hostile},
		"empty in field": &dbCore.InCondition{Field: hostile, Values: []any{}},
	}

	for name, cond := range validating {
		t.Run(name, func(t *testing.T) {
			_, _, err := dbCore.RenderCondition(cond, bracketContext(true))
			require.Error(t, err)

			validateErr := dbCore.ValidateCondition(cond)
			require.Error(t, validateErr)
			assert.Equal(t, validateErr.Error(), err.Error(), "the two refusals must not drift apart")

			// A context that is not Strict degrades the way ToSQL does: bound, not emitted.
			sql, args := renderOK(t, cond, bracketContext(false))
			assert.NotContains(t, sql, "OR 1=1")
			assert.Contains(t, args, hostile)
		})
	}

	// Slots Validate does not cover are refused in the same words.
	_, _, err := dbCore.RenderCondition(eq("Id", dbCore.Identifier(hostile)), bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `the right side of a comparison is "a = 1 OR 1=1", which is not a column reference`)

	_, _, err = dbCore.RenderCondition(&dbCore.RawCondition{SQL: "?.id = ?", Args: []any{"users; DROP TABLE users", 1}}, bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `a "?." placeholder in a RawCondition is "users; DROP TABLE users"`)

	// A refusal deep inside a composite reaches the caller, and so does one inside a WHERE
	// clause nested as a condition.
	const hostileLeft = `the left side of a comparison is "a = 1 OR 1=1", which is not a column reference`
	nested := &dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{
		eq("Id", 1),
		&dbCore.CompositeCondition{Operator: "OR", Conditions: []dbCore.Condition{eq(hostile, true)}},
	}}
	_, _, err = dbCore.RenderCondition(nested, bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), hostileLeft)
	_, _, err = dbCore.RenderCondition(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
		eq("2024Orders.Id", 1), eq(hostile, 2),
	}}, bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), hostileLeft)
	_, _, err = dbCore.RenderCondition(&dbCore.HavingClause{Condition: eq(hostile, 1)}, bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), hostileLeft)

	// The predicate is the context's, not IsSimpleIdentifier: Validate refuses a digit-leading
	// name, and a context that accepts one lets it through under Strict.
	digitLeading := eq("2024Orders.Id", 1)
	assert.Error(t, dbCore.ValidateCondition(digitLeading))
	sql, _ := renderOK(t, digitLeading, bracketContext(true))
	assert.Equal(t, "[2024Orders].[Id] = ?", sql)
}

// columnName is an app's own type for column names. It is not a string to a type switch, so
// the condition types bind it as a value.
type columnName string

// TestRenderContextStrictRefusesNonStringOperand: nil, a number or a named string type in an
// identifier slot is bound as a value, which compares a constant — nil IS NULL,
// 'DeletedAt' IS NOT NULL — and matches every row or none. Validate lets these through, since
// they were being bound before it existed; Strict refuses them.
func TestRenderContextStrictRefusesNonStringOperand(t *testing.T) {
	cases := map[string]struct {
		cond      dbCore.Condition
		wantError string
		lenient   string
		lenArgs   []any
	}{
		"is not null, named string type": {
			&dbCore.IsNullCondition{Field: columnName("DeletedAt"), Not: true},
			`the field of an IS NULL is core_test.columnName("DeletedAt"), which is not a column reference`,
			"? IS NOT NULL", []any{columnName("DeletedAt")},
		},
		"is null, nil field": {
			&dbCore.IsNullCondition{Field: nil},
			"the field of an IS NULL is nil, which is not a column reference",
			"? IS NULL", []any{nil},
		},
		"binary left, named string type": {
			eq(columnName("Order"), 1),
			`the left side of a comparison is core_test.columnName("Order"), which is not a column reference`,
			"? = ?", []any{columnName("Order"), 1},
		},
		"binary left, number": {
			eq(7, 1),
			"the left side of a comparison is int(7), which is not a column reference",
			"? = ?", []any{7, 1},
		},
		"unary operand, nil": {
			&dbCore.UnaryCondition{Operator: "NOT", Operand: nil},
			"the operand of NOT is nil, which is not a column reference",
			"NOT ?", []any{nil},
		},
		"in field, number": {
			&dbCore.InCondition{Field: 1, Values: []any{1}},
			"the field of an IN is int(1), which is not a column reference",
			"? IN (?)", []any{1, 1},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := dbCore.RenderCondition(tc.cond, bracketContext(true))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantError)

			// Validate and a lenient context keep v2.4.3's behaviour: bound, not refused.
			assert.NoError(t, dbCore.ValidateCondition(tc.cond))
			sql, args := renderOK(t, tc.cond, bracketContext(false))
			assert.Equal(t, tc.lenient, sql)
			assert.Equal(t, tc.lenArgs, args)
		})
	}

	// The same refusal inside a WHERE clause, where a lost column would widen an UPDATE or
	// DELETE to every row.
	_, _, err := dbCore.RenderWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
		eq("TenantId", 7), &dbCore.IsNullCondition{Field: columnName("DeletedAt"), Not: true},
	}}, bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the field of an IS NULL is core_test.columnName")
}

// subqueryRecorder is a context whose Subquery hook renders a subquery the way an engine's
// dialect would — its identifiers through the same context, its WHERE through RenderWhere — and
// records the table of every subquery it was handed, in order.
func subqueryRecorder() (*dbCore.RenderContext, *[]string) {
	var routed []string
	ctx := bracketContext(true)
	ctx.Subquery = func(q *dbCore.Query) (string, []any, error) {
		routed = append(routed, q.From.Table)
		fields := make([]string, len(q.Select.Fields))
		for i, field := range q.Select.Fields {
			fields[i] = bracketQuote(field)
		}
		sql := "SELECT " + strings.Join(fields, ", ") + " FROM " + bracketQuote(q.From.Table)
		where, args, err := dbCore.RenderWhere(q.Where, ctx)
		if err != nil {
			return "", nil, err
		}
		if where != "" {
			sql += " WHERE " + where
		}
		return sql, args, nil
	}
	return ctx, &routed
}

func statusSubquery(table string, status any) *dbCore.Query {
	return &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"Id"}},
		From:   &dbCore.FromClause{Table: table},
		Where:  &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{eq("Status", status)}},
	}
}

// TestRenderContextRoutesNestedSubqueries: every *Query operand goes through ctx.Subquery, at
// any depth, and its args land where the SQL puts them.
func TestRenderContextRoutesNestedSubqueries(t *testing.T) {
	ctx, routed := subqueryRecorder()

	nested := &dbCore.InCondition{Field: "users.Id", IsSubquery: true, Subquery: &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"UserId"}},
		From:   &dbCore.FromClause{Table: "orders"},
		Where: &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
			&dbCore.ExistsCondition{Query: statusSubquery("2024Orders", "paid")},
		}},
	}}
	sql, args := renderOK(t, nested, ctx)
	assert.Equal(t, "[users].[Id] IN (SELECT [UserId] FROM [orders] WHERE EXISTS (SELECT [Id] FROM [2024Orders] WHERE [Status] = ?))", sql)
	assert.Equal(t, []any{"paid"}, args)
	assert.Equal(t, []string{"orders", "2024Orders"}, *routed)

	// Every operand kind that can hold a subquery.
	*routed = nil
	every := &dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{
		&dbCore.BinaryCondition{Left: statusSubquery("a", 1), Operator: "<", Right: statusSubquery("b", 2)},
		&dbCore.BetweenCondition{Field: "Total", Lower: statusSubquery("c", 3), Upper: 4},
		&dbCore.LikeCondition{Field: "Name", Pattern: statusSubquery("d", 5)},
		&dbCore.UnaryCondition{Operator: "EXISTS", Operand: statusSubquery("e", 6)},
		&dbCore.IsNullCondition{Field: statusSubquery("f", 7)},
		&dbCore.ExistsCondition{Query: statusSubquery("g", 8), Not: true},
	}}
	sql, args = renderOK(t, every, ctx)
	assert.Equal(t, "("+
		"(SELECT [Id] FROM [a] WHERE [Status] = ?) < (SELECT [Id] FROM [b] WHERE [Status] = ?) AND "+
		"[Total] BETWEEN (SELECT [Id] FROM [c] WHERE [Status] = ?) AND ? AND "+
		"[Name] LIKE (SELECT [Id] FROM [d] WHERE [Status] = ?) AND "+
		"EXISTS (SELECT [Id] FROM [e] WHERE [Status] = ?) AND "+
		"(SELECT [Id] FROM [f] WHERE [Status] = ?) IS NULL AND "+
		"NOT EXISTS (SELECT [Id] FROM [g] WHERE [Status] = ?))", sql)
	assert.Equal(t, []any{1, 2, 3, 4, 5, 6, 7, 8}, args)
	assert.Equal(t, []string{"a", "b", "c", "d", "e", "f", "g"}, *routed)

	// A refusal from the hook reaches the caller unchanged, so an UnsupportedError stays one.
	refusing := bracketContext(true)
	refusing.Subquery = func(*dbCore.Query) (string, []any, error) {
		return "", nil, dbCore.Unsupported("bracketed", "DISTINCT ON", "")
	}
	_, _, err := dbCore.RenderCondition(&dbCore.ExistsCondition{Query: statusSubquery("t", 1)}, refusing)
	assert.True(t, dbCore.IsUnsupported(err), "got %v", err)

	// A nil subquery is an error under a context; buildSubquerySQL would dereference it.
	for _, holder := range []dbCore.Condition{
		&dbCore.ExistsCondition{},
		&dbCore.InCondition{Field: "Id", IsSubquery: true},
	} {
		for _, context := range []*dbCore.RenderContext{ctx, bracketContext(false)} {
			_, _, err = dbCore.RenderCondition(holder, context)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot render a nil subquery")
		}
	}

	// Without a Subquery hook a lenient context renders the subquery as it always has, through
	// core, with nothing inside it quoted.
	withoutHook := &dbCore.InCondition{Field: "Id", IsSubquery: true, Subquery: statusSubquery("orders", 1)}
	sql, _ = renderOK(t, withoutHook, bracketContext(false))
	assert.Equal(t, "[Id] IN (SELECT Id FROM orders WHERE Status = ?)", sql)

	// A Strict one refuses it, in every operand kind, because the subquery's conditions would
	// escape the context: here an operator Strict refuses at the top level.
	injected := statusSubquery("2024Orders", 1)
	injected.Where = &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
		&dbCore.CompositeCondition{Operator: "OR 1=1 OR", Conditions: []dbCore.Condition{eq("a", 1), eq("b", 2)}},
	}}
	for name, holder := range map[string]dbCore.Condition{
		"in":      withoutHook,
		"exists":  &dbCore.ExistsCondition{Query: injected},
		"binary":  &dbCore.BinaryCondition{Left: "Total", Operator: ">", Right: injected},
		"between": &dbCore.BetweenCondition{Field: "Total", Lower: injected, Upper: 2},
		"like":    &dbCore.LikeCondition{Field: "Name", Pattern: injected},
		"is null": &dbCore.IsNullCondition{Field: injected},
	} {
		_, _, err = dbCore.RenderCondition(holder, bracketContext(true))
		require.Errorf(t, err, "%s", name)
		assert.Containsf(t, err.Error(), "cannot render a subquery without a Subquery hook", "%s", name)
	}
}

// TestRenderContextEmptyIn: an IN with no values is "f IN ()" on every engine unless the
// context says otherwise, and when it does the whole predicate is replaced.
func TestRenderContextEmptyIn(t *testing.T) {
	// A Strict context renders a subquery only through its hook, so this one has one.
	ctx, _ := subqueryRecorder()
	ctx.EmptyIn = func(not bool) string {
		if not {
			return "1=1"
		}
		return "1=0"
	}

	sql, args := renderOK(t, &dbCore.InCondition{Field: "Id", Values: []any{}}, ctx)
	assert.Equal(t, "1=0", sql)
	assert.Nil(t, args)

	sql, _ = renderOK(t, &dbCore.InCondition{Field: "Id"}, ctx)
	assert.Equal(t, "1=0", sql, "a nil list is as empty as an empty one")

	sql, _ = renderOK(t, &dbCore.InCondition{Field: "Id", Not: true}, ctx)
	assert.Equal(t, "1=1", sql)

	// The field's args go with the field.
	sql, args = renderOK(t, &dbCore.InCondition{Field: statusSubquery("orders", "paid")}, ctx)
	assert.Equal(t, "1=0", sql)
	assert.Nil(t, args)

	// A non-empty list and a subquery are untouched.
	sql, args = renderOK(t, &dbCore.InCondition{Field: "Id", Values: []any{1}}, ctx)
	assert.Equal(t, "[Id] IN (?)", sql)
	assert.Equal(t, []any{1}, args)
	sql, _ = renderOK(t, &dbCore.InCondition{Field: "Id", IsSubquery: true, Subquery: statusSubquery("orders", 1)}, ctx)
	assert.Equal(t, "[Id] IN (SELECT [Id] FROM [orders] WHERE [Status] = ?)", sql)

	// It composes like any other predicate.
	sql, args = renderOK(t, &dbCore.CompositeCondition{Operator: "OR", Conditions: []dbCore.Condition{
		&dbCore.InCondition{Field: "Id"}, eq("Name", "x"),
	}}, ctx)
	assert.Equal(t, "(1=0 OR [Name] = ?)", sql)
	assert.Equal(t, []any{"x"}, args)

	// Strict still judges the field, even though the field is dropped.
	_, _, err := dbCore.RenderCondition(&dbCore.InCondition{Field: "a = 1 OR 1=1"}, ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `the field of an IN is "a = 1 OR 1=1", which is not a column reference`)

	// Without the hook a context keeps today's form.
	sql, args = renderOK(t, &dbCore.InCondition{Field: "Id", Values: []any{}}, bracketContext(true))
	assert.Equal(t, "[Id] IN ()", sql)
	assert.Nil(t, args)
}

// TestRenderContextOperatorMapping: ctx.Operator sees the operator of every BinaryCondition and
// UnaryCondition, can map or refuse it, and never sees the keywords the other types spell.
func TestRenderContextOperatorMapping(t *testing.T) {
	var seen []string
	ctx := bracketContext(true)
	// The EXISTS below needs a Subquery hook under Strict; this one keeps the subquery's own
	// operators out of the record.
	ctx.Subquery = func(*dbCore.Query) (string, []any, error) { return "SELECT 1", nil, nil }
	ctx.Operator = func(op string) (string, error) {
		seen = append(seen, op)
		switch strings.ToUpper(op) {
		case "ILIKE":
			return "LIKE", nil
		case "@>":
			return "", dbCore.Unsupported("bracketed", "the @> operator", "")
		}
		return op, nil
	}

	// The context is Strict, and ILIKE is not an operator Strict lets through on its own: with a
	// hook, the hook decides.
	sql, args := renderOK(t, &dbCore.BinaryCondition{Left: "Email", Operator: "ILIKE", Right: "%@example.com"}, ctx)
	assert.Equal(t, "[Email] LIKE ?", sql)
	assert.Equal(t, []any{"%@example.com"}, args)

	sql, _ = renderOK(t, &dbCore.UnaryCondition{Operator: "NOT", Operand: "IsActive"}, ctx)
	assert.Equal(t, "NOT [IsActive]", sql)

	_, _, err := dbCore.RenderCondition(&dbCore.BinaryCondition{Left: "Tags", Operator: "@>", Right: "x"}, ctx)
	assert.True(t, dbCore.IsUnsupported(err), "got %v", err)

	for _, keyword := range []dbCore.Condition{
		&dbCore.InCondition{Field: "Id", Values: []any{1}},
		&dbCore.BetweenCondition{Field: "Id", Lower: 1, Upper: 2},
		&dbCore.LikeCondition{Field: "Name", Pattern: "a%", Not: true},
		&dbCore.IsNullCondition{Field: "Id"},
		&dbCore.ExistsCondition{Query: statusSubquery("orders", 1)},
	} {
		renderOK(t, keyword, ctx)
	}
	assert.Equal(t, []string{"ILIKE", "NOT", "@>"}, seen, "only Binary and Unary operators go through the hook")

	// Without the hook a lenient context emits the operator verbatim, as ToSQL does.
	sql, _ = renderOK(t, &dbCore.BinaryCondition{Left: "Email", Operator: "ILIKE", Right: "x"}, bracketContext(false))
	assert.Equal(t, "[Email] ILIKE ?", sql)
}

// TestRenderContextStrictOperatorAllowList: without an Operator hook, Strict lets a
// BinaryCondition or UnaryCondition use only an operator every engine spells the same way. The
// operator is SQL, so one taken from input is refused rather than placed next to the operands,
// as a WHERE clause's AND/OR is.
func TestRenderContextStrictOperatorAllowList(t *testing.T) {
	binary := func(operator string) dbCore.Condition {
		return &dbCore.BinaryCondition{Left: "Id", Operator: operator, Right: 1}
	}
	unary := func(operator string) dbCore.Condition {
		return &dbCore.UnaryCondition{Operator: operator, Operand: "IsActive"}
	}

	// As written, whatever the case or spacing — the pagination filters say "like".
	for _, operator := range []string{"=", "<>", "!=", "<", "<=", ">", ">=", "LIKE", "like", "NOT LIKE", "not  like"} {
		sql, _ := renderOK(t, binary(operator), bracketContext(true))
		assert.Equal(t, "[Id] "+operator+" ?", sql)
	}
	for _, operator := range []string{"NOT", "not", "EXISTS", "NOT EXISTS"} {
		sql, _ := renderOK(t, unary(operator), bracketContext(true))
		assert.Equal(t, operator+" [IsActive]", sql)
	}

	for _, operator := range []string{"= 1 OR 1=1 --", "ILIKE", "@>", "IS", ""} {
		_, _, err := dbCore.RenderCondition(binary(operator), bracketContext(true))
		assert.Truef(t, dbCore.IsUnsupported(err), "binary %q: got %v", operator, err)
		assert.ErrorContainsf(t, err, "bracketed does not support the operator", "binary %q", operator)
	}
	for _, operator := range []string{"NOT 1=1 OR", "-", ""} {
		_, _, err := dbCore.RenderCondition(unary(operator), bracketContext(true))
		assert.Truef(t, dbCore.IsUnsupported(err), "unary %q: got %v", operator, err)
	}

	// Refused wherever it is nested, and verbatim in a lenient context and in ToSQL.
	_, _, err := dbCore.RenderWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
		eq("TenantId", 7), binary("= 1 OR 1=1 --"),
	}}, bracketContext(true))
	assert.True(t, dbCore.IsUnsupported(err), "got %v", err)
	sql, _ := renderOK(t, binary("= 1 OR 1=1 --"), bracketContext(false))
	assert.Equal(t, "[Id] = 1 OR 1=1 -- ?", sql)
	sql, _ = binary("= 1 OR 1=1 --").ToSQL()
	assert.Equal(t, "Id = 1 OR 1=1 -- ?", sql, "the goldens pin ToSQL's verbatim operator")
}

// TestRenderContextRawQuotesDotPlaceholders: ctx.Raw rewrites the text first, then the
// identifiers the "?." placeholders substitute, and the literal column after "?.", are quoted by
// the context.
func TestRenderContextRawQuotesDotPlaceholders(t *testing.T) {
	ilike := regexp.MustCompile(`(?i)\bILIKE\b`)
	var rewritten []string
	ctx := bracketContext(true)
	ctx.Raw = func(sql string) string {
		rewritten = append(rewritten, sql)
		return ilike.ReplaceAllString(sql, "LIKE")
	}

	// The join the ORM's many-to-many loaders build.
	sql, args := renderOK(t, &dbCore.RawCondition{SQL: "?.id = ?.?", Args: []any{"2024Orders", "post_tags", "tag_id"}}, ctx)
	assert.Equal(t, "[2024Orders].[id] = [post_tags].[tag_id]", sql)
	assert.Equal(t, []any{}, args)

	// The literal column sits in an identifier position too, and T-SQL wants a reserved or
	// digit-leading name delimited even after a dot.
	sql, _ = renderOK(t, &dbCore.RawCondition{SQL: "?.Order = 1 AND ?.2024Col = 2", Args: []any{"t", "t"}}, ctx)
	assert.Equal(t, "[t].[Order] = 1 AND [t].[2024Col] = 2", sql)

	// A schema-qualified name is quoted part by part; a literal column the caller delimited is
	// left as written, and so is the empty column of "?.*".
	sql, args = renderOK(t, &dbCore.RawCondition{SQL: `?."Id" = ?`, Args: []any{"dbo.2024Orders", 1}}, ctx)
	assert.Equal(t, `[dbo].[2024Orders]."Id" = ?`, sql)
	assert.Equal(t, []any{1}, args)
	sql, _ = renderOK(t, &dbCore.RawCondition{SQL: "EXISTS (SELECT ?.* FROM x)", Args: []any{"x"}}, ctx)
	assert.Equal(t, "EXISTS (SELECT [x].* FROM x)", sql)

	// A core.Raw arg is the caller vouching for the text, as everywhere else.
	sql, _ = renderOK(t, &dbCore.RawCondition{SQL: "?.id = 1", Args: []any{dbCore.Raw("[dbo].[2024Orders]")}}, ctx)
	assert.Equal(t, "[dbo].[2024Orders].[id] = 1", sql)

	// Raw runs before the expansion, on the text as written, on the fast path too.
	rewritten = nil
	sql, args = renderOK(t, &dbCore.RawCondition{SQL: "?.Email ilike ?", Args: []any{"users", "%@example.com"}}, ctx)
	assert.Equal(t, "[users].[Email] LIKE ?", sql)
	assert.Equal(t, []any{"%@example.com"}, args)
	sql, _ = renderOK(t, &dbCore.RawCondition{SQL: "Email ILIKE ?", Args: []any{"%@example.com"}}, ctx)
	assert.Equal(t, "Email LIKE ?", sql)
	assert.Equal(t, []string{"?.Email ilike ?", "Email ILIKE ?"}, rewritten)

	// Not Strict: a name that is not an identifier is still quoted, and cannot close its
	// brackets; a literal column that is not one is left as the caller wrote it.
	sql, _ = renderOK(t, &dbCore.RawCondition{SQL: "?.id = 1", Args: []any{"odd]name"}}, bracketContext(false))
	assert.Equal(t, "[odd]]name].[id] = 1", sql)
	sql, _ = renderOK(t, &dbCore.RawCondition{SQL: "?.1 = 1", Args: []any{"t"}}, bracketContext(false))
	assert.Equal(t, "[t].1 = 1", sql)

	// Strict: both are refused.
	_, _, err := dbCore.RenderCondition(&dbCore.RawCondition{SQL: "?.id = 1", Args: []any{"odd]name"}}, bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `a "?." placeholder in a RawCondition is "odd]name", which is not a column reference`)
	_, _, err = dbCore.RenderCondition(&dbCore.RawCondition{SQL: "?.1 = 1", Args: []any{"t"}}, bracketContext(true))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `the column "1" after a "?." placeholder in a RawCondition is not a column reference bracketed accepts`)

	// Without a context nothing is quoted, as at v2.4.3.
	sql, _ = (&dbCore.RawCondition{SQL: "?.id = ?.?", Args: []any{"2024Orders", "post_tags", "tag_id"}}).ToSQL()
	assert.Equal(t, "2024Orders.id = post_tags.tag_id", sql)
}

// TestRenderContextLikeEscapeValidation: under any context an ESCAPE has to be something that
// can be written as ESCAPE '<escape>'. ToSQL interpolates it as it stands.
func TestRenderContextLikeEscapeValidation(t *testing.T) {
	like := func(escape string) *dbCore.LikeCondition {
		return &dbCore.LikeCondition{Field: "Name", Pattern: "100!%", Escape: escape}
	}

	for _, escape := range []string{"!", `\`, "#", "§"} {
		sql, args := renderOK(t, like(escape), bracketContext(false))
		assert.Equal(t, "[Name] LIKE ? ESCAPE '"+escape+"'", sql)
		assert.Equal(t, []any{"100!%"}, args)
	}

	sql, _ := renderOK(t, like(""), bracketContext(false))
	assert.Equal(t, "[Name] LIKE ?", sql, "no escape, no ESCAPE clause")

	for _, escape := range []string{"!!", "'", "ab", "\xff"} {
		_, _, err := dbCore.RenderCondition(like(escape), bracketContext(false))
		require.Errorf(t, err, "escape %q", escape)
		assert.True(t, dbCore.IsUnsupported(err), "got %v", err)
		assert.Contains(t, err.Error(), "bracketed does not support LIKE ESCAPE")
	}

	// It is the context that turns the check on, not Strict or any hook.
	_, _, err := dbCore.RenderCondition(like("'"), &dbCore.RenderContext{})
	assert.True(t, dbCore.IsUnsupported(err), "got %v", err)

	// ToSQL still writes it verbatim; the goldens pin that for Postgres and MySQL.
	sql, _ = like("!!").ToSQL()
	assert.Equal(t, "Name LIKE ? ESCAPE '!!'", sql)
}

// appCondition is a condition an app wrote before the seam existed: ToSQL and nothing else.
type appCondition struct {
	sql  string
	args []any
}

func (c appCondition) ToSQL() (string, []interface{}) { return c.sql, c.args }

// validatingAppCondition also implements ValidatingCondition.
type validatingAppCondition struct {
	appCondition
	err error
}

func (c validatingAppCondition) Validate() error { return c.err }

// contextAppCondition is an app-defined condition that opted into the seam.
type contextAppCondition struct{ column string }

func (c contextAppCondition) ToSQL() (string, []interface{}) {
	return c.column + " > 0", nil
}

func (c contextAppCondition) ToSQLContext(ctx *dbCore.RenderContext) (string, []any, error) {
	if ctx == nil || ctx.QuoteIdentifier == nil {
		sql, args := c.ToSQL()
		return sql, args, nil
	}
	return ctx.QuoteIdentifier(c.column) + " > 0", nil, nil
}

// TestRenderContextCustomConditionFallsBackAndIsValidated: a condition type the framework does
// not know still renders through the seam, via its ToSQL, and under Strict its own Validate
// gets the chance to refuse — directly and wherever it is nested.
func TestRenderContextCustomConditionFallsBackAndIsValidated(t *testing.T) {
	plain := appCondition{sql: "custom_fn(col) > ?", args: []any{1}}
	sql, args := renderOK(t, plain, bracketContext(true))
	assert.Equal(t, "custom_fn(col) > ?", sql, "an app-defined condition renders its own SQL")
	assert.Equal(t, []any{1}, args)

	refusal := errors.New("custom condition refuses")
	refusing := validatingAppCondition{appCondition: plain, err: refusal}

	_, _, err := dbCore.RenderCondition(refusing, bracketContext(true))
	assert.ErrorIs(t, err, refusal)

	for name, holder := range map[string]dbCore.Condition{
		"composite":        &dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{eq("Id", 1), refusing}},
		"nested composite": &dbCore.CompositeCondition{Operator: "OR", Conditions: []dbCore.Condition{&dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{refusing}}}},
	} {
		_, _, err = dbCore.RenderCondition(holder, bracketContext(true))
		assert.ErrorIsf(t, err, refusal, "%s", name)
	}
	_, _, err = dbCore.RenderWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{refusing}}, bracketContext(true))
	assert.ErrorIs(t, err, refusal)
	_, _, err = dbCore.RenderHaving(&dbCore.HavingClause{Condition: refusing}, bracketContext(true))
	assert.ErrorIs(t, err, refusal)

	// Only Strict validates; a lenient context and a nil one render it as ToSQL would.
	sql, _ = renderOK(t, refusing, bracketContext(false))
	assert.Equal(t, "custom_fn(col) > ?", sql)
	sql, _ = renderOK(t, refusing, nil)
	assert.Equal(t, "custom_fn(col) > ?", sql)

	// An app-defined condition that implements ContextRenderer is given the context.
	sql, _ = renderOK(t, contextAppCondition{column: "2024Orders.Total"}, bracketContext(true))
	assert.Equal(t, "[2024Orders].[Total] > 0", sql)
	sql, _ = renderOK(t, contextAppCondition{column: "2024Orders.Total"}, nil)
	assert.Equal(t, "2024Orders.Total > 0", sql)

	// A nil condition is refused rather than skipped: a filter that silently lost a
	// condition matches more rows than its caller asked for.
	for name, holder := range map[string]func() error{
		"direct": func() error {
			_, _, err := dbCore.RenderCondition(nil, bracketContext(true))
			return err
		},
		"in a WHERE": func() error {
			_, _, err := dbCore.RenderWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{eq("Id", 1), nil}}, bracketContext(false))
			return err
		},
		"a nil built-in": func() error {
			_, _, err := dbCore.RenderCondition((*dbCore.BinaryCondition)(nil), bracketContext(false))
			return err
		},
		"a nil nested WHERE clause": func() error {
			_, _, err := dbCore.RenderCondition((*dbCore.WhereClause)(nil), bracketContext(false))
			return err
		},
		"a nil nested HAVING clause": func() error {
			_, _, err := dbCore.RenderCondition((*dbCore.HavingClause)(nil), bracketContext(false))
			return err
		},
	} {
		assert.ErrorContainsf(t, holder(), "cannot render a nil condition", "%s", name)
	}
}

// TestRenderWhereSkipsEmptyAndDefaultsToAnd: under a context a WHERE clause or composite with
// parts that render empty, or an empty Operator, renders as valid SQL. Without one it renders
// what ToSQL always has, dangling operator and double space included.
func TestRenderWhereSkipsEmptyAndDefaultsToAnd(t *testing.T) {
	ctx := bracketContext(true)
	empty := &dbCore.CompositeCondition{Operator: "OR"}
	blankRaw := &dbCore.RawCondition{SQL: "  "}

	where := &dbCore.WhereClause{Conditions: []dbCore.Condition{eq("a", 1), empty, blankRaw, eq("b", 2)}}
	sql, args, err := dbCore.RenderWhere(where, ctx)
	require.NoError(t, err)
	assert.Equal(t, "[a] = ? AND [b] = ?", sql)
	assert.Equal(t, []any{1, 2}, args)

	// Without a context both corrections are off: ToSQL joins with the empty operator — two
	// spaces — and keeps the empty part between them.
	legacy := &dbCore.WhereClause{Conditions: []dbCore.Condition{eq("a", 1), empty, eq("b", 2)}}
	sql, args = legacy.ToSQL()
	assert.Equal(t, "a = ?    b = ?", sql)
	assert.Equal(t, []any{1, 2}, args)
	sql, _, err = dbCore.RenderWhere(legacy, nil)
	require.NoError(t, err)
	assert.Equal(t, "a = ?    b = ?", sql, "a nil context is ToSQL")

	sql, _, err = dbCore.RenderWhere(&dbCore.WhereClause{Operator: "or", Conditions: []dbCore.Condition{eq("a", 1), eq("b", 2)}}, ctx)
	require.NoError(t, err)
	assert.Equal(t, "[a] = ? OR [b] = ?", sql)

	for name, nothing := range map[string]*dbCore.WhereClause{
		"nil":                nil,
		"no conditions":      {Operator: "AND"},
		"only empty parts":   {Operator: "AND", Conditions: []dbCore.Condition{empty, blankRaw}},
		"only empty, no op":  {Conditions: []dbCore.Condition{empty}},
		"nested empty parts": {Operator: "AND", Conditions: []dbCore.Condition{&dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{empty}}}},
	} {
		sql, args, err = dbCore.RenderWhere(nothing, ctx)
		require.NoErrorf(t, err, "%s", name)
		assert.Emptyf(t, sql, "%s", name)
		assert.Nilf(t, args, "%s", name)
	}

	// A composite skips its empty operands too, and parenthesises what is left.
	composite := &dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{eq("a", 1), empty}}
	sql, _ = renderOK(t, composite, ctx)
	assert.Equal(t, "[a] = ?", sql)
	sql, _ = composite.ToSQL()
	assert.Equal(t, "(a = ? AND )", sql, "ToSQL keeps the empty operand, as the goldens pin")
	sql, _ = renderOK(t, &dbCore.CompositeCondition{Conditions: []dbCore.Condition{eq("a", 1), empty, eq("b", 2)}}, ctx)
	assert.Equal(t, "([a] = ? AND [b] = ?)", sql)

	// RenderHaving has one condition, and no predicate when it renders empty.
	for name, nothing := range map[string]*dbCore.HavingClause{
		"nil":          nil,
		"no condition": {},
		"empty":        {Condition: empty},
	} {
		sql, args, err = dbCore.RenderHaving(nothing, ctx)
		require.NoErrorf(t, err, "%s", name)
		assert.Emptyf(t, sql, "%s", name)
		assert.Nilf(t, args, "%s", name)
	}
	sql, args, err = dbCore.RenderHaving(&dbCore.HavingClause{Condition: eq("Total", 5)}, ctx)
	require.NoError(t, err)
	assert.Equal(t, "[Total] = ?", sql)
	assert.Equal(t, []any{5}, args)
}

// TestCompositeOperatorWhitelistUnderStrict: the operator between two predicates is SQL, so
// under Strict only AND and OR may be it — in a CompositeCondition and in a WHERE clause alike.
func TestCompositeOperatorWhitelistUnderStrict(t *testing.T) {
	composite := func(operator string) *dbCore.CompositeCondition {
		return &dbCore.CompositeCondition{Operator: operator, Conditions: []dbCore.Condition{eq("a", 1), eq("b", 2)}}
	}
	where := func(operator string) *dbCore.WhereClause {
		return &dbCore.WhereClause{Operator: operator, Conditions: []dbCore.Condition{eq("a", 1), eq("b", 2)}}
	}

	for operator, want := range map[string]string{"AND": "AND", "and": "AND", " Or ": "OR", "": "AND"} {
		sql, _ := renderOK(t, composite(operator), bracketContext(true))
		assert.Equalf(t, "([a] = ? "+want+" [b] = ?)", sql, "operator %q", operator)

		sql, _, err := dbCore.RenderWhere(where(operator), bracketContext(true))
		require.NoError(t, err)
		assert.Equalf(t, "[a] = ? "+want+" [b] = ?", sql, "operator %q", operator)
	}

	for _, operator := range []string{"XOR", "AND NOT", "; DROP TABLE users; --"} {
		_, _, err := dbCore.RenderCondition(composite(operator), bracketContext(true))
		assert.Truef(t, dbCore.IsUnsupported(err), "composite %q: got %v", operator, err)

		_, _, err = dbCore.RenderWhere(where(operator), bracketContext(true))
		assert.Truef(t, dbCore.IsUnsupported(err), "where %q: got %v", operator, err)
	}

	// Refused wherever it is nested.
	nested := &dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{eq("c", 3), composite("XOR")}}
	_, _, err := dbCore.RenderCondition(nested, bracketContext(true))
	assert.True(t, dbCore.IsUnsupported(err), "got %v", err)

	// A lenient context and a nil one emit the operator as given.
	sql, _ := renderOK(t, composite("XOR"), bracketContext(false))
	assert.Equal(t, "([a] = ? XOR [b] = ?)", sql)
	sql, _ = composite("XOR").ToSQL()
	assert.Equal(t, "(a = ? XOR b = ?)", sql)
}
