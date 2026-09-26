package core_test

// The condition family, the window types and the aggregate renderer produce SQL with no
// dialect in sight: `?` placeholders, identifiers verbatim, a subquery through
// buildSubquerySQL with LIMIT and OFFSET spelled the Postgres/MySQL way. Every engine the
// framework ships renders a WHERE, a HAVING, a JOIN ON and an IN-subquery through exactly
// this code, so its output *is* the Postgres and MySQL output for those positions.
//
// A seam is about to be added — a context that lets an engine quote identifiers and route
// subqueries through its own dialect — and the promise that goes with it is that without a
// context the conditions render byte for byte what ToSQL renders at v2.4.3. A promise like that is only
// as good as the thing that checks it. TestConditionToSQLMatchesGolden is that thing: it
// renders one entry per condition type and operand kind and compares the lot with
// testdata/conditions.golden, which was generated at v2.4.3 (whose db/sql renders
// identically to v2.2.1).
//
// Two things are deliberately absent, because v2.4.3 got them wrong and pinning them would
// have made the fix look like a regression: a RawCondition whose "?." placeholders lack the
// args they need, which fell back to its original SQL with args it had already half
// consumed, and a second render of one RawCondition, which found its Args consumed by the
// first. Both are to be fixed, and the fixed behaviour gets tests of its own rather than a
// golden entry that would have to change. Every RawCondition below
// is still built for the one render it gets here.

import (
	"flag"
	"fmt"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/internal/goldentest"
)

// update rewrites the golden file instead of comparing against it. It is this package's own
// flag, so -update rewrites this package's golden file and no other; the mechanics are in
// db/sql/internal/goldentest.
var update = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

const conditionsGolden = "testdata/conditions.golden"

const conditionsGoldenHeader = `# Golden file for TestConditionToSQLMatchesGolden (db/sql/core/render_golden_test.go).
# What every condition type, window definition and aggregate renders with no dialect,
# which is what Postgres and MySQL emit for a WHERE, HAVING, JOIN ON or IN-subquery.
#
# Regenerate from the module root, then review the diff like any other change:
#   go test ./db/sql/core -run Golden -update
#
# sql and text are Go-quoted. args lists each bound value with its dynamic type;
# "nil" is a nil slice and "[]" an empty one, which callers can tell apart.
`

// goldenEntry is one named case: the lines recorded for it, each ending in a newline.
type goldenEntry struct {
	name string
	body string
	// source is what rendered body, for an entry sqlEntry made, so another test can render
	// the same corpus another way and compare.
	source interface{ ToSQL() (string, []any) }
}

// sqlEntry records a ToSQL result: the SQL and the args it binds.
func sqlEntry(name string, r interface{ ToSQL() (string, []any) }) goldenEntry {
	sql, args := r.ToSQL()
	return goldenEntry{name: name, body: formatSQLEntryBody(sql, args), source: r}
}

// formatSQLEntryBody is the body sqlEntry records for a SQL result.
func formatSQLEntryBody(sql string, args []any) string {
	return fmt.Sprintf("sql:  %q\nargs: %s\n", sql, goldentest.EncodeArgs(args))
}

// textEntry records a String result, which has no args to bind — or, for
// AggregateFunction's FILTER, throws them away.
func textEntry(name string, s fmt.Stringer) goldenEntry {
	return goldenEntry{name: name, body: fmt.Sprintf("text: %q\n", s.String())}
}

// assertMatchesGolden compares entries with the file at path, or rewrites it under -update;
// see goldentest.AssertMatches, which the builder's golden test shares.
func assertMatchesGolden(t *testing.T, path, header string, entries []goldenEntry) {
	t.Helper()
	golden := make([]goldentest.Entry, len(entries))
	for i, entry := range entries {
		golden[i] = goldentest.Entry{Name: entry.name, Body: entry.body}
	}
	goldentest.AssertMatches(t, path, header, golden, *update)
}

func intPtr(n int) *int { return &n }

// plainSubquery is the smallest subquery: SELECT user_id FROM orders.
func plainSubquery() *dbCore.Query {
	return &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"user_id"}},
		From:   &dbCore.FromClause{Table: "orders"},
	}
}

// boundSubquery binds one arg, so an entry can show where subquery args land relative to
// the outer condition's own.
func boundSubquery(table string, value any) *dbCore.Query {
	return &dbCore.Query{
		Select: &dbCore.SelectClause{Fields: []string{"id"}},
		From:   &dbCore.FromClause{Table: table},
		Where: &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "status", Operator: "=", Right: value},
		}},
		Limit: intPtr(1),
	}
}

// everyClauseSubquery reaches every clause buildSubquerySQL renders, in one query, so the
// order they are emitted in and the order their args are collected in are both pinned.
func everyClauseSubquery() *dbCore.Query {
	return &dbCore.Query{
		Select: &dbCore.SelectClause{
			Fields:     []string{"o.user_id", "COUNT(*)"},
			Distinct:   true,
			DistinctOn: []string{"o.user_id"},
		},
		From: &dbCore.FromClause{IsSubquery: true, Alias: "o", Subquery: boundSubquery("orders", "paid")},
		Joins: []*dbCore.JoinClause{
			{
				Type:      "INNER",
				Table:     "users",
				Condition: &dbCore.BinaryCondition{Left: "users.id", Operator: "=", Right: dbCore.Identifier("o.user_id")},
			},
			{
				Type:       "LEFT",
				IsSubquery: true,
				Alias:      "r",
				Subquery:   boundSubquery("refunds", "open"),
				Condition:  &dbCore.RawCondition{SQL: "r.id = o.id AND r.amount > ?", Args: []any{0}},
			},
			{Type: "CROSS", Table: "regions"},
		},
		Where: &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "users.active", Operator: "=", Right: true},
			&dbCore.IsNullCondition{Field: "o.deleted_at"},
		}},
		GroupBy: &dbCore.GroupByClause{Fields: []string{"o.user_id"}},
		Having: &dbCore.HavingClause{
			Condition: &dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 2},
		},
		OrderBy: &dbCore.OrderByClause{Fields: []dbCore.OrderByField{
			{Field: "o.user_id", Direction: "desc"},
			{Field: "COUNT(*)"},
		}},
		Limit:  intPtr(10),
		Offset: intPtr(20),
	}
}

// TestConditionToSQLMatchesGolden pins what each condition renders through its own ToSQL,
// for every operand kind an identifier slot or a value slot can hold.
func TestConditionToSQLMatchesGolden(t *testing.T) {
	assertMatchesGolden(t, conditionsGolden, conditionsGoldenHeader, conditionCorpus())
}

func conditionCorpus() []goldenEntry {
	var entries []goldenEntry
	entries = append(entries, binaryEntries()...)
	entries = append(entries, unaryEntries()...)
	entries = append(entries, inEntries()...)
	entries = append(entries, betweenEntries()...)
	entries = append(entries, existsEntries()...)
	entries = append(entries, likeEntries()...)
	entries = append(entries, isNullEntries()...)
	entries = append(entries, rawEntries()...)
	entries = append(entries, compositeEntries()...)
	entries = append(entries, subqueryEntries()...)
	entries = append(entries, clauseEntries()...)
	entries = append(entries, windowEntries()...)
	entries = append(entries, aggregateEntries()...)
	return entries
}

// binaryEntries covers the left side (an identifier slot) and the right side (a value
// slot) separately, since the same operand renders differently in each.
func binaryEntries() []goldenEntry {
	left := func(operand any) *dbCore.BinaryCondition {
		return &dbCore.BinaryCondition{Left: operand, Operator: "=", Right: 1}
	}
	right := func(operand any) *dbCore.BinaryCondition {
		return &dbCore.BinaryCondition{Left: "a", Operator: "=", Right: operand}
	}
	op := func(operator string) *dbCore.BinaryCondition {
		return &dbCore.BinaryCondition{Left: "a", Operator: operator, Right: 1}
	}

	return []goldenEntry{
		sqlEntry("binary/left/plain", left("age")),
		sqlEntry("binary/left/dotted", left("users.age")),
		sqlEntry("binary/left/dollar-sign", left("t.col$1")),
		sqlEntry("binary/left/identifier", left(dbCore.Identifier("users.id"))),
		sqlEntry("binary/left/identifier-demoted", left(dbCore.Identifier("a = 1 OR 1=1"))),
		sqlEntry("binary/left/raw", left(dbCore.Raw("lower(email)"))),
		sqlEntry("binary/left/subquery", left(boundSubquery("orders", "paid"))),
		sqlEntry("binary/left/demoted-predicate", left("a = 1 OR 1=1")),
		sqlEntry("binary/left/demoted-digit-leading", left("2024Orders.Id")),
		sqlEntry("binary/left/demoted-bracketed", left("[dbo].[2024Orders]")),
		sqlEntry("binary/left/demoted-double-quoted", left(`"Order"`)),
		sqlEntry("binary/left/demoted-space", left("Order Details")),
		sqlEntry("binary/left/demoted-empty", left("")),
		sqlEntry("binary/left/int", left(7)),
		sqlEntry("binary/left/bool", left(true)),
		sqlEntry("binary/left/nil", left(nil)),

		sqlEntry("binary/right/string", right("alice")),
		sqlEntry("binary/right/string-that-looks-like-a-column", right("b")),
		sqlEntry("binary/right/identifier", right(dbCore.Identifier("orders.user_id"))),
		sqlEntry("binary/right/identifier-demoted", right(dbCore.Identifier("2024Orders.Id"))),
		sqlEntry("binary/right/raw", right(dbCore.Raw("NOW()"))),
		sqlEntry("binary/right/subquery", right(boundSubquery("orders", "paid"))),
		sqlEntry("binary/right/int", right(42)),
		sqlEntry("binary/right/int64", right(int64(42))),
		sqlEntry("binary/right/float", right(1.5)),
		sqlEntry("binary/right/bool-true", right(true)),
		sqlEntry("binary/right/bool-false", right(false)),
		sqlEntry("binary/right/nil", right(nil)),
		sqlEntry("binary/right/slice-is-one-arg", right([]int{1, 2})),
		sqlEntry("binary/right/bytes", right([]byte("x"))),

		sqlEntry("binary/operator/not-equal-bang", op("!=")),
		sqlEntry("binary/operator/not-equal-ansi", op("<>")),
		sqlEntry("binary/operator/less-or-equal", op("<=")),
		sqlEntry("binary/operator/ilike", op("ILIKE")),
		sqlEntry("binary/operator/jsonb-contains", op("@>")),
		sqlEntry("binary/operator/is-distinct-from", op("IS DISTINCT FROM")),

		sqlEntry("binary/subqueries-on-both-sides", &dbCore.BinaryCondition{
			Left:     boundSubquery("orders", "left"),
			Operator: "<",
			Right:    boundSubquery("refunds", "right"),
		}),
	}
}

func unaryEntries() []goldenEntry {
	unary := func(operator string, operand any) *dbCore.UnaryCondition {
		return &dbCore.UnaryCondition{Operator: operator, Operand: operand}
	}
	return []goldenEntry{
		sqlEntry("unary/not-plain", unary("NOT", "active")),
		sqlEntry("unary/not-dotted", unary("NOT", "users.active")),
		sqlEntry("unary/not-identifier", unary("NOT", dbCore.Identifier("users.active"))),
		sqlEntry("unary/not-raw", unary("NOT", dbCore.Raw("(a OR b)"))),
		sqlEntry("unary/exists-subquery", unary("EXISTS", boundSubquery("orders", "paid"))),
		sqlEntry("unary/not-demoted", unary("NOT", "a OR 1=1")),
		sqlEntry("unary/minus-int", unary("-", 5)),
	}
}

func inEntries() []goldenEntry {
	in := func(field any, values ...any) *dbCore.InCondition {
		return &dbCore.InCondition{Field: field, Values: values}
	}
	return []goldenEntry{
		sqlEntry("in/ints", in("id", 1, 2, 3)),
		sqlEntry("in/strings", in("status", "a", "b")),
		sqlEntry("in/single", in("id", 1)),
		sqlEntry("in/nil-value", in("id", nil)),
		sqlEntry("in/mixed-types", in("id", 1, "two", int64(3), true)),
		sqlEntry("in/empty-values", &dbCore.InCondition{Field: "id", Values: []any{}}),
		sqlEntry("in/nil-values", &dbCore.InCondition{Field: "id"}),
		sqlEntry("in/not", &dbCore.InCondition{Field: "id", Values: []any{1, 2}, Not: true}),
		sqlEntry("in/not-empty-values", &dbCore.InCondition{Field: "id", Values: []any{}, Not: true}),
		sqlEntry("in/field-dotted", in("users.id", 1)),
		sqlEntry("in/field-identifier", in(dbCore.Identifier("users.id"), 1)),
		sqlEntry("in/field-raw", in(dbCore.Raw("lower(email)"), "user@example.com")),
		sqlEntry("in/field-demoted", in("2024Orders.Id", 1)),
		sqlEntry("in/field-int", in(7, 1)),
		sqlEntry("in/field-subquery", in(boundSubquery("orders", "paid"), 1, 2)),
		sqlEntry("in/subquery", &dbCore.InCondition{Field: "id", IsSubquery: true, Subquery: plainSubquery()}),
		sqlEntry("in/subquery-not", &dbCore.InCondition{Field: "id", IsSubquery: true, Subquery: plainSubquery(), Not: true}),
		sqlEntry("in/subquery-every-clause", &dbCore.InCondition{Field: "users.id", IsSubquery: true, Subquery: everyClauseSubquery()}),
		sqlEntry("in/subquery-ignores-values", &dbCore.InCondition{
			Field: "id", Values: []any{1, 2}, IsSubquery: true, Subquery: plainSubquery(),
		}),
		sqlEntry("in/subquery-field-and-subquery-args", &dbCore.InCondition{
			Field: boundSubquery("orders", "field"), IsSubquery: true, Subquery: boundSubquery("refunds", "set"),
		}),
	}
}

func betweenEntries() []goldenEntry {
	between := func(field, lower, upper any) *dbCore.BetweenCondition {
		return &dbCore.BetweenCondition{Field: field, Lower: lower, Upper: upper}
	}
	return []goldenEntry{
		sqlEntry("between/ints", between("age", 18, 65)),
		sqlEntry("between/strings-are-bound", between("created_at", "2024-01-01", "2024-12-31")),
		sqlEntry("between/hostile-string-is-bound", between("created_at", "1 OR 1=1 --", "2")),
		sqlEntry("between/not", &dbCore.BetweenCondition{Field: "age", Lower: 18, Upper: 65, Not: true}),
		sqlEntry("between/nil-bounds", between("age", nil, nil)),
		sqlEntry("between/subquery-lower", between("created_at", boundSubquery("events", "first"), 5)),
		sqlEntry("between/subquery-both-bounds", between("created_at", boundSubquery("events", "first"), boundSubquery("events", "last"))),
		sqlEntry("between/field-identifier", between(dbCore.Identifier("t.age"), 1, 2)),
		sqlEntry("between/field-raw", between(dbCore.Raw("EXTRACT(YEAR FROM created_at)"), 2020, 2024)),
		sqlEntry("between/field-demoted", between("a = 1 OR 1=1", 1, 2)),
		sqlEntry("between/field-subquery", between(boundSubquery("orders", "paid"), 1, 2)),
	}
}

func existsEntries() []goldenEntry {
	return []goldenEntry{
		sqlEntry("exists/plain", &dbCore.ExistsCondition{Query: plainSubquery()}),
		sqlEntry("exists/not", &dbCore.ExistsCondition{Query: plainSubquery(), Not: true}),
		sqlEntry("exists/bound", &dbCore.ExistsCondition{Query: boundSubquery("orders", "paid")}),
		sqlEntry("exists/every-clause", &dbCore.ExistsCondition{Query: everyClauseSubquery()}),
	}
}

func likeEntries() []goldenEntry {
	like := func(field, pattern any) *dbCore.LikeCondition {
		return &dbCore.LikeCondition{Field: field, Pattern: pattern}
	}
	escape := func(escape string) *dbCore.LikeCondition {
		return &dbCore.LikeCondition{Field: "name", Pattern: "100!%", Escape: escape}
	}
	return []goldenEntry{
		sqlEntry("like/pattern-string", like("name", "a%")),
		sqlEntry("like/not", &dbCore.LikeCondition{Field: "name", Pattern: "a%", Not: true}),
		sqlEntry("like/escape", escape("!")),
		sqlEntry("like/escape-not", &dbCore.LikeCondition{Field: "name", Pattern: "100!%", Escape: "!", Not: true}),
		sqlEntry("like/escape-backslash", escape(`\`)),
		sqlEntry("like/escape-multi-char-verbatim", escape("!!")),
		sqlEntry("like/escape-quote-verbatim", escape("'")),
		sqlEntry("like/escape-with-subquery-pattern", &dbCore.LikeCondition{
			Field: "name", Pattern: boundSubquery("patterns", "active"), Escape: "!",
		}),
		sqlEntry("like/pattern-subquery", like("name", boundSubquery("patterns", "active"))),
		sqlEntry("like/pattern-int-is-bound", like("code", 5)),
		sqlEntry("like/pattern-nil-is-bound", like("name", nil)),
		sqlEntry("like/pattern-raw-is-bound", like("name", dbCore.Raw("'%' || suffix"))),
		sqlEntry("like/pattern-identifier-is-bound", like("name", dbCore.Identifier("other.name"))),
		sqlEntry("like/field-dotted", like("users.name", "a%")),
		sqlEntry("like/field-identifier", like(dbCore.Identifier("users.name"), "a%")),
		sqlEntry("like/field-raw", like(dbCore.Raw("lower(name)"), "a%")),
		sqlEntry("like/field-demoted", like("name OR 1=1", "a%")),
	}
}

func isNullEntries() []goldenEntry {
	isNull := func(field any) *dbCore.IsNullCondition { return &dbCore.IsNullCondition{Field: field} }
	return []goldenEntry{
		sqlEntry("isnull/plain", isNull("deleted_at")),
		sqlEntry("isnull/not", &dbCore.IsNullCondition{Field: "deleted_at", Not: true}),
		sqlEntry("isnull/dotted", isNull("users.deleted_at")),
		sqlEntry("isnull/identifier", isNull(dbCore.Identifier("users.deleted_at"))),
		sqlEntry("isnull/raw", isNull(dbCore.Raw("NULLIF(name, '')"))),
		sqlEntry("isnull/demoted", isNull("2024Orders.DeletedAt")),
		sqlEntry("isnull/subquery", isNull(boundSubquery("orders", "paid"))),
		sqlEntry("isnull/nil", isNull(nil)),
	}
}

// rawEntries covers the fast path (no "?." anywhere, SQL and Args passed through) and the
// identifier placeholders, with enough args every time. Each RawCondition is fresh, as it had
// to be at v2.4.3, when ToSQL consumed Args on the "?." path.
func rawEntries() []goldenEntry {
	raw := func(sql string, args ...any) *dbCore.RawCondition {
		return &dbCore.RawCondition{SQL: sql, Args: args}
	}
	return []goldenEntry{
		sqlEntry("raw/nil-receiver", (*dbCore.RawCondition)(nil)),
		sqlEntry("raw/fast-path/no-args", &dbCore.RawCondition{SQL: "a = 1"}),
		sqlEntry("raw/fast-path/empty-args", &dbCore.RawCondition{SQL: "a = 1", Args: []any{}}),
		sqlEntry("raw/fast-path/args", raw("a = ? AND b = ?", 1, "two")),
		sqlEntry("raw/fast-path/more-args-than-placeholders", raw("a = ?", 1, 2)),
		sqlEntry("raw/fast-path/fewer-args-than-placeholders", raw("a = ? AND b = ?", 1)),
		sqlEntry("raw/fast-path/ilike-verbatim", raw("email ILIKE ?", "%@example.com")),
		sqlEntry("raw/fast-path/digit-leading-verbatim", raw("[dbo].[2024Orders].[Id] = ?", 1)),
		sqlEntry("raw/fast-path/empty-sql", &dbCore.RawCondition{}),

		sqlEntry("raw/dot-column", raw("?.id = ?", "users", 5)),
		sqlEntry("raw/dot-dot", raw("?.? = ?", "users", "id", 5)),
		sqlEntry("raw/dot-column-equals-dot-dot", raw("?.id = ?.?", "tags", "post_tags", "tag_id")),
		sqlEntry("raw/dot-quoted-column", raw(`?."Id" = ?`, "t", 1)),
		sqlEntry("raw/dot-column-with-digits-and-underscore", raw("?.user_id2 > ?", "t", 1)),
		sqlEntry("raw/dot-column-digit-leading-table", raw("?.Id = ?", "2024Orders", 1)),
		sqlEntry("raw/value-before-dot-column", raw("? < ?.created_at", 5, "events")),
		sqlEntry("raw/several-dot-placeholders", raw("?.a = ?.b AND ?.c = ?", "x", "y", "z", 1)),
		sqlEntry("raw/dot-column-no-value-placeholders", raw("?.id IS NOT NULL", "users")),
		sqlEntry("raw/dot-column-extra-args-appended", raw("?.id = ?", "t", 1, 2)),
		sqlEntry("raw/dot-column-fewer-values-than-placeholders", raw("?.id = ? AND b = ?", "t", 1)),
		sqlEntry("raw/dot-dot-identifier-typed-args", raw("?.? = 1", dbCore.Identifier("users"), dbCore.Identifier("id"))),
		sqlEntry("raw/dot-column-int-table-arg", raw("?.id = 1", 7)),
	}
}

func compositeEntries() []goldenEntry {
	eq := func(field string, value any) dbCore.Condition {
		return &dbCore.BinaryCondition{Left: field, Operator: "=", Right: value}
	}
	composite := func(operator string, conditions ...dbCore.Condition) *dbCore.CompositeCondition {
		return &dbCore.CompositeCondition{Operator: operator, Conditions: conditions}
	}
	return []goldenEntry{
		sqlEntry("composite/nil-receiver", (*dbCore.CompositeCondition)(nil)),
		sqlEntry("composite/empty", composite("AND")),
		sqlEntry("composite/empty-conditions-slice", &dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{}}),
		sqlEntry("composite/single-child-is-bare", composite("AND", eq("a", 1))),
		sqlEntry("composite/single-child-composite", composite("AND", composite("OR", eq("a", 1), eq("b", 2)))),
		sqlEntry("composite/and", composite("AND", eq("a", 1), eq("b", 2))),
		sqlEntry("composite/or", composite("OR", eq("a", 1), eq("b", 2))),
		sqlEntry("composite/operator-verbatim", composite("or", eq("a", 1), eq("b", 2))),
		sqlEntry("composite/nested", composite("AND",
			eq("a", 1),
			composite("OR", eq("b", 2), composite("AND", eq("c", 3), eq("d", 4))),
		)),
		sqlEntry("composite/every-condition-type", composite("AND",
			eq("a", 1),
			&dbCore.UnaryCondition{Operator: "NOT", Operand: "archived"},
			&dbCore.InCondition{Field: "b", Values: []any{2, 3}},
			&dbCore.BetweenCondition{Field: "c", Lower: 4, Upper: 5},
			&dbCore.ExistsCondition{Query: boundSubquery("orders", "paid")},
			&dbCore.LikeCondition{Field: "d", Pattern: "x%", Escape: "!"},
			&dbCore.IsNullCondition{Field: "e", Not: true},
			&dbCore.RawCondition{SQL: "?.f = ?", Args: []any{"t", 6}},
		)),
		sqlEntry("composite/empty-child-not-skipped", composite("AND", eq("a", 1), composite("OR"))),
		sqlEntry("composite/demoted-child-renders", composite("OR", eq("a = 1 OR 1=1", true), eq("b", 2))),
	}
}

// subqueryEntries isolate each branch of buildSubquerySQL, wrapped in EXISTS so the only
// thing rendering is the subquery.
func subqueryEntries() []goldenEntry {
	exists := func(q *dbCore.Query) *dbCore.ExistsCondition { return &dbCore.ExistsCondition{Query: q} }
	selectFrom := func(fields ...string) *dbCore.Query {
		return &dbCore.Query{
			Select: &dbCore.SelectClause{Fields: fields},
			From:   &dbCore.FromClause{Table: "t"},
		}
	}
	with := func(q *dbCore.Query, mutate func(q *dbCore.Query)) *dbCore.Query {
		mutate(q)
		return q
	}

	return []goldenEntry{
		sqlEntry("subquery/select-distinct", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Select.Distinct = true
		}))),
		sqlEntry("subquery/select-distinct-on", exists(with(selectFrom("a", "b"), func(q *dbCore.Query) {
			q.Select.Distinct = true
			q.Select.DistinctOn = []string{"a"}
		}))),
		sqlEntry("subquery/distinct-on-needs-distinct-flag", exists(with(selectFrom("a", "b"), func(q *dbCore.Query) {
			q.Select.DistinctOn = []string{"a"}
		}))),
		sqlEntry("subquery/no-select-clause", exists(&dbCore.Query{From: &dbCore.FromClause{Table: "t"}})),
		sqlEntry("subquery/no-from-clause", exists(&dbCore.Query{Select: &dbCore.SelectClause{Fields: []string{"1"}}})),
		sqlEntry("subquery/from-alias-dropped", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.From.Alias = "x"
		}))),
		sqlEntry("subquery/from-subquery", exists(with(selectFrom("x.id"), func(q *dbCore.Query) {
			q.From = &dbCore.FromClause{IsSubquery: true, Alias: "x", Subquery: boundSubquery("orders", "paid")}
		}))),
		sqlEntry("subquery/from-digit-leading-table-verbatim", exists(with(selectFrom("Id"), func(q *dbCore.Query) {
			q.From.Table = "[dbo].[2024Orders]"
		}))),
		sqlEntry("subquery/join-table", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Joins = []*dbCore.JoinClause{{Type: "INNER", Table: "u", Condition: &dbCore.RawCondition{SQL: "u.id = t.u_id"}}}
		}))),
		sqlEntry("subquery/join-table-alias-dropped", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Joins = []*dbCore.JoinClause{{Type: "INNER", Table: "users", Alias: "u", Condition: &dbCore.RawCondition{SQL: "u.id = t.u_id"}}}
		}))),
		sqlEntry("subquery/join-without-condition", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Joins = []*dbCore.JoinClause{{Type: "CROSS", Table: "u"}, {Type: "NATURAL", Table: "v"}}
		}))),
		sqlEntry("subquery/join-subquery", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Joins = []*dbCore.JoinClause{{
				Type: "LEFT", IsSubquery: true, Alias: "s", Subquery: boundSubquery("orders", "paid"),
				Condition: &dbCore.BinaryCondition{Left: "s.id", Operator: "=", Right: dbCore.Identifier("t.id")},
			}}
		}))),
		sqlEntry("subquery/join-lateral", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Joins = []*dbCore.JoinClause{{
				Type: "LATERAL", IsSubquery: true, IsLateral: true, Alias: "l", Subquery: plainSubquery(),
				Condition: &dbCore.RawCondition{SQL: "true"},
			}}
		}))),
		sqlEntry("subquery/join-dot-placeholders", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Joins = []*dbCore.JoinClause{{
				Type: "INNER", Table: "post_tags",
				Condition: &dbCore.RawCondition{SQL: "?.id = ?.?", Args: []any{"t", "post_tags", "tag_id"}},
			}}
		}))),
		sqlEntry("subquery/where-or", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Where = &dbCore.WhereClause{Operator: "OR", Conditions: []dbCore.Condition{
				&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
				&dbCore.BinaryCondition{Left: "b", Operator: "=", Right: 2},
			}}
		}))),
		sqlEntry("subquery/where-without-conditions", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Where = &dbCore.WhereClause{Operator: "AND"}
		}))),
		sqlEntry("subquery/group-by", exists(with(selectFrom("a", "COUNT(*)"), func(q *dbCore.Query) {
			q.GroupBy = &dbCore.GroupByClause{Fields: []string{"a", "b"}}
		}))),
		sqlEntry("subquery/group-by-modifiers-dropped", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.GroupBy = &dbCore.GroupByClause{
				Fields: []string{"a"}, Rollup: []string{"b"}, Cube: []string{"c"}, Sets: [][]string{{"d"}},
			}
		}))),
		sqlEntry("subquery/group-by-only-modifiers", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.GroupBy = &dbCore.GroupByClause{Rollup: []string{"a"}}
		}))),
		sqlEntry("subquery/having", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Having = &dbCore.HavingClause{Condition: &dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 1}}
		}))),
		sqlEntry("subquery/having-without-condition", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Having = &dbCore.HavingClause{}
		}))),
		sqlEntry("subquery/order-by-verbatim", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.OrderBy = &dbCore.OrderByClause{Fields: []dbCore.OrderByField{
				{Field: "a", Direction: "desc"},
				{Field: "lower(name)", Direction: "sideways"},
				{Field: "b"},
				{Field: "c || d", Raw: true, Direction: "ASC"},
			}}
		}))),
		sqlEntry("subquery/order-by-empty", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.OrderBy = &dbCore.OrderByClause{}
		}))),
		sqlEntry("subquery/limit", exists(with(selectFrom("a"), func(q *dbCore.Query) { q.Limit = intPtr(5) }))),
		sqlEntry("subquery/limit-zero", exists(with(selectFrom("a"), func(q *dbCore.Query) { q.Limit = intPtr(0) }))),
		sqlEntry("subquery/offset-without-limit", exists(with(selectFrom("a"), func(q *dbCore.Query) { q.Offset = intPtr(5) }))),
		sqlEntry("subquery/limit-and-offset", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Limit = intPtr(5)
			q.Offset = intPtr(10)
		}))),
		sqlEntry("subquery/order-by-limit-offset", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.OrderBy = &dbCore.OrderByClause{Fields: []dbCore.OrderByField{{Field: "a", Direction: "DESC"}}}
			q.Limit = intPtr(1)
			q.Offset = intPtr(0)
		}))),
		sqlEntry("subquery/ctes-unions-windows-returning-dropped", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.CTEs = []*dbCore.CTEClause{{Name: "c", Query: plainSubquery()}}
			q.Unions = []*dbCore.UnionClause{{Query: plainSubquery(), All: true}}
			q.Windows = []*dbCore.WindowClause{{Name: "w", Definition: &dbCore.WindowDefinition{PartitionBy: []string{"a"}}}}
			q.Returning = []string{"id"}
		}))),
		sqlEntry("subquery/every-clause", exists(everyClauseSubquery())),
		sqlEntry("subquery/nested-three-deep", exists(with(selectFrom("a"), func(q *dbCore.Query) {
			q.Where = &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
				&dbCore.InCondition{Field: "a", IsSubquery: true, Subquery: with(selectFrom("b"), func(inner *dbCore.Query) {
					inner.Where = &dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
						&dbCore.ExistsCondition{Query: boundSubquery("deepest", "x")},
					}}
					inner.Limit = intPtr(3)
				})},
			}}
		}))),
	}
}

// clauseEntries pin WhereClause.ToSQL and HavingClause.ToSQL. The dialects render
// UPDATE/DELETE WHERE through WhereClause.ToSQL directly, and buildSubquerySQL uses both.
func clauseEntries() []goldenEntry {
	eq := func(field string, value any) dbCore.Condition {
		return &dbCore.BinaryCondition{Left: field, Operator: "=", Right: value}
	}
	where := func(operator string, conditions ...dbCore.Condition) *dbCore.WhereClause {
		return &dbCore.WhereClause{Operator: operator, Conditions: conditions}
	}
	return []goldenEntry{
		sqlEntry("where/empty", &dbCore.WhereClause{Operator: "AND"}),
		sqlEntry("where/single", where("AND", eq("a", 1))),
		sqlEntry("where/and", where("AND", eq("a", 1), eq("b", 2))),
		sqlEntry("where/or", where("OR", eq("a", 1), eq("b", 2))),
		sqlEntry("where/empty-operator", where("", eq("a", 1), eq("b", 2))),
		sqlEntry("where/empty-part-not-skipped", where("AND", eq("a", 1), &dbCore.CompositeCondition{Operator: "OR"})),
		sqlEntry("where/composite-part", where("AND", eq("a", 1), &dbCore.CompositeCondition{
			Operator: "OR", Conditions: []dbCore.Condition{eq("b", 2), eq("c", 3)},
		})),
		sqlEntry("where/demoted-part-renders", where("AND", eq("a = 1 OR 1=1", true))),
		sqlEntry("where/dot-placeholders", where("AND", &dbCore.RawCondition{SQL: "?.id = ?", Args: []any{"users", 1}}, eq("b", 2))),
		sqlEntry("where/subquery-part", where("AND", &dbCore.InCondition{Field: "id", IsSubquery: true, Subquery: boundSubquery("orders", "paid")})),

		sqlEntry("having/without-condition", &dbCore.HavingClause{}),
		sqlEntry("having/raw-aggregate", &dbCore.HavingClause{Condition: &dbCore.BinaryCondition{
			Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 1,
		}}),
		sqlEntry("having/composite", &dbCore.HavingClause{Condition: &dbCore.CompositeCondition{
			Operator: "AND", Conditions: []dbCore.Condition{
				&dbCore.BinaryCondition{Left: dbCore.Raw("SUM(amount)"), Operator: ">=", Right: 100},
				&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: "<", Right: 10},
			},
		}}),
		sqlEntry("having/demoted-left-renders", &dbCore.HavingClause{Condition: &dbCore.BinaryCondition{
			Left: "COUNT(*)", Operator: ">", Right: 1,
		}}),
	}
}

// windowEntries pin both renderings of a window definition. ToSQL is what the dialects'
// FormatWindow uses; String is what Builder.Over inlines into a SELECT field. They already
// disagree about frames (ToSQL has no BETWEEN), and neither validates or quotes anything.
func windowEntries() []goldenEntry {
	definitions := []struct {
		name       string
		definition func() *dbCore.WindowDefinition
	}{
		{"empty", func() *dbCore.WindowDefinition { return &dbCore.WindowDefinition{} }},
		{"partition", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{PartitionBy: []string{"user_id", "region"}}
		}},
		{"partition-non-identifier-verbatim", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{PartitionBy: []string{"lower(name)", "2024Orders.Id"}}
		}},
		{"order", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{OrderBy: []dbCore.OrderByField{
				{Field: "created_at", Direction: "DESC"},
				{Field: "id", Direction: "asc"},
			}}
		}},
		{"order-without-direction", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{OrderBy: []dbCore.OrderByField{{Field: "id"}}}
		}},
		{"order-raw-flag-ignored", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{OrderBy: []dbCore.OrderByField{{Field: "a || b", Direction: "DESC", Raw: true}}}
		}},
		{"frame-unbounded-to-current", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{
				PartitionBy: []string{"user_id"},
				OrderBy:     []dbCore.OrderByField{{Field: "created_at", Direction: "ASC"}},
				Frame: &dbCore.WindowFrame{
					Type:  "ROWS",
					Start: &dbCore.FrameBound{Type: "UNBOUNDED PRECEDING"},
					End:   &dbCore.FrameBound{Type: "CURRENT ROW"},
				},
			}
		}},
		{"frame-with-offsets", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{
				OrderBy: []dbCore.OrderByField{{Field: "id", Direction: "ASC"}},
				Frame: &dbCore.WindowFrame{
					Type:      "RANGE",
					Start:     &dbCore.FrameBound{Type: "PRECEDING", Value: 3},
					End:       &dbCore.FrameBound{Type: "FOLLOWING", Value: "1 day"},
					Exclusion: "EXCLUDE CURRENT ROW",
				},
			}
		}},
		{"frame-start-only", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{Frame: &dbCore.WindowFrame{
				Type: "ROWS", Start: &dbCore.FrameBound{Type: "UNBOUNDED PRECEDING"},
			}}
		}},
		{"frame-end-only", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{Frame: &dbCore.WindowFrame{
				Type: "GROUPS", End: &dbCore.FrameBound{Type: "CURRENT ROW"},
			}}
		}},
		{"frame-type-only", func() *dbCore.WindowDefinition {
			return &dbCore.WindowDefinition{Frame: &dbCore.WindowFrame{Type: "ROWS", Exclusion: "EXCLUDE TIES"}}
		}},
	}

	var entries []goldenEntry
	for _, d := range definitions {
		entries = append(entries,
			sqlEntry("window/tosql/"+d.name, d.definition()),
			textEntry("window/string/"+d.name, d.definition()),
		)
		if frame := d.definition().Frame; frame != nil {
			entries = append(entries,
				sqlEntry("frame/tosql/"+d.name, frame),
				textEntry("frame/string/"+d.name, frame),
			)
		}
	}

	bounds := []struct {
		name  string
		bound *dbCore.FrameBound
	}{
		{"type-only", &dbCore.FrameBound{Type: "CURRENT ROW"}},
		{"int-value", &dbCore.FrameBound{Type: "PRECEDING", Value: 2}},
		{"string-value", &dbCore.FrameBound{Type: "FOLLOWING", Value: "INTERVAL '1 day'"}},
		{"zero-value", &dbCore.FrameBound{Type: "PRECEDING", Value: 0}},
	}
	for _, b := range bounds {
		entries = append(entries,
			sqlEntry("framebound/tosql/"+b.name, b.bound),
			textEntry("framebound/string/"+b.name, b.bound),
		)
	}
	return entries
}

// aggregateEntries pin AggregateFunction.String, including FILTER, which renders its
// condition and discards the condition's args — the placeholder survives, its value does
// not — and OrderByClause.String, which WITHIN GROUP uses.
func aggregateEntries() []goldenEntry {
	return []goldenEntry{
		textEntry("aggregate/count-star", &dbCore.AggregateFunction{Name: "COUNT", Arguments: []string{"*"}}),
		textEntry("aggregate/no-arguments", &dbCore.AggregateFunction{Name: "NOW"}),
		textEntry("aggregate/several-arguments", &dbCore.AggregateFunction{Name: "STRING_AGG", Arguments: []string{"name", "', '"}}),
		textEntry("aggregate/distinct", &dbCore.AggregateFunction{Name: "COUNT", Arguments: []string{"user_id"}, IsDistinct: true}),
		textEntry("aggregate/filter-binary-args-discarded", &dbCore.AggregateFunction{
			Name: "SUM", Arguments: []string{"amount"},
			Filter: &dbCore.BinaryCondition{Left: "status", Operator: "=", Right: "paid"},
		}),
		textEntry("aggregate/filter-raw", &dbCore.AggregateFunction{
			Name: "COUNT", Arguments: []string{"*"},
			Filter: &dbCore.RawCondition{SQL: "amount > 0"},
		}),
		textEntry("aggregate/filter-composite", &dbCore.AggregateFunction{
			Name: "AVG", Arguments: []string{"amount"},
			Filter: &dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{
				&dbCore.IsNullCondition{Field: "deleted_at"},
				&dbCore.InCondition{Field: "region", Values: []any{"eu", "us"}},
			}},
		}),
		textEntry("aggregate/within-group", &dbCore.AggregateFunction{
			Name: "PERCENTILE_CONT", Arguments: []string{"0.5"},
			WithinGroup: &dbCore.OrderByClause{Fields: []dbCore.OrderByField{{Field: "amount", Direction: "DESC"}}},
		}),
		textEntry("aggregate/everything", &dbCore.AggregateFunction{
			Name: "ARRAY_AGG", Arguments: []string{"name"}, IsDistinct: true,
			Filter:      &dbCore.LikeCondition{Field: "name", Pattern: "a%"},
			WithinGroup: &dbCore.OrderByClause{Fields: []dbCore.OrderByField{{Field: "name"}, {Field: "id", Direction: "DESC"}}},
		}),

		textEntry("orderby/string/empty", &dbCore.OrderByClause{}),
		textEntry("orderby/string/fields", &dbCore.OrderByClause{Fields: []dbCore.OrderByField{
			{Field: "a", Direction: "DESC"},
			{Field: "b"},
			{Field: "lower(c)", Direction: "sideways", Raw: true},
		}}),
	}
}
