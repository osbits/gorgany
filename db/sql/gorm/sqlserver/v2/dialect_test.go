package v2

import (
	"errors"
	"strings"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This suite is pure string assertion: it runs with no SQL Server anywhere, and pins either the
// exact SQL and args or the exact refusal. The shapes that matter most are the ones an
// EF-owned schema forces — [dbo].[2024Orders], a digit-leading table, and [Order], a reserved
// word — and the ones T-SQL spells its own way: TOP, OFFSET … FETCH, APPLY, OUTPUT and MERGE.
// Each was also run against SQL Server 2022 while the dialect was written.

func d() *SQLServerDialect { return &SQLServerDialect{} }

// render renders b and fails the test on a refusal.
func render(t *testing.T, b dbCore.IQueryBuilder) (string, []any) {
	t.Helper()
	sql, args, err := b.ToSQL()
	require.NoError(t, err)
	return sql, args
}

func requireUnsupported(t *testing.T, err error, construct string) {
	t.Helper()
	require.Error(t, err)
	var unsupportedErr *dbCore.UnsupportedError
	require.Truef(t, errors.As(err, &unsupportedErr), "expected an UnsupportedError, got %T: %v", err, err)
	assert.Equal(t, DialectName, unsupportedErr.Dialect)
	assert.Equal(t, construct, unsupportedErr.Construct)
	assert.Contains(t, err.Error(), "sqlserver does not support "+construct)
}

// distinct marks a builder's SELECT as DISTINCT, which the builder has no method for.
func distinct(b dbCore.IQueryBuilder) dbCore.IQueryBuilder {
	q := b.Build()
	if q.Select == nil {
		q.Select = &dbCore.SelectClause{}
	}
	q.Select.Distinct = true
	return b
}

// ------------------------------------------------------------------- basics

func TestNameIsSQLServer(t *testing.T) {
	assert.Equal(t, "sqlserver", d().Name())
	assert.Equal(t, DialectName, d().Name())
}

func TestCapabilities(t *testing.T) {
	assert.Equal(t, 2098, MaxBindParameters)
	assert.Equal(t, 1000, MaxInsertRows)
	assert.Equal(t, MaxBindParameters, dbCore.BindParameterLimit(d()))
	assert.True(t, dbCore.ReturningBlockedByTriggers(d()))
	assert.True(t, dbCore.SupportsReturning(d()), "RETURNING becomes OUTPUT, so the ORM reads keys back with it")
	assert.True(t, dbCore.SupportsReturning(&SQLServerDialect{ReadOnly: true}),
		"the capability is the dialect's, not the datasource's policy")
}

func TestFormatSelect(t *testing.T) {
	sql, args, err := d().FormatSelect([]string{"Id", "o.Name", "COUNT(*) AS n"}, false, nil)
	require.NoError(t, err)
	assert.Equal(t, "SELECT [Id], [o].[Name], COUNT(*) AS n", sql)
	assert.Empty(t, args)

	sql, _, err = d().FormatSelect([]string{"Status"}, true, nil)
	require.NoError(t, err)
	assert.Equal(t, "SELECT DISTINCT [Status]", sql)

	sql, _, err = d().FormatSelect(nil, false, nil)
	require.NoError(t, err)
	assert.Equal(t, "SELECT *", sql, "an empty list selects every column")
}

// TestFormatSelectRefusesDistinctOn: T-SQL has no DISTINCT ON, and degrading it to a plain
// DISTINCT would return a different row set.
func TestFormatSelectRefusesDistinctOn(t *testing.T) {
	sql, _, err := d().FormatSelect([]string{"Id"}, true, []string{"CustomerId"})
	requireUnsupported(t, err, "DISTINCT ON")
	assert.Empty(t, sql)
	assert.Contains(t, err.Error(), "ROW_NUMBER()")

	sql, _, err = d().FormatDistinctOn([]string{"a"})
	requireUnsupported(t, err, "DISTINCT ON")
	assert.Empty(t, sql)

	b := NewBuilder().Select("Id").From("orders").DistinctOn("CustomerId")
	_, _, err = distinct(b).ToSQL()
	requireUnsupported(t, err, "DISTINCT ON")
}

func TestFormatFrom(t *testing.T) {
	tests := []struct{ table, alias, want string }{
		{"users", "", "FROM [users]"},
		{"users", "u", "FROM [users] AS [u]"},
		{"users u", "", "FROM [users] AS [u]"},
		{"dbo.2024Orders", "o", "FROM [dbo].[2024Orders] AS [o]"},
		{"[dbo].[Order]", "", "FROM [dbo].[Order]"},
		{"dbo.fn_recent(7) r", "", "FROM dbo.fn_recent(7) r"},
	}
	for _, tt := range tests {
		sql, args, err := d().FormatFrom(tt.table, tt.alias)
		require.NoErrorf(t, err, "FormatFrom(%q, %q)", tt.table, tt.alias)
		assert.Equal(t, tt.want, sql)
		assert.Empty(t, args)
	}

	_, _, err := d().FormatFrom("users u", "x")
	require.Error(t, err, "two aliases for one table")
	assert.Contains(t, err.Error(), "already names an alias")

	_, _, err = d().FormatFrom("users", "u x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table alias")
}

// TestPlaceholderStyleIsQuestionMark: the dialect emits "?", which gorm rewrites to @p1…@pN
// when the statement runs, exactly as it does for the other engines.
func TestPlaceholderStyleIsQuestionMark(t *testing.T) {
	sql, args := render(t, NewBuilder().Select("Id").From("users").Eq("a", 1).Eq("b", 2))
	assert.Equal(t, "SELECT [Id] FROM [users] WHERE [a] = ? AND [b] = ?", sql)
	assert.Equal(t, []any{1, 2}, args)
	assert.NotContains(t, sql, "@p1")
	assert.NotContains(t, sql, "$1")
}

// TestNoAtSignInDialectOutput: an "@" anywhere in a statement switches gorm from positional to
// named parameters, and a "?" that is not a placeholder takes an argument meant for another.
// So every shape the dialect renders must contain no "@" and exactly one "?" per argument.
func TestNoAtSignInDialectOutput(t *testing.T) {
	sub := NewBuilder().Select("CustomerId").From("dbo.2024Orders").Eq("Status", "open").Limit(3).Build()
	cases := map[string]dbCore.IQueryBuilder{
		"select": NewBuilder().Select("Id", "Total AS Sum").From("dbo.2024Orders o").Eq("o.Id", 1).
			In("o.Status", "a", "b").Between("o.Total", 1, 2).Like("o.Name", "a%").OrderBy("o.Id", "desc").Offset(5).Limit(10),
		"subquery":   NewBuilder().Select("Id").From("dbo.Customer").InSubquery("Id", sub),
		"apply":      NewBuilder().Select("c.Id").From("dbo.Customer c").LateralJoin(sub, "x", nil),
		"grouping":   NewBuilder().Select("a").From("t").GroupBy("a").Cube("b").GroupingSets([]string{"a"}, []string{}),
		"insert":     NewBuilder().Insert("dbo.2024Orders").Columns("a", "b").Values(1, 2).Values(3, 4).Returning("Id"),
		"do nothing": NewBuilder().Insert("t").Columns("a", "b").Values(1, 2).OnConflict("a").DoNothing().Returning("Id"),
		"merge":      NewBuilder().Insert("t").Columns("a", "b").Values(1, 2).OnConflict("a").DoUpdate(map[string]any{"b": 3}).Returning("Id"),
		"update":     NewBuilder().Update("dbo.Order").Set("Name", "x").Eq("Id", 1).Limit(1).Returning("Id", "DELETED.Name"),
		"delete":     NewBuilder().Delete("dbo.Order").Eq("Id", 1).Limit(1).Returning("*"),
		"default":    NewBuilder().Insert("t").Columns().Values().Returning("Id"),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			sql, args := render(t, b)
			assert.NotContains(t, sql, "@")
			assert.Equal(t, len(args), strings.Count(sql, "?"), sql)
		})
	}
}

func TestFullSelectClauseOrder(t *testing.T) {
	sql, args := render(t, NewBuilder().
		WithCTE("paid", NewBuilder().Select("CustomerId").From("dbo.2024Orders").Eq("Status", "paid").Build()).
		Select("c.Id", "COUNT(o.Id) AS orders").
		From("dbo.Customer c").
		LeftJoin("dbo.2024Orders o", &dbCore.BinaryCondition{Left: "o.CustomerId", Operator: "=", Right: dbCore.Identifier("c.Id")}).
		Eq("c.Region", "north").
		GroupBy("c.Id").
		Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(o.Id)"), Operator: ">", Right: 2}).
		OrderBy("c.Id", "desc").
		Limit(10).
		Offset(20))

	assert.Equal(t,
		"WITH [paid] AS (SELECT [CustomerId] FROM [dbo].[2024Orders] WHERE [Status] = ?) "+
			"SELECT [c].[Id], COUNT(o.Id) AS orders FROM [dbo].[Customer] AS [c] "+
			"LEFT JOIN [dbo].[2024Orders] AS [o] ON [o].[CustomerId] = [c].[Id] "+
			"WHERE [c].[Region] = ? "+
			"GROUP BY [c].[Id] "+
			"HAVING COUNT(o.Id) > ? "+
			"ORDER BY [c].[Id] DESC "+
			"OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY",
		sql)
	assert.Equal(t, []any{"paid", "north", 2}, args)
}

// TestEFShapes pins the statements an app pointed at an EF Core schema sends most: a
// digit-leading table, a reserved-word table and column, and the ORM's lookups on them.
func TestEFShapes(t *testing.T) {
	sql, args := render(t, NewBuilder().From("dbo.2024Orders").Eq("Id", 7).Limit(1))
	assert.Equal(t, "SELECT TOP (1) * FROM [dbo].[2024Orders] WHERE [Id] = ?", sql)
	assert.Equal(t, []any{7}, args)

	sql, args = render(t, NewBuilder().Select("Id", "Name", "User").From("[dbo].[Order]").Eq("User", "ann").OrderBy("Name", "asc"))
	assert.Equal(t, "SELECT [Id], [Name], [User] FROM [dbo].[Order] WHERE [User] = ? ORDER BY [Name] ASC", sql)
	assert.Equal(t, []any{"ann"}, args)

	sql, args = render(t, NewBuilder().Select("o.*").From("dbo.2024Orders o").
		InnerJoin("dbo.Order x", &dbCore.RawCondition{SQL: "?.OrderId = ?.?", Args: []any{"o", "x", "Id"}}).
		IsNull("o.DeletedAt"))
	assert.Equal(t,
		"SELECT [o].* FROM [dbo].[2024Orders] AS [o] INNER JOIN [dbo].[Order] AS [x] ON [o].[OrderId] = [x].[Id] "+
			"WHERE [o].[DeletedAt] IS NULL", sql)
	assert.Empty(t, args)

	sql, args = render(t, NewBuilder().Select("COUNT(*)").From("dbo.2024Orders").Eq("Status", "open"))
	assert.Equal(t, "SELECT COUNT(*) FROM [dbo].[2024Orders] WHERE [Status] = ?", sql)
	assert.Equal(t, []any{"open"}, args)

	sql, args = render(t, NewBuilder().Insert("dbo.2024Orders").Columns("CustomerId", "Total").Values(1, 9.5).Returning("Id"))
	assert.Equal(t, "INSERT INTO [dbo].[2024Orders] ([CustomerId], [Total]) OUTPUT INSERTED.[Id] VALUES (?, ?)", sql)
	assert.Equal(t, []any{1, 9.5}, args)

	sql, args = render(t, NewBuilder().Update("dbo.Order").Set("Name", "x").Eq("Id", 3))
	assert.Equal(t, "UPDATE [dbo].[Order] SET [Name] = ? WHERE [Id] = ?", sql)
	assert.Equal(t, []any{"x", 3}, args)

	sql, args = render(t, NewBuilder().Delete("dbo.2024Orders").Eq("Id", 3))
	assert.Equal(t, "DELETE FROM [dbo].[2024Orders] WHERE [Id] = ?", sql)
	assert.Equal(t, []any{3}, args)
}

// TestDeterministicOutput pins that repeated renders of the same query are byte-identical,
// which a map-ranged SET clause would prevent.
func TestDeterministicOutput(t *testing.T) {
	build := func() string {
		sql, _ := render(t, NewBuilder().Update("users").SetMap(map[string]any{"c": 3, "a": 1, "b": 2}))
		return sql
	}
	first := build()
	assert.Equal(t, "UPDATE [users] SET [a] = ?, [b] = ?, [c] = ?", first)
	for i := 0; i < 25; i++ {
		assert.Equal(t, first, build())
	}
}

func TestFormatQueryRejectsNil(t *testing.T) {
	_, _, err := d().FormatQuery(nil)
	require.Error(t, err)
}

func TestAQueryThatIsTwoWritesIsAnError(t *testing.T) {
	_, _, err := d().FormatQuery(&dbCore.Query{
		Insert: &dbCore.InsertClause{Table: "t", Columns: []string{"a"}, Values: [][]any{{1}}},
		Delete: &dbCore.DeleteClause{Table: "t"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both INSERT and DELETE")
}

// -------------------------------------------------------------------- joins

func TestFormatJoinSupportedTypes(t *testing.T) {
	on := &dbCore.BinaryCondition{Left: "o.CustomerId", Operator: "=", Right: dbCore.Identifier("c.Id")}
	tests := []struct{ joinType, want string }{
		{"INNER", "INNER JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
		{"inner", "INNER JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
		{"LEFT", "LEFT JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
		{"LEFT OUTER", "LEFT OUTER JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
		{"RIGHT", "RIGHT JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
		{"RIGHT OUTER", "RIGHT OUTER JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
		{"FULL", "FULL JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
		{"full  outer", "FULL OUTER JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
		{"", "JOIN [orders] AS [o] ON [o].[CustomerId] = [c].[Id]"},
	}
	for _, tt := range tests {
		sql, args, err := d().FormatJoin(&dbCore.JoinClause{Type: tt.joinType, Table: "orders o", Condition: on})
		require.NoErrorf(t, err, "join type %q", tt.joinType)
		assert.Equal(t, tt.want, sql)
		assert.Empty(t, args)
	}

	sql, _, err := d().FormatJoin(&dbCore.JoinClause{Type: "CROSS", Table: "dbo.Order"})
	require.NoError(t, err)
	assert.Equal(t, "CROSS JOIN [dbo].[Order]", sql)

	sql, _, err = d().FormatJoin(nil)
	require.NoError(t, err)
	assert.Empty(t, sql)
}

func TestFormatJoinRefusesNaturalAndUnknown(t *testing.T) {
	for _, joinType := range []string{"NATURAL", "natural left", "NATURAL INNER"} {
		sql, _, err := d().FormatJoin(&dbCore.JoinClause{Type: joinType, Table: "orders"})
		requireUnsupported(t, err, "NATURAL JOIN")
		assert.Empty(t, sql)
	}
	_, _, err := NewBuilder().Select("Id").From("users").NaturalJoin("orders").ToSQL()
	requireUnsupported(t, err, "NATURAL JOIN")

	sql, _, err := d().FormatJoin(&dbCore.JoinClause{Type: "SEMI", Table: "orders", Condition: &dbCore.RawCondition{SQL: "1=1"}})
	requireUnsupported(t, err, `a "SEMI" join`)
	assert.Empty(t, sql)
}

func TestCrossJoinWithConditionIsRefused(t *testing.T) {
	_, _, err := d().FormatJoin(&dbCore.JoinClause{Type: "CROSS", Table: "orders", Condition: &dbCore.RawCondition{SQL: "a = b"}})
	requireUnsupported(t, err, "CROSS JOIN with an ON condition")
}

func TestJoinWithoutConditionIsAnError(t *testing.T) {
	_, _, err := NewBuilder().Select("Id").From("users").InnerJoin("orders", nil).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INNER JOIN needs an ON condition")

	_, _, err = NewBuilder().Select("Id").From("users").InnerJoin("orders", &dbCore.RawCondition{SQL: "  "}).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "renders empty")
}

func latestPaidOrder() *dbCore.Query {
	return NewBuilder().Select("o.Total").From("dbo.2024Orders o").
		Where(&dbCore.BinaryCondition{Left: "o.CustomerId", Operator: "=", Right: dbCore.Identifier("c.Id")}).
		OrderBy("o.Id", "desc").Limit(1).Build()
}

// TestLateralBecomesCrossApply: LATERAL is Postgres's and MySQL's, APPLY is T-SQL's, and
// APPLY takes no ON, so the trivial ON a LATERAL join is written with goes.
func TestLateralBecomesCrossApply(t *testing.T) {
	want := "SELECT [c].[Name], [x].[Total] FROM [dbo].[Customer] AS [c] CROSS APPLY (SELECT TOP (1) [o].[Total] " +
		"FROM [dbo].[2024Orders] AS [o] WHERE [o].[CustomerId] = [c].[Id] ORDER BY [o].[Id] DESC) AS [x]"
	for _, on := range []dbCore.Condition{nil, &dbCore.RawCondition{SQL: "true"}, &dbCore.RawCondition{SQL: " 1 = 1 "}, &dbCore.RawCondition{SQL: "TRUE"}} {
		sql, args := render(t, NewBuilder().Select("c.Name", "x.Total").From("dbo.Customer c").LateralJoin(latestPaidOrder(), "x", on))
		assert.Equal(t, want, sql)
		assert.Empty(t, args)
	}

	sql, _, err := d().FormatJoin(&dbCore.JoinClause{Type: "CROSS APPLY", Table: "dbo.fn_items(c.Id) i"})
	require.NoError(t, err)
	assert.Equal(t, "CROSS APPLY dbo.fn_items(c.Id) i", sql, "a table-valued function is the caller's SQL")
}

func TestLeftLateralBecomesOuterApply(t *testing.T) {
	for _, joinType := range []string{"LEFT LATERAL", "LEFT OUTER LATERAL", "LEFT", "OUTER APPLY"} {
		sql, _, err := d().FormatJoin(&dbCore.JoinClause{
			Type: joinType, IsLateral: true, IsSubquery: true, Alias: "x", Subquery: latestPaidOrder(),
			Condition: &dbCore.RawCondition{SQL: "true"},
		})
		require.NoErrorf(t, err, "join type %q", joinType)
		assert.True(t, strings.HasPrefix(sql, "OUTER APPLY (SELECT TOP (1) [o].[Total] FROM"), sql)
		assert.True(t, strings.HasSuffix(sql, ") AS [x]"), sql)
	}

	// APPLY has no right or full form.
	for _, joinType := range []string{"RIGHT LATERAL", "FULL LATERAL"} {
		_, _, err := d().FormatJoin(&dbCore.JoinClause{Type: joinType, IsSubquery: true, Alias: "x", Subquery: latestPaidOrder()})
		requireUnsupported(t, err, "a "+joinType+" join")
	}
}

func TestLateralWithRealOnIsRefused(t *testing.T) {
	for _, on := range []dbCore.Condition{
		&dbCore.RawCondition{SQL: "x.CustomerId = c.Id"},
		&dbCore.RawCondition{SQL: "1=1", Args: []any{1}},
		&dbCore.BinaryCondition{Left: "x.CustomerId", Operator: "=", Right: dbCore.Identifier("c.Id")},
	} {
		_, _, err := NewBuilder().Select("c.Name").From("dbo.Customer c").LateralJoin(latestPaidOrder(), "x", on).ToSQL()
		requireUnsupported(t, err, "a LATERAL join with an ON condition")
		assert.Contains(t, err.Error(), "subquery's WHERE")
	}
}

// TestJoinOnIsValidated: the Postgres and MySQL dialects render a JOIN's ON without checking
// it. This one renders it under the condition context, like a WHERE.
func TestJoinOnIsValidated(t *testing.T) {
	_, _, err := NewBuilder().Select("Id").From("users u").
		InnerJoin("orders o", &dbCore.BinaryCondition{Left: "o.user_id = u.id OR 1=1 --", Operator: "=", Right: 1}).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot render JOIN ON")
	assert.Contains(t, err.Error(), "not a column reference")

	_, _, err = NewBuilder().Select("Id").From("users u").
		InnerJoin("orders o", &dbCore.BinaryCondition{Left: "o.UserId", Operator: "~", Right: "x"}).ToSQL()
	requireUnsupported(t, err, "the ~ operator")
}

func TestDerivedJoinRequiresAlias(t *testing.T) {
	_, _, err := d().FormatJoin(&dbCore.JoinClause{Type: "INNER", IsSubquery: true, Subquery: latestPaidOrder(), Condition: &dbCore.RawCondition{SQL: "1=1"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an alias")

	_, _, err = d().FormatJoin(&dbCore.JoinClause{Type: "INNER", IsSubquery: true, Alias: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "carries no query")
}

// --------------------------------------------------------------- conditions

func TestFormatWhereQuotesConditionIdentifiers(t *testing.T) {
	sql, args, err := d().FormatWhere(&dbCore.WhereClause{
		Operator: "AND",
		Conditions: []dbCore.Condition{
			&dbCore.BinaryCondition{Left: "o.2024Total", Operator: ">", Right: 18},
			&dbCore.IsNullCondition{Field: "Order.DeletedAt"},
			&dbCore.InCondition{Field: "User", Values: []any{"a", "b"}},
			&dbCore.BetweenCondition{Field: "Total", Lower: 1, Upper: 2},
			&dbCore.LikeCondition{Field: "Name", Pattern: "a!%", Escape: "!"},
			&dbCore.BinaryCondition{Left: "a.Id", Operator: "=", Right: dbCore.Identifier("b.Id")},
			&dbCore.RawCondition{SQL: "?.Order = ?", Args: []any{"t", 5}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t,
		"WHERE [o].[2024Total] > ? AND [Order].[DeletedAt] IS NULL AND [User] IN (?, ?) AND [Total] BETWEEN ? AND ? "+
			"AND [Name] LIKE ? ESCAPE '!' AND [a].[Id] = [b].[Id] AND [t].[Order] = ?",
		sql)
	assert.Equal(t, []any{18, "a", "b", 1, 2, "a!%", 5}, args)

	sql, args, err = d().FormatWhere(&dbCore.WhereClause{Operator: "or", Conditions: []dbCore.Condition{
		&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
		&dbCore.BinaryCondition{Left: "b", Operator: "=", Right: 2},
	}})
	require.NoError(t, err)
	assert.Equal(t, "WHERE [a] = ? OR [b] = ?", sql)
	assert.Equal(t, []any{1, 2}, args)
}

func TestFormatWhereEmptyClause(t *testing.T) {
	for _, where := range []*dbCore.WhereClause{nil, {Operator: "AND"}, {Operator: "AND", Conditions: []dbCore.Condition{&dbCore.RawCondition{SQL: ""}}}} {
		sql, args, err := d().FormatWhere(where)
		require.NoError(t, err)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	}
}

// TestFormatWhereRewritesILIKE: T-SQL has no ILIKE. LIKE is case-insensitive under the default
// collations, EF's included.
func TestFormatWhereRewritesILIKE(t *testing.T) {
	sql, args, err := d().FormatWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
		&dbCore.BinaryCondition{Left: "Name", Operator: "ilike", Right: "%ann%"},
		&dbCore.BinaryCondition{Left: "Name", Operator: "NOT ILIKE", Right: "%bob%"},
		&dbCore.RawCondition{SQL: "notilike = 1 AND Region ILIKE ?", Args: []any{"n%"}},
	}})
	require.NoError(t, err)
	assert.Equal(t, "WHERE [Name] LIKE ? AND [Name] NOT LIKE ? AND notilike = 1 AND Region LIKE ?", sql)
	assert.Equal(t, []any{"%ann%", "%bob%", "n%"}, args)
	assert.NotContains(t, sql, "ILIKE")
}

// TestILIKEInsideALiteralOrANameIsLeftAlone: only the operator is rewritten. The word inside a
// string literal is data, and inside a delimited name part of the name, so rewriting it would
// match other rows, or name a column that does not exist, with no error.
func TestILIKEInsideALiteralOrANameIsLeftAlone(t *testing.T) {
	for raw, want := range map[string]string{
		"Note = 'has ilike word'":                        "Note = 'has ilike word'",
		"Note = 'it''s ILIKE' AND Name ILIKE ?":          "Note = 'it''s ILIKE' AND Name LIKE ?",
		"[is ilike] = 1":                                 "[is ilike] = 1",
		"[a]]ILIKE] ILIKE ?":                             "[a]]ILIKE] LIKE ?",
		`"ilike col" ILIKE ?`:                            `"ilike col" LIKE ?`,
		"Name ILIKE N'x' + 'ilike'":                      "Name LIKE N'x' + 'ilike'",
		"Name ilike ? AND Note = 'ILIKE":                 "Name LIKE ? AND Note = 'ILIKE",
		"Name ILIKE ? OR [Note]ILIKE ? OR 'a'ILIKE Name": "Name LIKE ? OR [Note]LIKE ? OR 'a'LIKE Name",
	} {
		assert.Equalf(t, want, rewriteILIKE(raw), "rewriteILIKE(%q)", raw)
	}

	sql, _, err := d().FormatWhere(&dbCore.WhereClause{Conditions: []dbCore.Condition{
		&dbCore.RawCondition{SQL: "Note = 'has ilike word' AND [is ilike] = 1"},
	}})
	require.NoError(t, err)
	assert.Equal(t, "WHERE Note = 'has ilike word' AND [is ilike] = 1", sql)
}

// TestFormatWhereRefusesNonIdentifierField: the context is Strict, so a field that is not a
// name is refused rather than bound and compared as text — which, on an UPDATE or DELETE,
// would match every row or none.
func TestFormatWhereRefusesNonIdentifierField(t *testing.T) {
	for _, condition := range []dbCore.Condition{
		&dbCore.BinaryCondition{Left: "Name = 'x' OR 'y'='y'", Operator: "=", Right: true},
		&dbCore.BinaryCondition{Left: "LOWER(Name)", Operator: "=", Right: "x"},
		&dbCore.BinaryCondition{Left: 1, Operator: "=", Right: 1},
		&dbCore.IsNullCondition{Field: nil},
		&dbCore.InCondition{Field: "1e5", Values: []any{1}},
		&dbCore.LikeCondition{Field: "CURRENT_USER", Pattern: "x"},
	} {
		_, _, err := d().FormatWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{condition}})
		require.Errorf(t, err, "%#v", condition)
		assert.Contains(t, err.Error(), "cannot render WHERE")
	}

	// A core.Raw is the caller vouching for the expression.
	sql, args, err := d().FormatWhere(&dbCore.WhereClause{Operator: "AND", Conditions: []dbCore.Condition{
		&dbCore.BinaryCondition{Left: dbCore.Raw("LOWER(Name)"), Operator: "=", Right: "x"},
	}})
	require.NoError(t, err)
	assert.Equal(t, "WHERE LOWER(Name) = ?", sql)
	assert.Equal(t, []any{"x"}, args)
}

func TestFormatWhereRefusesABadJoiningOperator(t *testing.T) {
	_, _, err := d().FormatWhere(&dbCore.WhereClause{Operator: "XOR", Conditions: []dbCore.Condition{
		&dbCore.BinaryCondition{Left: "a", Operator: "=", Right: 1},
		&dbCore.BinaryCondition{Left: "b", Operator: "=", Right: 2},
	}})
	requireUnsupported(t, err, `joining conditions with "XOR"`)
}

// TestEmptyInRendersFalsePredicate: IN () is a syntax error, and T-SQL has no FALSE literal.
func TestEmptyInRendersFalsePredicate(t *testing.T) {
	sql, args := render(t, NewBuilder().Select("Id").From("users").In("Id"))
	assert.Equal(t, "SELECT [Id] FROM [users] WHERE 1=0", sql)
	assert.Empty(t, args)

	sql, args = render(t, NewBuilder().Select("Id").From("users").NotIn("Id").Eq("a", 1))
	assert.Equal(t, "SELECT [Id] FROM [users] WHERE 1=1 AND [a] = ?", sql)
	assert.Equal(t, []any{1}, args)
}

// TestSubqueryInConditionGoesThroughDialect: a subquery nested in a condition is rendered by
// this dialect, not by core's Postgres-shaped fallback, so it is quoted and paginated with TOP.
func TestSubqueryInConditionGoesThroughDialect(t *testing.T) {
	sub := NewBuilder().Select("CustomerId").From("dbo.2024Orders").Eq("Status", "open").OrderBy("Total", "desc").Limit(5).Build()

	sql, args := render(t, NewBuilder().Select("Id").From("dbo.Customer").Eq("Region", "north").InSubquery("Id", sub))
	assert.Equal(t,
		"SELECT [Id] FROM [dbo].[Customer] WHERE [Region] = ? AND [Id] IN "+
			"(SELECT TOP (5) [CustomerId] FROM [dbo].[2024Orders] WHERE [Status] = ? ORDER BY [Total] DESC)",
		sql)
	assert.Equal(t, []any{"north", "open"}, args)

	sql, _ = render(t, NewBuilder().Select("Id").From("dbo.Customer").NotExists(sub))
	assert.Equal(t,
		"SELECT [Id] FROM [dbo].[Customer] WHERE NOT EXISTS "+
			"(SELECT TOP (5) [CustomerId] FROM [dbo].[2024Orders] WHERE [Status] = ? ORDER BY [Total] DESC)",
		sql)

	sql, args = render(t, NewBuilder().Select("Id").From("t").Where(&dbCore.BinaryCondition{
		Left: "Total", Operator: ">", Right: NewBuilder().Select("AVG(Total)").From("t").Eq("Kind", 1).Build(),
	}))
	assert.Equal(t, "SELECT [Id] FROM [t] WHERE [Total] > (SELECT AVG(Total) FROM [t] WHERE [Kind] = ?)", sql)
	assert.Equal(t, []any{1}, args)

	// The subquery's own conditions are checked as strictly as the outer ones.
	bad := NewBuilder().Select("Id").From("t").Where(&dbCore.BinaryCondition{Left: "a OR 1=1", Operator: "=", Right: 1}).Build()
	_, _, err := NewBuilder().Select("Id").From("u").InSubquery("Id", bad).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a column reference")
}

// ------------------------------------------------------- ORDER BY / GROUP BY

func TestFormatOrderByQuotesAndNormalizes(t *testing.T) {
	sql, args, err := d().FormatOrderBy("CreatedAt", "desc")
	require.NoError(t, err)
	assert.Equal(t, "ORDER BY [CreatedAt] DESC", sql)
	assert.Empty(t, args)

	sql, _, err = d().FormatOrderBy("o.2024Total", "asc")
	require.NoError(t, err)
	assert.Equal(t, "ORDER BY [o].[2024Total] ASC", sql)

	// Anything unrecognised in the direction slot degrades to ASC.
	sql, _, err = d().FormatOrderBy("Id", "; DROP TABLE users")
	require.NoError(t, err)
	assert.Equal(t, "ORDER BY [Id] ASC", sql)
}

// TestFormatOrderByRefusesNonIdentifier: Postgres and MySQL bind a non-identifier as a value,
// which orders by a constant; SQL Server refuses that at the server (Msg 1008), so the
// dialect refuses it where it is built.
func TestFormatOrderByRefusesNonIdentifier(t *testing.T) {
	for _, field := range []string{"(SELECT 1)", "LEN(Name)", "Id DESC", "1", "", "Id; DROP TABLE t"} {
		sql, args, err := d().FormatOrderBy(field, "asc")
		requireUnsupported(t, err, "ORDER BY on something that is not a column")
		assert.Contains(t, err.Error(), "OrderByRaw")
		assert.Empty(t, sql)
		assert.Nil(t, args)
	}
	_, _, err := NewBuilder().Select("Id").From("t").OrderBy("LEN(Name)", "desc").ToSQL()
	requireUnsupported(t, err, "ORDER BY on something that is not a column")
}

func TestOrderByRawIsVerbatim(t *testing.T) {
	sql, args := render(t, NewBuilder().Select("Id").From("users").OrderByRaw("LEN(Name)", "desc").OrderBy("Id", "asc").Limit(5))
	assert.Equal(t, "SELECT TOP (5) [Id] FROM [users] ORDER BY LEN(Name) DESC, [Id] ASC", sql)
	assert.Empty(t, args)
}

// TestGroupByPositionIsRefused: GROUP BY 1 is the first select item on MySQL and Postgres. T-SQL
// has no GROUP BY position; it reads the constant 1 and refuses it (Msg 164), which the dialect
// says first, in every place a GROUP BY list stands.
func TestGroupByPositionIsRefused(t *testing.T) {
	for _, clause := range []*dbCore.GroupByClause{
		{Fields: []string{"1"}},
		{Fields: []string{"Status", " 2 "}},
		{Rollup: []string{"1"}},
		{Cube: []string{"Region", "3"}},
		{Sets: [][]string{{"Region"}, {"1"}}},
	} {
		_, _, err := d().FormatGroupBy(clause)
		requireUnsupported(t, err, "a GROUP BY position")
		assert.Contains(t, err.Error(), "group by the column or expression itself")
	}

	_, _, err := NewBuilder().Select("Status", "COUNT(*) AS n").From("dbo.2024Orders").GroupBy("1").ToSQL()
	requireUnsupported(t, err, "a GROUP BY position")
	assert.Contains(t, err.Error(), "GROUP BY 1 as a constant")

	// A number inside an expression is the expression's.
	sql, _ := render(t, NewBuilder().Select("COUNT(*) AS n").From("t").GroupBy("LEFT(Name, 1)"))
	assert.Equal(t, "SELECT COUNT(*) AS n FROM [t] GROUP BY LEFT(Name, 1)", sql)
}

func TestFormatGroupByISOModifiers(t *testing.T) {
	tests := []struct {
		clause *dbCore.GroupByClause
		want   string
	}{
		{&dbCore.GroupByClause{Fields: []string{"Status", "o.2024Kind"}}, "GROUP BY [Status], [o].[2024Kind]"},
		{&dbCore.GroupByClause{Fields: []string{"YEAR(CreatedAt)"}}, "GROUP BY YEAR(CreatedAt)"},
		{&dbCore.GroupByClause{Rollup: []string{"Region", "City"}}, "GROUP BY ROLLUP ([Region], [City])"},
		{&dbCore.GroupByClause{Cube: []string{"a", "b"}}, "GROUP BY CUBE ([a], [b])"},
		{&dbCore.GroupByClause{Sets: [][]string{{"a"}, {"a", "b"}, {}}}, "GROUP BY GROUPING SETS (([a]), ([a], [b]), ())"},
		{
			&dbCore.GroupByClause{Fields: []string{"Year"}, Rollup: []string{"Region"}, Cube: []string{"c"}, Sets: [][]string{{"d"}}},
			"GROUP BY [Year], ROLLUP ([Region]), CUBE ([c]), GROUPING SETS (([d]))",
		},
	}
	for _, tt := range tests {
		sql, args, err := d().FormatGroupBy(tt.clause)
		require.NoError(t, err)
		assert.Equal(t, tt.want, sql)
		assert.Empty(t, args)
	}

	for _, clause := range []*dbCore.GroupByClause{nil, {}} {
		sql, _, err := d().FormatGroupBy(clause)
		require.NoError(t, err)
		assert.Empty(t, sql, "must never emit a bare GROUP BY")
	}

	sql, _ := render(t, NewBuilder().Select("a", "b").From("t").GroupingSets([]string{"a"}, []string{"a", "b"}))
	assert.Equal(t, "SELECT [a], [b] FROM [t] GROUP BY GROUPING SETS (([a]), ([a], [b]))", sql)
}

func TestFormatHaving(t *testing.T) {
	sql, args, err := d().FormatHaving(&dbCore.HavingClause{
		Condition: &dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 5},
	})
	require.NoError(t, err)
	assert.Equal(t, "HAVING COUNT(*) > ?", sql)
	assert.Equal(t, []any{5}, args)

	sql, _, err = d().FormatHaving(&dbCore.HavingClause{Condition: &dbCore.RawCondition{SQL: "MAX(Name) ILIKE 'a%'"}})
	require.NoError(t, err)
	assert.Equal(t, "HAVING MAX(Name) LIKE 'a%'", sql)

	for _, having := range []*dbCore.HavingClause{nil, {}, {Condition: &dbCore.RawCondition{}}} {
		sql, _, err = d().FormatHaving(having)
		require.NoError(t, err)
		assert.Empty(t, sql)
	}

	_, _, err = d().FormatHaving(&dbCore.HavingClause{Condition: &dbCore.BinaryCondition{Left: "COUNT(*)", Operator: ">", Right: 5}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot render HAVING")
}

// --------------------------------------------------------------- pagination

func TestFormatLimitAndOffset(t *testing.T) {
	sql, _, err := d().FormatLimit(10)
	require.NoError(t, err)
	assert.Equal(t, "TOP (10)", sql)

	sql, _, err = d().FormatOffset(20)
	require.NoError(t, err)
	assert.Equal(t, "OFFSET 20 ROWS", sql)

	_, _, err = d().FormatLimit(-1)
	require.Error(t, err)
	_, _, err = d().FormatOffset(-1)
	require.Error(t, err)
}

func TestLimitWithoutOffsetIsTop(t *testing.T) {
	sql, _ := render(t, NewBuilder().Select("Id").From("users").OrderBy("Id", "desc").Limit(10))
	assert.Equal(t, "SELECT TOP (10) [Id] FROM [users] ORDER BY [Id] DESC", sql)

	sql, _ = render(t, distinct(NewBuilder().Select("Status").From("orders").Limit(3)))
	assert.Equal(t, "SELECT DISTINCT TOP (3) [Status] FROM [orders]", sql, "TOP goes after DISTINCT")
}

// TestTopZeroIsLegal: FETCH NEXT 0 ROWS is an error (Msg 10744); TOP (0) is not.
func TestTopZeroIsLegal(t *testing.T) {
	sql, _ := render(t, NewBuilder().Select("Id").From("users").Limit(0))
	assert.Equal(t, "SELECT TOP (0) [Id] FROM [users]", sql)
}

func TestOffsetZeroWithLimitIsTop(t *testing.T) {
	sql, _ := render(t, NewBuilder().Select("Id").From("users").Limit(10).Offset(0))
	assert.Equal(t, "SELECT TOP (10) [Id] FROM [users]", sql, "page one of a paginator needs no ORDER BY")
}

// TestOffsetSynthesisesOrderBySelectNull: OFFSET belongs to ORDER BY in T-SQL, so a query with
// no ORDER BY gets ORDER BY (SELECT NULL) — the same unspecified order LIMIT/OFFSET without
// ORDER BY has on the other engines.
func TestOffsetSynthesisesOrderBySelectNull(t *testing.T) {
	sql, _ := render(t, NewBuilder().Select("Id").From("users").Limit(10).Offset(20))
	assert.Equal(t, "SELECT [Id] FROM [users] ORDER BY (SELECT NULL) OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY", sql)
}

func TestOffsetFetchWithOrderBy(t *testing.T) {
	sql, _ := render(t, NewBuilder().Select("Id").From("users").OrderBy("Name", "asc").OrderBy("Id", "desc").Limit(10).Offset(20))
	assert.Equal(t, "SELECT [Id] FROM [users] ORDER BY [Name] ASC, [Id] DESC OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY", sql)
}

func TestLimitZeroWithOffsetIsTopZero(t *testing.T) {
	sql, _ := render(t, NewBuilder().Select("Id").From("users").Limit(0).Offset(20))
	assert.Equal(t, "SELECT TOP (0) [Id] FROM [users]", sql)
}

func TestBareOffsetIsOffsetRows(t *testing.T) {
	sql, _ := render(t, NewBuilder().Select("Id").From("users").Offset(20))
	assert.Equal(t, "SELECT [Id] FROM [users] ORDER BY (SELECT NULL) OFFSET 20 ROWS", sql)

	sql, _ = render(t, NewBuilder().Select("Id").From("users").OrderBy("Id", "asc").Offset(20))
	assert.Equal(t, "SELECT [Id] FROM [users] ORDER BY [Id] ASC OFFSET 20 ROWS", sql)
}

// TestOffsetZeroAlone: Offset(0) skips nothing. It renders nothing unless there is an ORDER BY
// to attach it to, where OFFSET 0 ROWS is harmless and makes the ORDER BY legal in a subquery.
func TestOffsetZeroAlone(t *testing.T) {
	sql, _ := render(t, NewBuilder().Select("Id").From("users").Offset(0))
	assert.Equal(t, "SELECT [Id] FROM [users]", sql)

	sql, _ = render(t, NewBuilder().Select("Id").From("users").OrderBy("Id", "asc").Offset(0))
	assert.Equal(t, "SELECT [Id] FROM [users] ORDER BY [Id] ASC OFFSET 0 ROWS", sql)
}

func TestNegativeLimitOrOffsetIsRefused(t *testing.T) {
	for name, b := range map[string]dbCore.IQueryBuilder{
		"select limit":  NewBuilder().Select("Id").From("t").Limit(-1),
		"select offset": NewBuilder().Select("Id").From("t").Offset(-5),
		"update limit":  NewBuilder().Update("t").Set("a", 1).Limit(-1),
		"delete limit":  NewBuilder().Delete("t").Limit(-1),
		"subquery":      NewBuilder().Select("Id").From("t").InSubquery("Id", NewBuilder().Select("Id").From("u").Limit(-2).Build()),
	} {
		t.Run(name, func(t *testing.T) {
			sql, args, err := b.ToSQL()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is negative")
			assert.Empty(t, sql)
			assert.Nil(t, args)
		})
	}
}

// TestDistinctOffsetWithoutOrderByIsRefused: SQL Server 2022 refuses
// SELECT DISTINCT … ORDER BY (SELECT NULL) with Msg 145 (ORDER BY items must appear in the
// select list), which was checked against a live server, so the synthesised ORDER BY cannot
// serve a DISTINCT query.
func TestDistinctOffsetWithoutOrderByIsRefused(t *testing.T) {
	_, _, err := distinct(NewBuilder().Select("Status").From("orders").Offset(5)).ToSQL()
	requireUnsupported(t, err, "DISTINCT with an Offset and no ORDER BY")

	_, _, err = distinct(NewBuilder().Select("Status").From("orders").Offset(5).Limit(2)).ToSQL()
	requireUnsupported(t, err, "DISTINCT with an Offset and no ORDER BY")

	sql, _ := render(t, distinct(NewBuilder().Select("Status").From("orders").OrderBy("Status", "asc").Offset(5).Limit(2)))
	assert.Equal(t, "SELECT DISTINCT [Status] FROM [orders] ORDER BY [Status] ASC OFFSET 5 ROWS FETCH NEXT 2 ROWS ONLY", sql)

	sql, _ = render(t, distinct(NewBuilder().Select("Status").From("orders").Limit(0).Offset(5)))
	assert.Equal(t, "SELECT DISTINCT TOP (0) [Status] FROM [orders]", sql, "TOP (0) needs no ORDER BY")

	// Written into the first item, which is how an app asks for DISTINCT, it is refused the
	// same way: the server answers ORDER BY (SELECT NULL) under DISTINCT with Msg 145.
	for _, item := range []string{"DISTINCT Status", "distinct(Status)", "  DISTINCT  Status, Region"} {
		_, _, err = NewBuilder().Select(item).From("orders").Offset(5).Limit(2).ToSQL()
		requireUnsupported(t, err, "DISTINCT with an Offset and no ORDER BY")
	}
}

// TestDistinctWrittenIntoTheFirstItemIsLifted: Select("DISTINCT Status") used to render its
// item verbatim after TOP, as SELECT TOP (2) DISTINCT Status, which is Msg 156. The keyword now
// takes its own place, before TOP, and what is left of the item renders as any item does.
func TestDistinctWrittenIntoTheFirstItemIsLifted(t *testing.T) {
	for _, tc := range []struct {
		fields []string
		limit  int
		want   string
	}{
		{[]string{"DISTINCT Status"}, 2, "SELECT DISTINCT TOP (2) [Status] FROM [orders]"},
		{[]string{"distinct Status"}, -1, "SELECT DISTINCT [Status] FROM [orders]"},
		{[]string{"DISTINCT(Status)", "Region"}, 1, "SELECT DISTINCT TOP (1) (Status), [Region] FROM [orders]"},
		{[]string{"DISTINCT Status, Region"}, 3, "SELECT DISTINCT TOP (3) Status, Region FROM [orders]"},
		{[]string{"DISTINCT", "Status"}, 2, "SELECT DISTINCT TOP (2) [Status] FROM [orders]"},
		{[]string{"DISTINCT o.2024Total AS Total"}, 2, "SELECT DISTINCT TOP (2) [o].[2024Total] AS [Total] FROM [orders]"},
		// Not DISTINCT: a column whose name starts with the word, and a bracketed one.
		{[]string{"DISTINCTIVE"}, 2, "SELECT TOP (2) [DISTINCTIVE] FROM [orders]"},
		{[]string{"[DISTINCT]"}, 2, "SELECT TOP (2) [DISTINCT] FROM [orders]"},
	} {
		b := NewBuilder().Select(tc.fields...).From("orders")
		if tc.limit >= 0 {
			b = b.Limit(tc.limit)
		}
		sql, _ := render(t, b)
		assert.Equal(t, tc.want, sql, "fields %q", tc.fields)
	}

	sql, _ := render(t, distinct(NewBuilder().Select("DISTINCT Status").From("orders").Limit(2)))
	assert.Equal(t, "SELECT DISTINCT TOP (2) [Status] FROM [orders]", sql, "DISTINCT is not doubled")

	sql, _ = render(t, NewBuilder().Select("DISTINCT Status").From("orders").OrderBy("Status", "asc").Offset(1))
	assert.Equal(t, "SELECT DISTINCT [Status] FROM [orders] ORDER BY [Status] ASC OFFSET 1 ROWS", sql)

	_, _, err := NewBuilder().Select("DISTINCT ON (CustomerId) Id").From("orders").ToSQL()
	requireUnsupported(t, err, "DISTINCT ON")
	_, _, err = d().FormatSelect([]string{"distinct on(CustomerId) Id"}, false, nil)
	requireUnsupported(t, err, "DISTINCT ON")
}

// ------------------------------------------------------------------- scopes

// scopeQuery is a query with the features the scope table is about, for one cell at a time.
func scopeQuery(orderBy bool, limit, offset *int, cte bool) *dbCore.Query {
	b := NewBuilder().Select("Id").From("orders")
	if orderBy {
		b = b.OrderBy("Id", "desc")
	}
	if limit != nil {
		b = b.Limit(*limit)
	}
	if offset != nil {
		b = b.Offset(*offset)
	}
	if cte {
		b = b.WithCTE("c", NewBuilder().Select("Id").From("t").Build())
	}
	return b.Build()
}

func intp(n int) *int { return &n }

// TestScopeRulesTable pins one subtest per cell of formatSelect's scope table: whether a SELECT
// in each position may carry its own WITH, an ORDER BY without paging, a Limit and an Offset.
func TestScopeRulesTable(t *testing.T) {
	type cell struct {
		name string
		q    *dbCore.Query
		want string // the rendered SELECT, or "" for a refusal
	}
	cellsFor := func() []cell {
		return []cell{
			{"own WITH", scopeQuery(false, nil, nil, true), "WITH"},
			{"ORDER BY without paging", scopeQuery(true, nil, nil, false), "SELECT [Id] FROM [orders] ORDER BY [Id] DESC"},
			{"Limit only", scopeQuery(false, intp(3), nil, false), "SELECT TOP (3) [Id] FROM [orders]"},
			{"Limit with ORDER BY", scopeQuery(true, intp(3), nil, false), "SELECT TOP (3) [Id] FROM [orders] ORDER BY [Id] DESC"},
			{"Offset", scopeQuery(false, intp(3), intp(6), false), "SELECT [Id] FROM [orders] ORDER BY (SELECT NULL) OFFSET 6 ROWS FETCH NEXT 3 ROWS ONLY"},
		}
	}
	type scopeRow struct {
		scope   selectScope
		refused map[string]bool
		with    string // what "own WITH" renders to, when allowed
	}
	rows := []scopeRow{
		{scopeTop, map[string]bool{}, "WITH [c] AS (SELECT [Id] FROM [t]) SELECT [Id] FROM [orders]"},
		{scopeCTEBody, map[string]bool{"own WITH": true, "ORDER BY without paging": true}, ""},
		{scopeSubquery, map[string]bool{"own WITH": true, "ORDER BY without paging": true}, ""},
		{scopeUnionArm, map[string]bool{"own WITH": true, "ORDER BY without paging": true, "Limit only": true, "Limit with ORDER BY": true, "Offset": true}, ""},
		// An INSERT … SELECT source may be ordered (it orders IDENTITY assignment), and its WITH
		// is hoisted by the INSERT, so the source renders without it.
		{scopeInsertSource, map[string]bool{}, "SELECT [Id] FROM [orders]"},
	}

	for _, row := range rows {
		for _, c := range cellsFor() {
			t.Run(row.scope.String()+"/"+c.name, func(t *testing.T) {
				sql, _, err := d().formatSelect(c.q, row.scope)
				if row.refused[c.name] {
					require.Error(t, err)
					assert.True(t, dbCore.IsUnsupported(err), "%v", err)
					assert.Empty(t, sql)
					return
				}
				require.NoError(t, err)
				want := c.want
				if c.name == "own WITH" {
					want = row.with
				}
				assert.Equal(t, want, sql)
			})
		}
	}
}

// TestNestedCTEIsRefused: T-SQL allows WITH only at the start of a statement.
func TestNestedCTEIsRefused(t *testing.T) {
	inner := NewBuilder().WithCTE("a", NewBuilder().Select("Id").From("t").Build()).Select("Id").From("a").Build()

	_, _, err := NewBuilder().WithCTE("b", inner).Select("Id").From("b").ToSQL()
	requireUnsupported(t, err, "a WITH clause inside a CTE")
	assert.Contains(t, err.Error(), "outermost query")

	_, _, err = NewBuilder().Select("x.Id").Subquery(inner, "x").ToSQL()
	requireUnsupported(t, err, "a WITH clause inside a subquery")

	_, _, err = NewBuilder().Select("Id").From("t").InSubquery("Id", inner).ToSQL()
	requireUnsupported(t, err, "a WITH clause inside a subquery")
}

// TestOrderByInSubqueryWithoutLimitIsRefused: Msg 1033 at the server; refused where it is
// built instead.
func TestOrderByInSubqueryWithoutLimitIsRefused(t *testing.T) {
	ordered := NewBuilder().Select("Id").From("t").OrderBy("Id", "desc").Build()

	_, _, err := NewBuilder().Select("x.Id").Subquery(ordered, "x").ToSQL()
	requireUnsupported(t, err, "ORDER BY in a subquery without Limit or Offset")
	assert.Contains(t, err.Error(), "Msg 1033")

	_, _, err = NewBuilder().Select("Id").From("u").InSubquery("Id", ordered).ToSQL()
	requireUnsupported(t, err, "ORDER BY in a subquery without Limit or Offset")

	_, _, err = NewBuilder().WithCTE("c", ordered).Select("Id").From("c").ToSQL()
	requireUnsupported(t, err, "ORDER BY in a CTE without Limit or Offset")

	sql, _ := render(t, NewBuilder().Select("x.Id").Subquery(NewBuilder().Select("Id").From("t").OrderBy("Id", "desc").Offset(0).Build(), "x"))
	assert.Equal(t, "SELECT [x].[Id] FROM (SELECT [Id] FROM [t] ORDER BY [Id] DESC OFFSET 0 ROWS) AS [x]", sql)
}

func TestInsertSourceMayOrderBy(t *testing.T) {
	src := NewBuilder().Select("CustomerId", "Total").From("staging").OrderBy("Total", "asc").Build()
	sql, args := render(t, NewBuilder().Insert("dbo.2024Orders").Columns("CustomerId", "Total").FromSelect(src))
	assert.Equal(t, "INSERT INTO [dbo].[2024Orders] ([CustomerId], [Total]) SELECT [CustomerId], [Total] FROM [staging] ORDER BY [Total] ASC", sql)
	assert.Empty(t, args)
}

func TestUnion(t *testing.T) {
	other := NewBuilder().Select("Id").From("archived_users").Eq("Kind", 2).Build()

	sql, args := render(t, NewBuilder().Select("Id").From("users").Eq("Kind", 1).Union(other))
	assert.Equal(t, "SELECT [Id] FROM [users] WHERE [Kind] = ? UNION SELECT [Id] FROM [archived_users] WHERE [Kind] = ?", sql)
	assert.Equal(t, []any{1, 2}, args)

	sql, _ = render(t, NewBuilder().Select("Id").From("users").UnionAll(other).Union(other))
	assert.Equal(t, "SELECT [Id] FROM [users] UNION ALL SELECT [Id] FROM [archived_users] WHERE [Kind] = ? "+
		"UNION SELECT [Id] FROM [archived_users] WHERE [Kind] = ?", sql)

	sql, _, err := d().FormatUnion(other, true)
	require.NoError(t, err)
	assert.Equal(t, "UNION ALL SELECT [Id] FROM [archived_users] WHERE [Kind] = ?", sql)
}

// TestUnionWithOrderByOrLimitIsRefused: the builder puts ORDER BY and Limit on the query that
// holds the UNIONs, which cannot say whether they were meant for its own arm or the whole.
func TestUnionWithOrderByOrLimitIsRefused(t *testing.T) {
	other := NewBuilder().Select("Id").From("archived").Build()
	for name, b := range map[string]dbCore.IQueryBuilder{
		"ORDER BY": NewBuilder().Select("Id").From("users").Union(other).OrderBy("Id", "asc"),
		"Limit":    NewBuilder().Select("Id").From("users").Union(other).Limit(5),
		"Offset":   NewBuilder().Select("Id").From("users").Union(other).Offset(5),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := b.ToSQL()
			requireUnsupported(t, err, "ORDER BY, Limit or Offset on a query with UNION")
			assert.Contains(t, err.Error(), "derived table")
		})
	}

	// The remedy: page the UNION as a derived table.
	union := NewBuilder().Select("Id").From("users").Union(other).Build()
	sql, _ := render(t, NewBuilder().Select("u.Id").Subquery(union, "u").OrderBy("u.Id", "asc").Limit(5))
	assert.Equal(t, "SELECT TOP (5) [u].[Id] FROM (SELECT [Id] FROM [users] UNION SELECT [Id] FROM [archived]) AS [u] ORDER BY [u].[Id] ASC", sql)
}

func TestUnionArmMustBeBare(t *testing.T) {
	for name, arm := range map[string]*dbCore.Query{
		"ORDER BY": NewBuilder().Select("Id").From("a").OrderBy("Id", "asc").Build(),
		"Limit":    NewBuilder().Select("Id").From("a").Limit(1).Build(),
		"Offset":   NewBuilder().Select("Id").From("a").Offset(1).Build(),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := NewBuilder().Select("Id").From("users").Union(arm).ToSQL()
			requireUnsupported(t, err, "ORDER BY, Limit or Offset on a UNION arm")
		})
	}

	nested := NewBuilder().Select("Id").From("a").Union(NewBuilder().Select("Id").From("b").Build()).Build()
	_, _, err := NewBuilder().Select("Id").From("users").Union(nested).ToSQL()
	requireUnsupported(t, err, "a UNION nested in a UNION arm")

	withCTE := NewBuilder().WithCTE("c", NewBuilder().Select("Id").From("t").Build()).Select("Id").From("c").Build()
	_, _, err = NewBuilder().Select("Id").From("users").Union(withCTE).ToSQL()
	requireUnsupported(t, err, "a WITH clause inside a UNION arm")
}

func TestDerivedTableRequiresAlias(t *testing.T) {
	sub := NewBuilder().Select("Id").From("t").Build()
	_, _, err := NewBuilder().Select("Id").Subquery(sub, "").ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires an alias")

	_, _, err = NewBuilder().Select("Id").Subquery(sub, "x; DROP TABLE t").ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subquery alias")

	sql, _, err := d().FormatSubquery(sub, "")
	require.NoError(t, err)
	assert.Equal(t, "(SELECT [Id] FROM [t])", sql, "a condition subquery has no alias")

	sql, _, err = d().FormatSubquery(sub, "Order")
	require.NoError(t, err)
	assert.Equal(t, "(SELECT [Id] FROM [t]) AS [Order]", sql)
}

func TestFormatCTE(t *testing.T) {
	sql, args, err := d().FormatCTE("recent", NewBuilder().Select("Id").From("orders").Eq("Kind", 1).Build())
	require.NoError(t, err)
	assert.Equal(t, "[recent] AS (SELECT [Id] FROM [orders] WHERE [Kind] = ?)", sql)
	assert.Equal(t, []any{1}, args)

	_, _, err = d().FormatCTE("bad name", NewBuilder().Select("Id").From("orders").Build())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CTE name")

	_, _, err = d().FormatCTE("c", nil)
	require.Error(t, err)

	// A data-modifying CTE, which Postgres has, is refused.
	_, _, err = d().FormatCTE("gone", NewBuilder().Delete("t").Build())
	requireUnsupported(t, err, "a DELETE as a CTE")
}

// ------------------------------------------------------------------- window

func TestFormatWindow(t *testing.T) {
	sql, args, err := d().FormatWindow("w", &dbCore.WindowDefinition{
		PartitionBy: []string{"CustomerId", "o.2024Kind"},
		OrderBy:     []dbCore.OrderByField{{Field: "CreatedAt", Direction: "desc"}, {Field: "LEN(Name)", Raw: true}},
		Frame: &dbCore.WindowFrame{
			Type:  "rows",
			Start: &dbCore.FrameBound{Type: "PRECEDING", Value: 2},
			End:   &dbCore.FrameBound{Type: "current  row"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "[w] AS (PARTITION BY [CustomerId], [o].[2024Kind] ORDER BY [CreatedAt] DESC, LEN(Name) ASC "+
		"ROWS BETWEEN 2 PRECEDING AND CURRENT ROW)", sql)
	assert.Empty(t, args)

	sql, _, err = d().FormatWindow("w", &dbCore.WindowDefinition{
		Frame: &dbCore.WindowFrame{Type: "RANGE", Start: &dbCore.FrameBound{Type: "UNBOUNDED PRECEDING"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "[w] AS (RANGE UNBOUNDED PRECEDING)", sql)

	// Any integer type is an offset, a named one included.
	type rows uint16
	for _, n := range []any{int64(3), uint(3), rows(3), int8(3)} {
		sql, _, err = d().FormatWindow("w", &dbCore.WindowDefinition{
			Frame: &dbCore.WindowFrame{Type: "ROWS", Start: &dbCore.FrameBound{Type: "FOLLOWING", Value: n}},
		})
		require.NoError(t, err)
		assert.Equal(t, "[w] AS (ROWS 3 FOLLOWING)", sql)
	}

	sql, _, err = d().FormatWindow("w", nil)
	require.NoError(t, err)
	assert.Empty(t, sql)
}

func TestFormatWindowRefusesWhatTSQLCannotSay(t *testing.T) {
	window := func(def *dbCore.WindowDefinition) error {
		_, _, err := d().FormatWindow("w", def)
		return err
	}

	requireUnsupported(t, window(&dbCore.WindowDefinition{PartitionBy: []string{"YEAR(CreatedAt)"}}),
		"a window PARTITION BY item that is not a column")
	requireUnsupported(t, window(&dbCore.WindowDefinition{OrderBy: []dbCore.OrderByField{{Field: "LEN(Name)"}}}),
		"ORDER BY on something that is not a column")
	requireUnsupported(t, window(&dbCore.WindowDefinition{Frame: &dbCore.WindowFrame{Type: "GROUPS", Start: &dbCore.FrameBound{Type: "CURRENT ROW"}}}),
		"a GROUPS window frame")
	requireUnsupported(t, window(&dbCore.WindowDefinition{Frame: &dbCore.WindowFrame{Type: "ROWS", Start: &dbCore.FrameBound{Type: "CURRENT ROW"}, Exclusion: "EXCLUDE TIES"}}),
		"a window frame EXCLUDE clause")
	requireUnsupported(t, window(&dbCore.WindowDefinition{Frame: &dbCore.WindowFrame{Type: "RANGE", Start: &dbCore.FrameBound{Type: "PRECEDING", Value: 1}}}),
		"a RANGE frame with an offset")

	for _, def := range []*dbCore.WindowDefinition{
		{Frame: &dbCore.WindowFrame{Type: "ROWS", Start: &dbCore.FrameBound{Type: "PRECEDING", Value: "1; DROP TABLE t"}}},
		{Frame: &dbCore.WindowFrame{Type: "ROWS", Start: &dbCore.FrameBound{Type: "PRECEDING", Value: -1}}},
		{Frame: &dbCore.WindowFrame{Type: "ROWS", Start: &dbCore.FrameBound{Type: "SIDEWAYS"}}},
		{Frame: &dbCore.WindowFrame{Type: "ROWS"}},
		{Frame: &dbCore.WindowFrame{Type: "SLICES", Start: &dbCore.FrameBound{Type: "CURRENT ROW"}}},
	} {
		require.Error(t, window(def))
	}

	_, _, err := d().FormatWindow("ROW_NUMBER()", &dbCore.WindowDefinition{PartitionBy: []string{"a"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "window name")
}

// TestBuilderWindowsRenderThroughTheDialect: the builder's Over(name) inlines core's text for a
// window into the select item, which brackets nothing and knows nothing of T-SQL, so the
// dialect renders the query's windows itself. What FormatWindow refuses is refused on the
// builder's path too, and what it renders replaces core's text.
func TestBuilderWindowsRenderThroughTheDialect(t *testing.T) {
	ranked := func(def *dbCore.WindowDefinition) dbCore.IQueryBuilder {
		b := NewBuilder().Window("ROW_NUMBER()", def)
		return b.Select("Id", b.Over("ROW_NUMBER()")+" AS rn").From("dbo.2024Orders")
	}

	sql, args := render(t, ranked(&dbCore.WindowDefinition{
		PartitionBy: []string{"CustomerId"},
		OrderBy:     []dbCore.OrderByField{{Field: "Order", Direction: "desc"}},
		Frame: &dbCore.WindowFrame{Type: "ROWS", Start: &dbCore.FrameBound{Type: "PRECEDING", Value: 1},
			End: &dbCore.FrameBound{Type: "CURRENT ROW"}},
	}))
	assert.Equal(t, "SELECT [Id], ROW_NUMBER() OVER (PARTITION BY [CustomerId] ORDER BY [Order] DESC "+
		"ROWS BETWEEN 1 PRECEDING AND CURRENT ROW) AS rn FROM [dbo].[2024Orders]", sql)
	assert.Empty(t, args)

	for construct, def := range map[string]*dbCore.WindowDefinition{
		"a window PARTITION BY item that is not a column": {PartitionBy: []string{"YEAR(CreatedAt)"}},
		"a GROUPS window frame": {OrderBy: []dbCore.OrderByField{{Field: "Order"}},
			Frame: &dbCore.WindowFrame{Type: "GROUPS", Start: &dbCore.FrameBound{Type: "PRECEDING", Value: 1}}},
		"a window frame EXCLUDE clause": {Frame: &dbCore.WindowFrame{Type: "ROWS",
			Start: &dbCore.FrameBound{Type: "CURRENT ROW"}, Exclusion: "EXCLUDE TIES"}},
		"a RANGE frame with an offset": {Frame: &dbCore.WindowFrame{Type: "RANGE",
			Start: &dbCore.FrameBound{Type: "PRECEDING", Value: 1}}},
	} {
		_, _, err := ranked(def).ToSQL()
		requireUnsupported(t, err, construct)
	}

	// A window no select item uses is still one the query defines, and still has to be sayable.
	_, _, err := NewBuilder().Window("w", &dbCore.WindowDefinition{Frame: &dbCore.WindowFrame{Type: "GROUPS",
		Start: &dbCore.FrameBound{Type: "CURRENT ROW"}}}).Select("Id").From("t").ToSQL()
	requireUnsupported(t, err, "a GROUPS window frame")

	// A window in a subquery renders with its query.
	inner := ranked(&dbCore.WindowDefinition{PartitionBy: []string{"CustomerId"}}).Build()
	sql, _ = render(t, NewBuilder().Select("Id").Subquery(inner, "r"))
	assert.Equal(t, "SELECT [Id] FROM (SELECT [Id], ROW_NUMBER() OVER (PARTITION BY [CustomerId]) AS rn "+
		"FROM [dbo].[2024Orders]) AS [r]", sql)
}

// TestAggregateFilterIsRefused: core.AggregateFunction renders FILTER (WHERE …) into a SELECT
// item, and T-SQL has no FILTER.
func TestAggregateFilterIsRefused(t *testing.T) {
	agg := (&dbCore.AggregateFunction{Name: "COUNT", Arguments: []string{"*"}, Filter: &dbCore.RawCondition{SQL: "Status = 1"}}).String()
	_, _, err := NewBuilder().Select(agg + " AS n").From("orders").ToSQL()
	requireUnsupported(t, err, "an aggregate FILTER (WHERE …) clause")
	assert.Contains(t, err.Error(), "CASE WHEN")

	sql, _ := render(t, NewBuilder().Select("SUM(CASE WHEN Status = 1 THEN 1 END) AS n").From("orders"))
	assert.Equal(t, "SELECT SUM(CASE WHEN Status = 1 THEN 1 END) AS n FROM [orders]", sql)
}

// --------------------------------------------------------- RETURNING / caps

func TestFormatReturningIsOutputInserted(t *testing.T) {
	sql, args, err := d().FormatReturning([]string{"Id", "CreatedAt"})
	require.NoError(t, err)
	assert.Equal(t, "OUTPUT INSERTED.[Id], INSERTED.[CreatedAt]", sql)
	assert.Empty(t, args)

	sql, _, err = d().FormatReturning([]string{"*"})
	require.NoError(t, err)
	assert.Equal(t, "OUTPUT INSERTED.*", sql)
}

func TestReturningOnSelectIsRefused(t *testing.T) {
	_, _, err := NewBuilder().Select("Id").From("users").Returning("Id").ToSQL()
	requireUnsupported(t, err, "RETURNING on a SELECT")
	assert.Contains(t, err.Error(), "OUTPUT")
}

// TestTooManyParametersIsRefused: SQL Server takes 2100 parameters per request and
// sp_executesql uses two, so 2098 is the most a statement may bind. 2098 and 2099 were both
// sent to a live server: the first ran, the second failed with Msg 8003.
func TestTooManyParametersIsRefused(t *testing.T) {
	ids := make([]any, MaxBindParameters)
	for i := range ids {
		ids[i] = i
	}

	sql, args := render(t, NewBuilder().Select("Id").From("t").In("Id", ids...))
	assert.Len(t, args, 2098)
	assert.Equal(t, 2098, strings.Count(sql, "?"))

	sql, args, err := NewBuilder().Select("Id").From("t").In("Id", append(ids, 2098)...).ToSQL()
	requireUnsupported(t, err, "a statement with 2099 bound parameters")
	assert.Contains(t, err.Error(), "at most 2098")
	assert.Empty(t, sql)
	assert.Nil(t, args)

	// The cap counts every bound value, wherever it sits.
	rows := NewBuilder().Insert("t").Columns("a", "b", "c")
	for i := 0; i < 700; i++ {
		rows = rows.Values(i, i, i)
	}
	_, _, err = rows.ToSQL()
	requireUnsupported(t, err, "a statement with 2100 bound parameters")
}

// ---------------------------------------------------------------- read-only

func TestReadOnlyRefusesEveryWrite(t *testing.T) {
	ro := NewBuilderWithDialect(&SQLServerDialect{ReadOnly: true})
	tests := map[string]struct {
		b         dbCore.IQueryBuilder
		statement string
	}{
		"INSERT":            {ro.Insert("t").Columns("a").Values(1), "INSERT"},
		"INSERT SELECT":     {ro.Insert("t").Columns("a").FromSelect(NewBuilder().Select("a").From("s").Build()), "INSERT"},
		"INSERT DEFAULT":    {ro.Insert("t"), "INSERT"},
		"INSERT RETURNING":  {ro.Insert("t").Columns("a").Values(1).Returning("Id"), "INSERT"},
		"DO NOTHING":        {ro.Insert("t").Columns("a").Values(1).OnConflict("a").DoNothing(), "INSERT ... ON CONFLICT"},
		"DO UPDATE":         {ro.Insert("t").Columns("a").Values(1).OnConflict("a").DoUpdate(map[string]any{"a": 2}), "INSERT ... ON CONFLICT"},
		"UPDATE":            {ro.Update("t").Set("a", 1).Eq("Id", 1), "UPDATE"},
		"DELETE":            {ro.Delete("t").Eq("Id", 1), "DELETE"},
		"unrenderable":      {ro.Update("t").Set("a", 1).OrderBy("a", "asc"), "UPDATE"},
		"write in a CTE":    {ro.WithCTE("gone", NewBuilder().Delete("t").Build()).Select("Id").From("gone"), "DELETE"},
		"write in UNION":    {ro.Select("Id").From("t").Union(NewBuilder().Insert("t").Columns("a").Values(1).Build()), "INSERT"},
		"write in subquery": {ro.Select("Id").From("t").InSubquery("Id", NewBuilder().Update("t").Set("a", 1).Build()), "UPDATE"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			sql, args, err := tt.b.ToSQL()
			require.Error(t, err)
			assert.ErrorIs(t, err, dbCore.ErrReadOnly)
			assert.Contains(t, err.Error(), "sqlserver refuses "+tt.statement+" on a read_only datasource")
			assert.Empty(t, sql)
			assert.Nil(t, args)
		})
	}
}

func TestReadOnlyStillRendersSelect(t *testing.T) {
	ro := &SQLServerDialect{ReadOnly: true}
	build := func(dialect dbCore.SQLDialect) dbCore.IQueryBuilder {
		return NewBuilderWithDialect(dialect).Select("Id").From("dbo.2024Orders").Eq("Status", "open").OrderBy("Id", "asc").Offset(10).Limit(5)
	}
	want, wantArgs := render(t, build(d()))
	got, gotArgs := render(t, build(ro))
	assert.Equal(t, want, got)
	assert.Equal(t, wantArgs, gotArgs)
	assert.Equal(t, "SELECT [Id] FROM [dbo].[2024Orders] WHERE [Status] = ? ORDER BY [Id] ASC OFFSET 10 ROWS FETCH NEXT 5 ROWS ONLY", got)

	// Refusals of reads are the same as well.
	_, _, err := NewBuilderWithDialect(ro).Select("Id").From("t").Returning("Id").ToSQL()
	requireUnsupported(t, err, "RETURNING on a SELECT")
	assert.NotErrorIs(t, err, dbCore.ErrReadOnly)
}

func TestNestedWriteIsRefusedWhenNotReadOnly(t *testing.T) {
	_, _, err := NewBuilder().Select("Id").From("t").InSubquery("Id", NewBuilder().Update("t").Set("a", 1).Build()).ToSQL()
	requireUnsupported(t, err, "an UPDATE as a subquery")

	_, _, err = NewBuilder().Select("Id").From("t").Union(NewBuilder().Insert("t").Columns("a").Values(1).Build()).ToSQL()
	requireUnsupported(t, err, "an INSERT as a UNION arm")
}

// TestRenderedReadsPassTheReadOnlyGuard pins the contract with the connection guard: on a
// read_only datasource the executor marks builder SQL as dialect-rendered and the guard checks
// it with dbCore.GuardReadOnlyShape under T-SQL's lexicon, which reads it word by word. So no
// read this dialect renders may contain a word that guard refuses — FETCH NEXT, APPLY and the
// synthesised (SELECT NULL) included — and every write it renders must be one the guard
// refuses, should one ever get past the dialect's own refusal.
func TestRenderedReadsPassTheReadOnlyGuard(t *testing.T) {
	sub := NewBuilder().Select("CustomerId").From("dbo.2024Orders").Eq("Status", "open").OrderBy("Total", "desc").Limit(3).Build()
	reads := map[string]dbCore.IQueryBuilder{
		"top":         NewBuilder().Select("Id").From("dbo.2024Orders").Limit(1),
		"offset":      NewBuilder().Select("Id").From("[dbo].[Order]").Offset(10).Limit(5),
		"subquery":    NewBuilder().Select("Id").From("dbo.Customer").InSubquery("Id", sub).NotExists(sub),
		"apply":       NewBuilder().Select("c.Id").From("dbo.Customer c").LateralJoin(sub, "x", nil),
		"cte":         NewBuilder().WithCTE("c", sub).Select("CustomerId").From("c"),
		"grouping":    NewBuilder().Select("a", "COUNT(*) AS n").From("t").Rollup("a").Having(&dbCore.BinaryCondition{Left: dbCore.Raw("COUNT(*)"), Operator: ">", Right: 1}),
		"union":       NewBuilder().Select("Id").From("a").UnionAll(NewBuilder().Select("Id").From("b").Build()),
		"joins":       NewBuilder().Select("u.Id").From("users u").LeftJoin("dbo.Order o", &dbCore.BinaryCondition{Left: "o.User", Operator: "=", Right: dbCore.Identifier("u.Id")}),
		"reserved":    NewBuilder().Select("Update", "Delete", "Insert", "Merge").From("dbo.Exec").Eq("Into", 1).OrderBy("Set", "asc"),
		"empty in":    NewBuilder().Select("Id").From("t").In("Id"),
		"derived":     NewBuilder().Select("x.Id").Subquery(sub, "x"),
		"like escape": NewBuilder().Select("Id").From("t").LikeEscape("Name", "a!%", "!"),
	}
	for name, b := range reads {
		t.Run("read/"+name, func(t *testing.T) {
			sql, _ := render(t, b)
			assert.NoError(t, dbCore.GuardReadOnlyShape(sql, dbCore.LexiconTSQL), sql)
		})
	}

	writes := map[string]dbCore.IQueryBuilder{
		"insert":     NewBuilder().Insert("t").Columns("a").Values(1).Returning("Id"),
		"default":    NewBuilder().Insert("t"),
		"do nothing": NewBuilder().Insert("t").Columns("a").Values(1).OnConflict("a").DoNothing(),
		"merge":      NewBuilder().Insert("t").Columns("a", "b").Values(1, 2).OnConflict("a").DoUpdate(map[string]any{"b": 3}),
		"update":     NewBuilder().Update("t").Set("a", 1).Limit(1),
		"delete":     NewBuilder().Delete("t").Eq("Id", 1).Returning("*"),
		"cte update": NewBuilder().WithCTE("c", sub).Update("c").Set("Status", "x"),
	}
	for name, b := range writes {
		t.Run("write/"+name, func(t *testing.T) {
			sql, _ := render(t, b)
			assert.ErrorIs(t, dbCore.GuardReadOnlyShape(sql, dbCore.LexiconTSQL), dbCore.ErrReadOnly, sql)
		})
	}
}
