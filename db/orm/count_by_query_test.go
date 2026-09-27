package orm

import (
	"context"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	mysqlv2 "github.com/osbits/gorgany/v2/db/sql/gorm/mysql/v2"
	v2 "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CountByQuery is what a paginated list asks for its total. It used to take the caller's
// builder and call Select("COUNT(*)").From(table) on it, and each half was wrong:
//   - ORDER BY, LIMIT and OFFSET stayed, so the "total" of page three was at most the page
//     size, and on SQL Server the ORDER BY alone is an error (Msg 8127);
//   - Select appends, so a builder that had selected columns counted with
//     `SELECT a, b, COUNT(*)`;
//   - From replaces the whole FROM clause, so a caller's own FROM — another table, a
//     subquery — was silently swapped for the model's table;
//   - a DISTINCT or UNION query, and a grouped one with a select list, had COUNT(*) appended
//     to that list, so it failed or counted per row.

// countingSession returns a mock session whose CountRaw records the SQL and args it is
// handed, and a pointer to what it recorded.
func countingSession(dialect func() dbCore.IQueryBuilder) (*MockDataSource, *capturedCount) {
	mockDS := NewMockDataSource()
	mockDS.session.queryFn = dialect
	captured := &capturedCount{}
	mockDS.session.executor.countRawFunc = func(_ context.Context, sql string, args ...interface{}) (int64, error) {
		captured.calls++
		captured.sql = sql
		captured.args = args
		return 42, nil
	}
	return mockDS, captured
}

type capturedCount struct {
	calls int
	sql   string
	args  []any
}

func pgBuilder() dbCore.IQueryBuilder    { return v2.NewBuilder() }
func mysqlBuilder() dbCore.IQueryBuilder { return mysqlv2.NewBuilder() }

// countEngines runs a case once per shipped dialect, since the count is rendered through
// the builder's own dialect.
var countEngines = []struct {
	name    string
	builder func() dbCore.IQueryBuilder
}{
	{"postgres", pgBuilder},
	{"mysql", mysqlBuilder},
}

// TestCountByQueryDropsOrderByLimitAndOffset, and leaves the caller's builder as it was:
// the count is derived from a copy, so the same builder still renders the page.
func TestCountByQueryDropsOrderByLimitAndOffset(t *testing.T) {
	for _, engine := range countEngines {
		t.Run(engine.name, func(t *testing.T) {
			mockDS, captured := countingSession(engine.builder)
			qb := mockDS.session.Query().
				Where(&dbCore.BinaryCondition{Left: "age", Operator: ">", Right: 18}).
				OrderBy("name", "ASC").
				Limit(10).
				Offset(20)
			pageBefore, _, err := qb.ToSQL()
			require.NoError(t, err)

			total, err := New[*TestEntity](mockDS.session).CountByQuery(qb)

			require.NoError(t, err)
			assert.Equal(t, int64(42), total)
			assert.Equal(t, "SELECT COUNT(*) FROM test_entities WHERE age > ?", captured.sql)
			assert.Equal(t, []any{18}, captured.args)

			pageAfter, _, err := qb.ToSQL()
			require.NoError(t, err)
			assert.Equal(t, pageBefore, pageAfter, "the caller's builder must not be changed")
		})
	}
}

// TestCountByQueryReplacesSelectList pins that the select list is replaced by COUNT(*), not
// appended to: Select appends, so the old count of a builder that had selected id and name
// sent `SELECT id, name, COUNT(*)`, a grouping error on Postgres and a row per id elsewhere.
func TestCountByQueryReplacesSelectList(t *testing.T) {
	for _, engine := range countEngines {
		t.Run(engine.name, func(t *testing.T) {
			mockDS, captured := countingSession(engine.builder)
			qb := mockDS.session.Query().Select("id", "name").
				Where(&dbCore.BinaryCondition{Left: "age", Operator: ">", Right: 18})

			_, err := New[*TestEntity](mockDS.session).CountByQuery(qb)

			require.NoError(t, err)
			assert.Equal(t, "SELECT COUNT(*) FROM test_entities WHERE age > ?", captured.sql)
		})
	}
}

// TestCountByQueryKeepsTheCallersFrom. The model's table is the default, used only when the
// builder names no FROM of its own.
func TestCountByQueryKeepsTheCallersFrom(t *testing.T) {
	for _, engine := range countEngines {
		t.Run(engine.name, func(t *testing.T) {
			t.Run("table", func(t *testing.T) {
				mockDS, captured := countingSession(engine.builder)
				qb := mockDS.session.Query().From("archived_entities")

				_, err := New[*TestEntity](mockDS.session).CountByQuery(qb)

				require.NoError(t, err)
				assert.Equal(t, "SELECT COUNT(*) FROM archived_entities", captured.sql)
			})

			t.Run("subquery", func(t *testing.T) {
				mockDS, captured := countingSession(engine.builder)
				recent := mockDS.session.Query().From("test_entities").
					Where(&dbCore.BinaryCondition{Left: "age", Operator: "<", Right: 30}).
					Build()
				qb := mockDS.session.Query().Subquery(recent, "recent")

				_, err := New[*TestEntity](mockDS.session).CountByQuery(qb)

				require.NoError(t, err)
				assert.Equal(t,
					"SELECT COUNT(*) FROM (SELECT * FROM test_entities WHERE age < ?) AS recent",
					captured.sql)
				assert.Equal(t, []any{30}, captured.args)
			})
		})
	}
}

// TestCountByQueryWrapsDistinctGroupByAndUnions. Each of these returns one row per group,
// per distinct row or per row of the union, so it is counted as a derived table rather
// than by replacing its select list. A common table expression moves to the outer query.
func TestCountByQueryWrapsDistinctGroupByAndUnions(t *testing.T) {
	cases := []struct {
		name  string
		build func(dbCore.IQueryBuilder) dbCore.IQueryBuilder
		want  string
		args  []any
	}{
		{
			name: "group by",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("age", "COUNT(*)").GroupBy("age").OrderBy("age", "DESC").Limit(5)
			},
			want: "SELECT COUNT(*) FROM (SELECT 1 AS gorgany_one FROM test_entities GROUP BY age) AS gorgany_count",
		},
		{
			// Named columns are kept: a HAVING may name one by its alias on MySQL.
			name: "group by with named columns and a having",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("age", "COUNT(*) AS n").GroupBy("age").
					Having(&dbCore.RawCondition{SQL: "COUNT(*) > ?", Args: []any{1}})
			},
			want: "SELECT COUNT(*) FROM (SELECT age, COUNT(*) AS n FROM test_entities GROUP BY age HAVING COUNT(*) > ?) AS gorgany_count",
			args: []any{1},
		},
		{
			// No select list renders as a star, which Postgres and ONLY_FULL_GROUP_BY MySQL
			// refuse beside a GROUP BY. Keeping it made this count fail where it used to work.
			name: "group by with a having and no select list",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.GroupBy("name").Having(&dbCore.RawCondition{SQL: "COUNT(*) > ?", Args: []any{1}})
			},
			want: "SELECT COUNT(*) FROM (SELECT 1 AS gorgany_one FROM test_entities GROUP BY name HAVING COUNT(*) > ?) AS gorgany_count",
			args: []any{1},
		},
		{
			name: "group by with a having and a star",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("test_entities.*").GroupBy("name").
					Having(&dbCore.RawCondition{SQL: "COUNT(*) > ?", Args: []any{1}})
			},
			want: "SELECT COUNT(*) FROM (SELECT 1 AS gorgany_one FROM test_entities GROUP BY name HAVING COUNT(*) > ?) AS gorgany_count",
			args: []any{1},
		},
		{
			// The GROUP BY names a select-list alias, so the list has to stay beside it.
			name: "group by a select-list alias",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("LOWER(name) AS lname", "COUNT(*) AS n").GroupBy("lname")
			},
			want: "SELECT COUNT(*) FROM (SELECT LOWER(name) AS lname, COUNT(*) AS n FROM test_entities GROUP BY lname) AS gorgany_count",
		},
		{
			// Replaced with a constant, GROUP BY 1 would group by the constant: one group.
			name: "group by a select-list position",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("name").GroupBy("1")
			},
			want: "SELECT COUNT(*) FROM (SELECT name FROM test_entities GROUP BY 1) AS gorgany_count",
		},
		{
			name: "distinct written into the select list",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("DISTINCT email").OrderBy("email", "ASC")
			},
			want: "SELECT COUNT(*) FROM (SELECT DISTINCT email FROM test_entities) AS gorgany_count",
		},
		{
			// The parenthesised spelling used to go unnoticed and was counted as every row.
			name: "distinct written with parentheses",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("DISTINCT(email)")
			},
			want: "SELECT COUNT(*) FROM (SELECT DISTINCT(email) FROM test_entities) AS gorgany_count",
		},
		{
			name: "distinct written with parentheses beside another column",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("DISTINCT(email), age")
			},
			want: "SELECT COUNT(*) FROM (SELECT DISTINCT(email), age FROM test_entities) AS gorgany_count",
		},
		{
			name: "distinct flag",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				qb = qb.Select("email", "age")
				qb.Build().Select.Distinct = true
				return qb
			},
			want: "SELECT COUNT(*) FROM (SELECT DISTINCT email, age FROM test_entities) AS gorgany_count",
		},
		{
			name: "union",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				archived := qb.Select("id", "name").From("archived_entities").Build()
				return qb.Select("id", "name").Union(archived).Limit(10)
			},
			want: "SELECT COUNT(*) FROM (SELECT id, name FROM test_entities UNION SELECT id, name FROM archived_entities) AS gorgany_count",
		},
		{
			name: "common table expression",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				adults := qb.From("test_entities").
					Where(&dbCore.BinaryCondition{Left: "age", Operator: ">=", Right: 18}).Build()
				return qb.WithCTE("adults", adults).From("adults").Select("DISTINCT email")
			},
			want: "WITH adults AS (SELECT * FROM test_entities WHERE age >= ?) SELECT COUNT(*) FROM (SELECT DISTINCT email FROM adults) AS gorgany_count",
			args: []any{18},
		},
	}

	for _, engine := range countEngines {
		t.Run(engine.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					mockDS, captured := countingSession(engine.builder)

					_, err := New[*TestEntity](mockDS.session).CountByQuery(tc.build(mockDS.session.Query()))

					require.NoError(t, err)
					assert.Equal(t, tc.want, captured.sql)
					assert.Equal(t, tc.args, captured.args)
				})
			}
		})
	}
}

// TestCountByQueryGroupByInnerColumnIsNamed. The grouped query's own columns are replaced,
// since the number of groups does not depend on them, and the replacement is named: an
// unnamed `1` in a derived table is SQL Server Msg 8155, and the caller's unaliased
// COUNT(*) would be the same error.
func TestCountByQueryGroupByInnerColumnIsNamed(t *testing.T) {
	src := pgBuilder().Select("age", "COUNT(*)").GroupBy("age").Build()

	count, err := countQuery(src, "test_entities")

	require.NoError(t, err)
	require.NotNil(t, count.From)
	require.True(t, count.From.IsSubquery)
	assert.Equal(t, "gorgany_count", count.From.Alias)
	assert.Equal(t, []string{"1 AS gorgany_one"}, count.From.Subquery.Select.Fields)
	assert.Equal(t, []string{"age", "COUNT(*)"}, src.Select.Fields,
		"the builder's own query must not be written through")
}

// TestCountByQueryRefusesUnnamedOrDuplicateColumns. A DISTINCT or UNION query's columns are
// what make its rows distinct, so they are kept inside the derived table, and a derived
// table needs every column named, and named once: SQL Server refuses otherwise (Msg 8155,
// 8156), and so does MySQL for a repeated name (error 1060). gorgany cannot invent a name
// without changing the query, so it refuses and names what to alias — before sending
// anything. A grouped query whose columns cannot be kept is refused the same way when its
// GROUP BY refers to them by alias or position, since replacing them would change what it
// groups by.
func TestCountByQueryRefusesUnnamedOrDuplicateColumns(t *testing.T) {
	distinctRefusal := func(problems string) string {
		return "orm: CountByQuery cannot wrap a DISTINCT/UNION query with unnamed or duplicate columns (" +
			problems + "); alias them"
	}
	groupedRefusal := func(reference, problems string) string {
		return "orm: CountByQuery cannot wrap a grouped query whose GROUP BY refers to its select list (" +
			reference + ") while that list cannot be kept (" + problems + "); alias its columns"
	}
	refused := []struct {
		name    string
		build   func(dbCore.IQueryBuilder) dbCore.IQueryBuilder
		wantErr string
	}{
		{
			name:    "an expression without an alias",
			build:   func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder { return qb.Select("DISTINCT LOWER(email)") },
			wantErr: distinctRefusal("LOWER(email) has no name"),
		},
		{
			name: "the same name from two tables",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				other := qb.Select("id", "id").From("archived_entities").Build()
				return qb.Select("t.id", "a.id").Union(other)
			},
			wantErr: distinctRefusal("id is not unique"),
		},
		{
			name:    "names that differ only in case",
			build:   func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder { return qb.Select("DISTINCT email, EMAIL") },
			wantErr: distinctRefusal("EMAIL is not unique"),
		},
		{
			name: "a star beside a join",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				qb = qb.Select("*").InnerJoin("archived_entities", &dbCore.RawCondition{SQL: "archived_entities.id = test_entities.id"})
				qb.Build().Select.Distinct = true
				return qb
			},
			wantErr: distinctRefusal("* with a JOIN"),
		},
		{
			name:    "a star beside another column",
			build:   func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder { return qb.Select("DISTINCT *", "name") },
			wantErr: distinctRefusal("* beside other columns"),
		},
		{
			name:    "a parenthesised expression without an alias",
			build:   func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder { return qb.Select("DISTINCT(LOWER(email))") },
			wantErr: distinctRefusal("(LOWER(email)) has no name"),
		},
		{
			// COUNT(*) has no name, so the list is replaced, and lname would be gone.
			name: "a group by alias beside an unnamed column",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("LOWER(name) AS lname", "COUNT(*)").GroupBy("lname")
			},
			wantErr: groupedRefusal("lname", "COUNT(*) has no name"),
		},
		{
			name:    "a group by position with no select list",
			build:   func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder { return qb.GroupBy("1") },
			wantErr: groupedRefusal("1", "* is not a named column"),
		},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			mockDS, captured := countingSession(pgBuilder)

			_, err := New[*TestEntity](mockDS.session).CountByQuery(tc.build(mockDS.session.Query()))

			require.Error(t, err)
			assert.Equal(t, tc.wantErr, err.Error())
			assert.Zero(t, captured.calls, "nothing may be sent")
		})
	}

	accepted := []struct {
		name  string
		build func(dbCore.IQueryBuilder) dbCore.IQueryBuilder
		want  string
	}{
		{
			name: "aliased expressions",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("DISTINCT LOWER(email) AS email_lower", "CAST(age AS text) AS age_text")
			},
			want: "SELECT COUNT(*) FROM (SELECT DISTINCT LOWER(email) AS email_lower, CAST(age AS text) AS age_text FROM test_entities) AS gorgany_count",
		},
		{
			name: "dotted columns with distinct last parts",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select(`DISTINCT t.id, "t"."Name", ` + "`t`.`email`")
			},
			want: "SELECT COUNT(*) FROM (SELECT DISTINCT t.id, \"t\".\"Name\", `t`.`email` FROM test_entities) AS gorgany_count",
		},
		{
			name:  "a star without a join",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder { return qb.Select("DISTINCT *") },
			want:  "SELECT COUNT(*) FROM (SELECT DISTINCT * FROM test_entities) AS gorgany_count",
		},
		{
			name: "one select string holding several columns",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("DISTINCT id, COALESCE(name, 'a, b') AS label")
			},
			want: "SELECT COUNT(*) FROM (SELECT DISTINCT id, COALESCE(name, 'a, b') AS label FROM test_entities) AS gorgany_count",
		},
		{
			// An expression over the table's columns survives the replacement of the list.
			name: "a group by expression beside an unnamed column",
			build: func(qb dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return qb.Select("LOWER(name)", "COUNT(*)").GroupBy("LOWER(name)")
			},
			want: "SELECT COUNT(*) FROM (SELECT 1 AS gorgany_one FROM test_entities GROUP BY LOWER(name)) AS gorgany_count",
		},
	}
	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			mockDS, captured := countingSession(pgBuilder)

			_, err := New[*TestEntity](mockDS.session).CountByQuery(tc.build(mockDS.session.Query()))

			require.NoError(t, err)
			assert.Equal(t, 1, captured.calls)
			assert.Equal(t, tc.want, captured.sql)
		})
	}
}

// TestCountByQueryRefusesAWriteBuilder. CountRaw runs whatever it is given, so a builder
// holding an UPDATE used to be executed as one by a method named Count.
func TestCountByQueryRefusesAWriteBuilder(t *testing.T) {
	mockDS, captured := countingSession(pgBuilder)
	qb := mockDS.session.Query().Update("test_entities").Set("name", "x")

	_, err := New[*TestEntity](mockDS.session).CountByQuery(qb)

	require.Error(t, err)
	assert.Equal(t, "orm: CountByQuery counts the rows of a SELECT, but the builder holds an INSERT, UPDATE or DELETE",
		err.Error())
	assert.Zero(t, captured.calls, "nothing may be sent")
}
