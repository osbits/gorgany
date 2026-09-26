package builder_test

// The builder corpus rendered through both shipped dialects. Everything the Postgres and
// MySQL engines put on the wire for a builder query — SELECT with every clause, INSERT with
// every conflict form, UPDATE, DELETE — and everything they refuse, since a refusal is as
// much a part of the contract as the SQL it declines to emit.
//
// The per-feature tests in builder_coverage_test.go assert the parts someone thought to
// check. This pins the rest: argument order across clauses, the spacing of an empty WHERE,
// which positions the MySQL ILIKE rewrite reaches, and the places where a subquery renders
// through core rather than through the dialect. A new engine is about to be added behind the
// same seam, and the changes that come with it are meant to be additive; this file is what
// makes "Postgres and MySQL output is unchanged" a fact the build checks rather than a claim.
// testdata/pg_mysql.golden was generated at v2.4.3, whose db/sql renders identically to
// v2.2.1.
//
// Every case builds a fresh query for every dialect, so no condition is rendered twice, and
// OrderByRaw is only ever the last clause call. At v2.4.3 a RawCondition with "?."
// placeholders consumed its Args when rendered, and the builder's clone — which every clause
// call makes — dropped the Raw flag; pinning either would have pinned the defect. Both are
// fixed, and are covered by TestRawConditionToSQLIsIdempotent (db/sql/core) and
// TestCloneKeepsOrderByRaw (builder_coverage_test.go) rather than here.

import (
	"flag"
	"fmt"
	"testing"

	"github.com/osbits/gorgany/v2/db/sql/builder"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	mysql "github.com/osbits/gorgany/v2/db/sql/gorm/mysql/v2"
	postgres "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	"github.com/osbits/gorgany/v2/db/sql/internal/goldentest"
	"github.com/stretchr/testify/require"
)

// update rewrites the golden file instead of comparing against it. It is this package's own
// flag, so -update rewrites this package's golden file and no other; the mechanics are in
// db/sql/internal/goldentest.
var update = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

const dialectGolden = "testdata/pg_mysql.golden"

const dialectGoldenHeader = `# Golden file for TestPostgresAndMySQLOutputMatchesGolden (db/sql/builder/dialect_golden_test.go).
# Each builder case rendered through PostgresDialect and MySQLDialect, and the ON CONFLICT
# cases through MySQLDialect{AllowUnfaithfulUpsert: true} as well.
#
# Regenerate from the module root, then review the diff like any other change:
#   go test ./db/sql/builder -run Golden -update
#
# sql is Go-quoted. args lists each bound value with its dynamic type; "nil" is a nil
# slice and "[]" an empty one. err is a refusal: "unsupported" for a core.UnsupportedError,
# "error" for any other, then the message.
`

// goldenDialects are the dialects every case renders through. upsertOnly marks a variant
// that only changes the outcome of an ON CONFLICT, so it renders only the cases that have
// one rather than doubling the file with entries identical to plain MySQL's.
var goldenDialects = []struct {
	name       string
	dialect    func() dbCore.SQLDialect
	upsertOnly bool
}{
	{name: "postgres", dialect: func() dbCore.SQLDialect { return &postgres.PostgresDialect{} }},
	{name: "mysql", dialect: func() dbCore.SQLDialect { return &mysql.MySQLDialect{} }},
	{
		name:       "mysql+unfaithful-upsert",
		dialect:    func() dbCore.SQLDialect { return &mysql.MySQLDialect{AllowUnfaithfulUpsert: true} },
		upsertOnly: true,
	},
}

// dialectCase is one builder query. build receives an empty builder for the dialect under
// test; subqueries come from builder.NewLike(b) so they speak the same dialect.
type dialectCase struct {
	name   string
	build  func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder
	upsert bool
}

// TestPostgresAndMySQLOutputMatchesGolden pins, per dialect, the SQL and args each builder
// case renders, or the error the dialect refuses it with.
func TestPostgresAndMySQLOutputMatchesGolden(t *testing.T) {
	var entries []goldentest.Entry
	for _, c := range dialectCorpus() {
		for _, d := range goldenDialects {
			if d.upsertOnly && !c.upsert {
				continue
			}
			body := renderDialectCase(c, d.dialect())

			// A second, independent build must render the same. Every build constructs its own
			// query, so a difference means the case hands state from one build to the next, and
			// its entry would pin whichever render happened to come first.
			require.Equalf(t, body, renderDialectCase(c, d.dialect()),
				"%s [%s] shares a condition between builds; construct it inside build", c.name, d.name)

			entries = append(entries, goldentest.Entry{Name: c.name + " [" + d.name + "]", Body: body})
		}
	}
	goldentest.AssertMatches(t, dialectGolden, dialectGoldenHeader, entries, *update)
}

func renderDialectCase(c dialectCase, dialect dbCore.SQLDialect) string {
	sql, args, err := c.build(builder.New(dialect)).ToSQL()
	return encodeGoldenResult(sql, args, err)
}

// encodeGoldenResult records the SQL and args, or the refusal. A refusal says whether it is a
// core.UnsupportedError — the construct is one the engine cannot express, which callers
// branch on with core.IsUnsupported — or any other error. The error's concrete type is not
// recorded: for a wrapped error it is a standard-library internal that may be renamed.
func encodeGoldenResult(sql string, args []any, err error) string {
	if err != nil {
		kind := "error"
		if dbCore.IsUnsupported(err) {
			kind = "unsupported"
		}
		return fmt.Sprintf("err:  %s %q\n", kind, err.Error())
	}
	return fmt.Sprintf("sql:  %q\nargs: %s\n", sql, goldentest.EncodeArgs(args))
}

// goldenDistinct sets SelectClause.Distinct, which no builder method reaches — DistinctOn
// records the ON list and leaves the flag alone — exactly as TestDistinctOnDivergesByDialect
// does. Build returns the builder's own query, so ToSQL renders the change.
func goldenDistinct(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
	b.Build().Select.Distinct = true
	return b
}

// goldenFromAlias sets FromClause.Alias, which From cannot: it takes only a table.
func goldenFromAlias(b dbCore.IQueryBuilder, alias string) dbCore.IQueryBuilder {
	b.Build().From.Alias = alias
	return b
}

// goldenPaidOrders is a subquery binding one arg, so an entry shows where subquery args land
// relative to the outer query's own.
func goldenPaidOrders(b dbCore.IQueryBuilder) *dbCore.Query {
	return builder.NewLike(b).Select("user_id").From("orders").Eq("status", "paid").Build()
}

// goldenEveryClauseSubquery reaches every clause a subquery can carry. Where it sits decides
// who renders it: in an IN or EXISTS it goes through core's dialect-free renderer, and in
// FROM, a JOIN, a CTE, a UNION or INSERT ... SELECT it goes through the dialect. The same
// query in both positions shows the difference — most visibly DISTINCT ON, which MySQL
// refuses in the one position and emits in the other.
func goldenEveryClauseSubquery(b dbCore.IQueryBuilder) *dbCore.Query {
	q := builder.NewLike(b).
		Select("o.user_id", "COUNT(*) AS n").
		DistinctOn("o.user_id").
		From("orders o").
		InnerJoin("users u", &dbCore.BinaryCondition{Left: "u.id", Operator: "=", Right: dbCore.Identifier("o.user_id")}).
		Eq("o.status", "paid").
		GroupBy("o.user_id").
		Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 2}).
		OrderBy("o.user_id", "desc").
		Limit(5).
		Offset(10).
		Build()
	q.Select.Distinct = true
	return q
}

func dialectCorpus() []dialectCase {
	var cases []dialectCase
	cases = append(cases, selectCases()...)
	cases = append(cases, joinCases()...)
	cases = append(cases, whereCases()...)
	cases = append(cases, groupByHavingCases()...)
	cases = append(cases, orderByLimitCases()...)
	cases = append(cases, compoundCases()...)
	cases = append(cases, insertCases()...)
	cases = append(cases, updateDeleteCases()...)
	return cases
}

func selectCases() []dialectCase {
	return []dialectCase{
		{name: "select/star", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.From("users")
		}},
		{name: "select/fields", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id", "name").From("users")
		}},
		{name: "select/fields-appended", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").Select("name", "email").From("users")
		}},
		{name: "select/expressions-verbatim", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("COUNT(*) AS n", "lower(email)", "[dbo].[2024Orders].[Id]").From("users")
		}},
		{name: "select/without-from", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("1")
		}},
		{name: "select/from-replaced", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").From("accounts")
		}},
		{name: "select/from-alias", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return goldenFromAlias(b.Select("u.id").From("users"), "u")
		}},
		{name: "select/from-table-verbatim", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("Id").From("[dbo].[2024Orders]")
		}},
		{name: "select/distinct", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return goldenDistinct(b.Select("status").From("orders"))
		}},
		{name: "select/distinct-on", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return goldenDistinct(b.Select("id", "user_id").From("orders").DistinctOn("user_id"))
		}},
		{name: "select/distinct-on-without-distinct-flag", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id", "user_id").From("orders").DistinctOn("user_id")
		}},
		{name: "select/window-over-inlined", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			w := b.Window("w", &dbCore.WindowDefinition{
				PartitionBy: []string{"user_id"},
				OrderBy:     []dbCore.OrderByField{{Field: "created_at", Direction: "DESC"}},
				Frame: &dbCore.WindowFrame{
					Type:  "ROWS",
					Start: &dbCore.FrameBound{Type: "UNBOUNDED PRECEDING"},
					End:   &dbCore.FrameBound{Type: "CURRENT ROW"},
				},
			})
			return w.Select("id", "ROW_NUMBER() "+w.Over("w")).From("orders")
		}},
		{name: "select/window-clause-not-rendered", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("orders").Window("w", &dbCore.WindowDefinition{PartitionBy: []string{"user_id"}})
		}},
		{name: "select/returning", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Returning("id")
		}},
		{name: "select/every-clause", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return goldenDistinct(b.
				WithCTE("paid", goldenPaidOrders(b)).
				Select("u.id", "u.name", "COUNT(p.user_id) AS orders").
				From("users u").
				LeftJoin("paid p", &dbCore.BinaryCondition{Left: "p.user_id", Operator: "=", Right: dbCore.Identifier("u.id")}).
				Gte("u.created_at", "2024-01-01").
				In("u.region", "eu", "us").
				GroupBy("u.id", "u.name").
				Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(p.user_id)"), Operator: ">", Right: 1}).
				OrderBy("u.name", "asc").
				Limit(20).
				Offset(40).
				UnionAll(builder.NewLike(b).Select("id", "name", "0").From("archived_users").Eq("active", false).Build()))
		}},
	}
}

func joinCases() []dialectCase {
	on := func() dbCore.Condition {
		return &dbCore.BinaryCondition{Left: "users.id", Operator: "=", Right: dbCore.Identifier("orders.user_id")}
	}
	// join takes a constructor rather than a clause so each dialect renders its own copy.
	join := func(clause func() *dbCore.JoinClause) func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
		return func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").Join(clause())
		}
	}
	return []dialectCase{
		{name: "join/inner", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").InnerJoin("orders", on())
		}},
		{name: "join/left", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").LeftJoin("orders", on())
		}},
		{name: "join/right", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").RightJoin("orders", on())
		}},
		{name: "join/full", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").FullJoin("orders", on())
		}},
		{name: "join/full-outer", build: join(func() *dbCore.JoinClause {
			return &dbCore.JoinClause{Type: "FULL OUTER", Table: "orders", Condition: on()}
		})},
		{name: "join/left-outer", build: join(func() *dbCore.JoinClause {
			return &dbCore.JoinClause{Type: "LEFT OUTER", Table: "orders", Condition: on()}
		})},
		{name: "join/cross", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").CrossJoin("regions")
		}},
		{name: "join/natural", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").NaturalJoin("profiles")
		}},
		{name: "join/table-alias", build: join(func() *dbCore.JoinClause {
			return &dbCore.JoinClause{
				Type: "INNER", Table: "orders", Alias: "o",
				Condition: &dbCore.BinaryCondition{Left: "o.user_id", Operator: "=", Right: dbCore.Identifier("users.id")},
			}
		})},
		{name: "join/raw-condition", build: join(func() *dbCore.JoinClause {
			return &dbCore.JoinClause{
				Type: "INNER", Table: "orders", Condition: &dbCore.RawCondition{SQL: "orders.user_id = users.id AND orders.total > ?", Args: []any{0}},
			}
		})},
		{name: "join/dot-placeholders", build: join(func() *dbCore.JoinClause {
			return &dbCore.JoinClause{
				Type: "INNER", Table: "post_tags",
				Condition: &dbCore.RawCondition{SQL: "?.id = ?.?", Args: []any{"users", "post_tags", "user_id"}},
			}
		})},
		{name: "join/condition-not-validated", build: join(func() *dbCore.JoinClause {
			return &dbCore.JoinClause{
				Type: "INNER", Table: "orders",
				Condition: &dbCore.BinaryCondition{Left: "a = 1 OR 1=1", Operator: "=", Right: true},
			}
		})},
		{name: "join/condition-ilike-not-rewritten", build: join(func() *dbCore.JoinClause {
			return &dbCore.JoinClause{
				Type: "INNER", Table: "orders",
				Condition: &dbCore.BinaryCondition{Left: "orders.note", Operator: "ILIKE", Right: "%gift%"},
			}
		})},
		{name: "join/subquery", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").Join(&dbCore.JoinClause{
				Type: "INNER", IsSubquery: true, Alias: "p", Subquery: goldenPaidOrders(b),
				Condition: &dbCore.BinaryCondition{Left: "p.user_id", Operator: "=", Right: dbCore.Identifier("users.id")},
			})
		}},
		{name: "join/subquery-every-clause", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").Join(&dbCore.JoinClause{
				Type: "LEFT", IsSubquery: true, Alias: "s", Subquery: goldenEveryClauseSubquery(b),
				Condition: &dbCore.RawCondition{SQL: "s.user_id = users.id"},
			})
		}},
		{name: "join/lateral-trivial-on", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			latest := builder.NewLike(b).Select("o.total").From("orders o").
				Where(&dbCore.BinaryCondition{Left: "o.user_id", Operator: "=", Right: dbCore.Identifier("u.id")}).
				OrderBy("o.total", "desc").Limit(1).Build()
			return b.Select("u.id", "latest.total").From("users u").LateralJoin(latest, "latest", &dbCore.RawCondition{SQL: "true"})
		}},
		{name: "join/lateral-real-on", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("u.id").From("users u").LateralJoin(goldenPaidOrders(b), "p",
				&dbCore.BinaryCondition{Left: "p.user_id", Operator: "=", Right: dbCore.Identifier("u.id")})
		}},
		{name: "join/several", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("users.id").From("users").
				InnerJoin("orders", on()).
				LeftJoin("refunds", &dbCore.RawCondition{SQL: "refunds.order_id = orders.id AND refunds.amount > ?", Args: []any{10}}).
				CrossJoin("regions").
				Eq("users.active", true)
		}},
	}
}

func whereCases() []dialectCase {
	where := func(apply func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder) func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
		return func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return apply(b.Select("id").From("users")) }
	}
	return []dialectCase{
		{name: "where/eq", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Eq("id", 1) })},
		{name: "where/neq", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Neq("id", 1) })},
		{name: "where/gt", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Gt("age", 18) })},
		{name: "where/gte", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Gte("age", 18) })},
		{name: "where/lt", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Lt("age", 65) })},
		{name: "where/lte", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Lte("age", 65) })},
		{name: "where/in", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.In("status", "a", "b") })},
		{name: "where/in-empty", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.In("id") })},
		{name: "where/in-empty-slice-spread", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.In("id", []any{}...)
		})},
		{name: "where/in-slice-is-one-value", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.In("id", []int{1, 2})
		})},
		{name: "where/not-in", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.NotIn("status", "a") })},
		{name: "where/not-in-empty", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.NotIn("id") })},
		{name: "where/in-subquery", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.InSubquery("id", goldenPaidOrders(b))
		})},
		{name: "where/in-subquery-every-clause", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.InSubquery("id", goldenEveryClauseSubquery(b))
		})},
		{name: "where/in-subquery-without-select", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.InSubquery("id", builder.NewLike(b).From("orders").Build())
		})},
		{name: "where/not-in-subquery", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.NotInSubquery("id", goldenPaidOrders(b))
		})},
		{name: "where/between", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Between("age", 18, 65) })},
		{name: "where/between-subquery-bounds", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Between("created_at",
				builder.NewLike(b).Select("MIN(created_at)").From("events").Eq("kind", "start").Build(),
				builder.NewLike(b).Select("MAX(created_at)").From("events").Eq("kind", "end").Build())
		})},
		{name: "where/not-between", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.NotBetween("age", 18, 65) })},
		{name: "where/exists", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Exists(goldenPaidOrders(b))
		})},
		{name: "where/exists-every-clause", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Exists(goldenEveryClauseSubquery(b))
		})},
		{name: "where/not-exists", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.NotExists(goldenPaidOrders(b))
		})},
		{name: "where/like", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Like("name", "%a%") })},
		{name: "where/not-like", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.NotLike("name", "%a%") })},
		{name: "where/like-escape", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.LikeEscape("name", "100!%", "!")
		})},
		{name: "where/not-like-escape", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.NotLikeEscape("name", "100!%", "!")
		})},
		{name: "where/like-escape-multi-char", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.LikeEscape("name", "100!!%", "!!")
		})},
		{name: "where/is-null", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.IsNull("deleted_at") })},
		{name: "where/is-not-null", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.IsNotNull("deleted_at") })},
		{name: "where/eq-nil", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Eq("deleted_at", nil) })},
		{name: "where/eq-bool", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder { return b.Eq("active", true) })},
		{name: "where/eq-raw-left", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Eq(dbCore.Raw("lower(email)"), "user@example.com")
		})},
		{name: "where/eq-identifier-right", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Eq("updated_at", dbCore.Identifier("created_at"))
		})},
		{name: "where/eq-subquery-right", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Eq("id", builder.NewLike(b).Select("user_id").From("orders").OrderBy("total", "desc").Limit(1).Build())
		})},
		{name: "where/every-helper-chained", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Eq("a", 1).Neq("b", 2).Gt("c", 3).Gte("d", 4).Lt("e", 5).Lte("f", 6).
				In("g", 7, 8).NotIn("h", 9).InSubquery("i", goldenPaidOrders(b)).
				Between("j", 10, 11).NotBetween("k", 12, 13).NotExists(goldenPaidOrders(b)).
				Like("l", "m%").NotLikeEscape("n", "o!%", "!").IsNull("p").IsNotNull("q")
		})},
		{name: "where/composite-or", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Eq("active", true).Where(&dbCore.CompositeCondition{Operator: "OR", Conditions: []dbCore.Condition{
				&dbCore.BinaryCondition{Left: "role", Operator: "=", Right: "admin"},
				&dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{
					&dbCore.BinaryCondition{Left: "role", Operator: "=", Right: "editor"},
					&dbCore.IsNullCondition{Field: "suspended_at"},
				}},
			}})
		})},
		{name: "where/composite-empty", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Where(&dbCore.CompositeCondition{Operator: "AND"})
		})},
		{name: "where/composite-empty-beside-condition", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Eq("id", 1).Where(&dbCore.CompositeCondition{Operator: "AND"})
		})},
		{name: "where/raw-fast-path", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Where(&dbCore.RawCondition{SQL: "age > ? AND age < ?", Args: []any{18, 65}})
		})},
		{name: "where/raw-dot-placeholders", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Where(&dbCore.RawCondition{SQL: "?.id = ?", Args: []any{"users", 5}})
		})},
		{name: "where/ilike-operator-rewritten", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Where(&dbCore.BinaryCondition{Left: "name", Operator: "ILIKE", Right: "a%"})
		})},
		{name: "where/ilike-raw-rewritten", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Where(&dbCore.RawCondition{SQL: "email ilike ?", Args: []any{"%@example.com"}})
		})},
		{name: "where/ilike-inside-literal-rewritten", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Where(&dbCore.RawCondition{SQL: "note = 'ILIKE' OR note ILIKE ?", Args: []any{"x"}})
		})},
		{name: "where/refuses-demoted-field", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Eq("a = 1 OR 1=1", true)
		})},
		{name: "where/refuses-digit-leading-field", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Eq("2024Orders.Id", 1)
		})},
		{name: "where/refuses-demoted-field-in-composite", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Where(&dbCore.CompositeCondition{Operator: "OR", Conditions: []dbCore.Condition{
				&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
				&dbCore.InCondition{Field: "b OR 1=1", Values: []any{2}},
			}})
		})},
		{name: "where/refuses-demoted-identifier", build: where(func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.IsNull(dbCore.Identifier("[dbo].[2024Orders]"))
		})},
	}
}

func groupByHavingCases() []dialectCase {
	return []dialectCase{
		{name: "groupby/fields", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("user_id", "COUNT(*)").From("orders").GroupBy("user_id", "status")
		}},
		{name: "groupby/rollup", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("a", "b").From("t").Rollup("a", "b")
		}},
		{name: "groupby/fields-and-rollup", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("a", "b", "c").From("t").GroupBy("a").Rollup("b", "c")
		}},
		{name: "groupby/cube", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("a", "b").From("t").Cube("a", "b")
		}},
		{name: "groupby/grouping-sets", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("a", "b").From("t").GroupingSets([]string{"a"}, []string{"a", "b"}, []string{})
		}},
		{name: "groupby/every-modifier", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("a").From("t").GroupBy("a").Rollup("b").Cube("c").GroupingSets([]string{"d"})
		}},
		{name: "having/raw-aggregate", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("user_id").From("orders").GroupBy("user_id").
				Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 5})
		}},
		{name: "having/replaced", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("user_id").From("orders").GroupBy("user_id").
				Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 5}).
				Having(&dbCore.BinaryCondition{Left: dbCore.Raw("SUM(total)"), Operator: ">", Right: 100})
		}},
		{name: "having/composite-with-where-args", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("user_id").From("orders").Eq("status", "paid").GroupBy("user_id").
				Having(&dbCore.CompositeCondition{Operator: "AND", Conditions: []dbCore.Condition{
					&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 1},
					&dbCore.BinaryCondition{Left: dbCore.Raw("MAX(total)"), Operator: "<", Right: 1000},
				}})
		}},
		{name: "having/ilike-rewritten", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("name").From("users").GroupBy("name").
				Having(&dbCore.BinaryCondition{Left: "name", Operator: "ILIKE", Right: "a%"})
		}},
		{name: "having/refuses-demoted-field", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("user_id").From("orders").GroupBy("user_id").
				Having(&dbCore.BinaryCondition{Left: "COUNT(*)", Operator: ">", Right: 5})
		}},
	}
}

// orderByLimitCases keeps OrderByRaw as the last clause call of any builder chain, because at
// v2.4.3 every clause call cloned and the clone lost the Raw flag. TestCloneKeepsOrderByRaw
// covers the fix.
func orderByLimitCases() []dialectCase {
	return []dialectCase{
		{name: "orderby/identifier", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("created_at", "desc")
		}},
		{name: "orderby/dotted", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("users.created_at", "ASC")
		}},
		{name: "orderby/direction-empty-is-asc", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("id", "")
		}},
		{name: "orderby/direction-whitelisted", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("id", "sideways; DROP TABLE users")
		}},
		{name: "orderby/non-identifier-bound", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("CASE WHEN 1=1 THEN id END", "desc")
		}},
		{name: "orderby/digit-leading-bound", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("Id").From("users").OrderBy("2024Orders.Id", "asc")
		}},
		{name: "orderby/several", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("last_name", "asc").OrderBy("lower(first_name)", "desc").OrderBy("id", "desc")
		}},
		{name: "orderby/args-after-where-and-having", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("user_id").From("orders").Eq("status", "paid").GroupBy("user_id").
				Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 1}).
				OrderBy("lower(user_id)", "asc").Limit(3)
		}},
		{name: "orderby/raw-last", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("last_name", "asc").OrderByRaw("first_name || ' ' || last_name", "desc")
		}},
		{name: "orderby/raw-direction-whitelisted", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderByRaw("LENGTH(name)", "")
		}},
		{name: "orderby/raw-after-limit-and-offset", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Limit(5).Offset(10).OrderByRaw("RANDOM()", "asc")
		}},
		{name: "limit/only", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Limit(10)
		}},
		{name: "limit/zero", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Limit(0)
		}},
		{name: "limit/and-offset", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("id", "asc").Limit(10).Offset(20)
		}},
		{name: "limit/offset-without-limit", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Offset(20)
		}},
		{name: "limit/offset-zero", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Limit(10).Offset(0)
		}},
		{name: "limit/replaced", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Limit(10).Limit(5).Offset(1).Offset(2)
		}},
	}
}

// compoundCases cover the positions where one query holds another: CTE, UNION, a derived
// table in FROM.
func compoundCases() []dialectCase {
	return []dialectCase{
		{name: "cte/single", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.WithCTE("paid", goldenPaidOrders(b)).Select("user_id").From("paid").Gt("user_id", 100)
		}},
		{name: "cte/several", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.
				WithCTE("paid", goldenPaidOrders(b)).
				WithCTE("big", builder.NewLike(b).Select("user_id").From("orders").Gt("total", 1000).Build()).
				Select("paid.user_id").From("paid").InnerJoin("big", &dbCore.RawCondition{SQL: "big.user_id = paid.user_id"})
		}},
		{name: "cte/every-clause", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.WithCTE("s", goldenEveryClauseSubquery(b)).Select("*").From("s")
		}},
		{name: "union/distinct", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Union(builder.NewLike(b).Select("id").From("archived_users").Build())
		}},
		{name: "union/all", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").UnionAll(builder.NewLike(b).Select("id").From("archived_users").Build())
		}},
		{name: "union/several-with-args", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").Eq("region", "eu").
				Union(builder.NewLike(b).Select("id").From("archived_users").Eq("region", "us").Build()).
				UnionAll(builder.NewLike(b).Select("id").From("pending_users").Eq("region", "apac").Build())
		}},
		{name: "union/order-and-limit-precede-union", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("id").From("users").OrderBy("id", "desc").Limit(5).
				Union(builder.NewLike(b).Select("id").From("archived_users").OrderBy("id", "asc").Limit(3).Build())
		}},
		{name: "union/arm-every-clause", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("user_id", "0").From("users").Union(goldenEveryClauseSubquery(b))
		}},
		{name: "from-subquery/alias", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("p.user_id").Subquery(goldenPaidOrders(b), "p").Gt("p.user_id", 10)
		}},
		{name: "from-subquery/without-alias", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("user_id").Subquery(goldenPaidOrders(b), "")
		}},
		{name: "from-subquery/every-clause", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Select("s.user_id").Subquery(goldenEveryClauseSubquery(b), "s")
		}},
		{name: "from-subquery/nested", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			inner := builder.NewLike(b).Select("p.user_id").Subquery(goldenPaidOrders(b), "p").Limit(10).Build()
			return b.Select("q.user_id").Subquery(inner, "q").OrderBy("q.user_id", "asc")
		}},
	}
}

// insertCases mark every ON CONFLICT form as upsert, since AllowUnfaithfulUpsert decides
// MySQL's answer to DO UPDATE and can leave the others alone only if nothing regresses.
func insertCases() []dialectCase {
	return []dialectCase{
		{name: "insert/single-row", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("name", "email").Values("Ann", "user@example.com")
		}},
		{name: "insert/multi-row", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("name", "email").
				Values("Ann", "ann@example.com").Values("Bob", "bob@example.com").Values("Cy", "cy@example.com")
		}},
		{name: "insert/without-columns", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Values(1, "Ann")
		}},
		{name: "insert/value-types", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("t").Columns("a", "b", "c", "d", "e").Values(nil, true, int64(7), 1.5, []byte("x"))
		}},
		{name: "insert/without-values", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("name")
		}},
		{name: "insert/table-verbatim", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("[dbo].[2024Orders]").Columns("[Id]").Values(1)
		}},
		{name: "insert/from-select", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("archive").Columns("user_id").FromSelect(goldenPaidOrders(b))
		}},
		{name: "insert/from-select-wins-over-values", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("archive").Columns("user_id").Values(1).FromSelect(goldenPaidOrders(b))
		}},
		{name: "insert/from-select-with-cte", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			source := builder.NewLike(b).WithCTE("paid", goldenPaidOrders(b)).Select("user_id").From("paid").Build()
			return b.Insert("archive").Columns("user_id").FromSelect(source)
		}},
		{name: "insert/from-select-every-clause", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("archive").Columns("user_id", "n").FromSelect(goldenEveryClauseSubquery(b))
		}},
		{name: "insert/returning", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("name").Values("Ann").Returning("id", "created_at")
		}},
		{name: "insert/on-conflict-do-nothing", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("email", "name").Values("user@example.com", "Ann").OnConflict("email").DoNothing()
		}},
		{name: "insert/on-conflict-do-nothing-without-target", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("email").Values("user@example.com").OnConflict().DoNothing()
		}},
		{name: "insert/on-conflict-do-nothing-without-columns", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Values("user@example.com").OnConflict("email").DoNothing()
		}},
		{name: "insert/on-conflict-do-nothing-nothing-to-assign", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Values("user@example.com").OnConflict().DoNothing()
		}},
		{name: "insert/on-conflict-do-nothing-multi-row", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("tags").Columns("name").Values("a").Values("b").OnConflict("name").DoNothing()
		}},
		{name: "insert/on-conflict-do-update", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("email", "name").Values("user@example.com", "Ann").
				OnConflict("email").DoUpdate(map[string]interface{}{"name": "Ann", "updated_at": dbCore.Raw("NOW()"), "visits": 2})
		}},
		{name: "insert/on-conflict-do-update-several-targets", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("memberships").Columns("user_id", "group_id", "role").Values(1, 2, "member").
				OnConflict("user_id", "group_id").DoUpdate(map[string]interface{}{"role": "member"})
		}},
		{name: "insert/on-conflict-do-update-empty-set", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("email").Values("user@example.com").OnConflict("email").DoUpdate(map[string]interface{}{})
		}},
		{name: "insert/on-conflict-without-action", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("email").Values("user@example.com").OnConflict("email")
		}},
		{name: "insert/on-conflict-do-update-returning", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("users").Columns("email", "name").Values("user@example.com", "Ann").
				OnConflict("email").DoUpdate(map[string]interface{}{"name": "Ann"}).Returning("id")
		}},
		{name: "insert/from-select-on-conflict-do-nothing", upsert: true, build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Insert("archive").Columns("user_id").FromSelect(goldenPaidOrders(b)).OnConflict("user_id").DoNothing()
		}},
	}
}

func updateDeleteCases() []dialectCase {
	return []dialectCase{
		{name: "update/set-with-where", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("name", "Ann").Eq("id", 1)
		}},
		{name: "update/set-map-sorted", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").SetMap(map[string]interface{}{"name": "Ann", "email": "user@example.com", "active": true}).Eq("id", 1)
		}},
		{name: "update/set-overwritten", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("name", "Ann").Set("name", "Bob")
		}},
		{name: "update/set-raw-and-nil", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("updated_at", dbCore.Raw("NOW()")).Set("deleted_at", nil)
		}},
		{name: "update/without-where", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("active", false)
		}},
		{name: "update/without-set", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Eq("id", 1)
		}},
		{name: "update/where-every-helper", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("flag", 1).
				Eq("a", 1).In("b", 2, 3).InSubquery("c", goldenPaidOrders(b)).Between("d", 4, 5).
				LikeEscape("e", "f!%", "!").IsNotNull("g").Exists(goldenPaidOrders(b))
		}},
		{name: "update/where-composite-or", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("active", false).Where(&dbCore.CompositeCondition{Operator: "OR", Conditions: []dbCore.Condition{
				&dbCore.IsNullCondition{Field: "email"},
				&dbCore.BinaryCondition{Left: "banned", Operator: "=", Right: true},
			}})
		}},
		{name: "update/where-composite-empty", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("active", false).Where(&dbCore.CompositeCondition{Operator: "AND"})
		}},
		{name: "update/where-dot-placeholders", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("active", false).Where(&dbCore.RawCondition{SQL: "?.id = ?", Args: []any{"users", 7}})
		}},
		{name: "update/where-not-validated", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("active", false).Eq("a = 1 OR 1=1", true)
		}},
		{name: "update/where-ilike-rewritten", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("active", false).Where(&dbCore.BinaryCondition{Left: "email", Operator: "ILIKE", Right: "%@example.com"})
		}},
		{name: "update/limit-and-order-ignored", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("active", false).Eq("region", "eu").OrderBy("id", "asc").Limit(10)
		}},
		{name: "update/join-ignored", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("active", false).InnerJoin("orders", &dbCore.RawCondition{SQL: "orders.user_id = users.id"})
		}},
		{name: "update/returning", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("users").Set("name", "Ann").Eq("id", 1).Returning("id", "name")
		}},
		{name: "update/table-verbatim", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Update("[dbo].[2024Orders]").Set("[Status]", "done").Where(&dbCore.RawCondition{SQL: "[Id] = ?", Args: []any{1}})
		}},
		{name: "delete/with-where", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("users").Eq("id", 1)
		}},
		{name: "delete/without-where", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("sessions")
		}},
		{name: "delete/where-in-subquery", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("users").InSubquery("id", goldenPaidOrders(b)).Lt("created_at", "2020-01-01")
		}},
		{name: "delete/where-not-validated", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("users").Eq("a = 1 OR 1=1", true)
		}},
		{name: "delete/where-ilike-rewritten", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("users").Where(&dbCore.RawCondition{SQL: "email ILIKE ?", Args: []any{"%@example.com"}})
		}},
		{name: "delete/limit-and-order-ignored", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("sessions").Lt("expires_at", "2024-01-01").OrderBy("expires_at", "asc").Limit(100)
		}},
		{name: "delete/returning", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("users").Eq("id", 1).Returning("id")
		}},
		{name: "statement/insert-wins-over-update-and-delete", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("users").Update("users").Set("a", 1).Insert("users").Columns("a").Values(2)
		}},
		{name: "statement/update-wins-over-delete", build: func(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
			return b.Delete("users").Update("users").Set("a", 1).Eq("id", 3)
		}},
	}
}
