package v2

import (
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The writes, pinned as exact SQL and args. Every shape here — OUTPUT on each statement, the
// DO NOTHING insert run twice, the MERGE run twice, UPDATE TOP and DELETE TOP — was also run
// against SQL Server 2022 while the dialect was written.

// ------------------------------------------------------------------- INSERT

func TestFormatQueryInsert(t *testing.T) {
	sql, args := render(t, NewBuilder().Insert("users").Columns("Id", "Name").Values(1, "ann"))
	assert.Equal(t, "INSERT INTO [users] ([Id], [Name]) VALUES (?, ?)", sql)
	assert.Equal(t, []any{1, "ann"}, args)

	sql, args = render(t, NewBuilder().Insert("t").Columns("a").Values(1).Values(2).Values(3))
	assert.Equal(t, "INSERT INTO [t] ([a]) VALUES (?), (?), (?)", sql)
	assert.Equal(t, []any{1, 2, 3}, args)

	// With no column list, every row must be as wide as the first.
	sql, args = render(t, NewBuilder().Insert("t").Values(1, 2).Values(3, 4))
	assert.Equal(t, "INSERT INTO [t] VALUES (?, ?), (?, ?)", sql)
	assert.Equal(t, []any{1, 2, 3, 4}, args)
}

func TestInsertIdentifierQuoting(t *testing.T) {
	sql, args := render(t, NewBuilder().Insert("dbo.2024Orders").Columns("2024Total", "Order", "User", "[Order Date]").Values(1, 2, 3, 4))
	assert.Equal(t, "INSERT INTO [dbo].[2024Orders] ([2024Total], [Order], [User], [Order Date]) VALUES (?, ?, ?, ?)", sql)
	assert.Equal(t, []any{1, 2, 3, 4}, args)

	sql, _ = render(t, NewBuilder().Insert("#staging").Columns("a").Values(1))
	assert.Equal(t, "INSERT INTO #staging ([a]) VALUES (?)", sql, "a #temp table is emitted as written")

	for _, column := range []string{"t.a", "COUNT(*)", "a b", "1", "a?"} {
		_, _, err := NewBuilder().Insert("t").Columns(column).Values(1).ToSQL()
		require.Errorf(t, err, "column %q", column)
		assert.Contains(t, err.Error(), "INSERT column")
	}

	_, _, err := NewBuilder().Insert("users u").Columns("a").Values(1).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "carries an alias")

	_, _, err = NewBuilder().Insert("").Columns("a").Values(1).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "names no table")
}

// TestInsertDefaultValues: the ORM inserts a row whose every column has a default as
// Columns().Values(), which T-SQL spells DEFAULT VALUES.
func TestInsertDefaultValues(t *testing.T) {
	for name, b := range map[string]dbCore.IQueryBuilder{
		"no values":       NewBuilder().Insert("dbo.Defaults"),
		"one empty row":   NewBuilder().Insert("dbo.Defaults").Columns().Values(),
		"with RETURNING":  NewBuilder().Insert("dbo.Defaults").Columns().Values().Returning("Id"),
		"RETURNING star":  NewBuilder().Insert("dbo.Defaults").Returning("*"),
		"RETURNING twice": NewBuilder().Insert("dbo.Defaults").Returning("Id", "CreatedAt"),
	} {
		t.Run(name, func(t *testing.T) {
			sql, args := render(t, b)
			assert.Empty(t, args)
			switch name {
			case "with RETURNING":
				assert.Equal(t, "INSERT INTO [dbo].[Defaults] OUTPUT INSERTED.[Id] DEFAULT VALUES", sql)
			case "RETURNING star":
				assert.Equal(t, "INSERT INTO [dbo].[Defaults] OUTPUT INSERTED.* DEFAULT VALUES", sql)
			case "RETURNING twice":
				assert.Equal(t, "INSERT INTO [dbo].[Defaults] OUTPUT INSERTED.[Id], INSERTED.[CreatedAt] DEFAULT VALUES", sql)
			default:
				assert.Equal(t, "INSERT INTO [dbo].[Defaults] DEFAULT VALUES", sql)
			}
		})
	}

	_, _, err := NewBuilder().Insert("t").Columns("a").ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gives no values")

	_, _, err = NewBuilder().Insert("t").Values().Values().ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DEFAULT VALUES inserts one row")
}

// TestInsertOutputPlacement: OUTPUT goes between the column list and the source, where T-SQL
// takes it on an INSERT.
func TestInsertOutputPlacement(t *testing.T) {
	sql, args := render(t, NewBuilder().Insert("dbo.2024Orders").Columns("CustomerId", "Total").Values(1, 2).Values(3, 4).Returning("Id", "INSERTED.CreatedAt"))
	assert.Equal(t, "INSERT INTO [dbo].[2024Orders] ([CustomerId], [Total]) OUTPUT INSERTED.[Id], INSERTED.[CreatedAt] VALUES (?, ?), (?, ?)", sql)
	assert.Equal(t, []any{1, 2, 3, 4}, args)

	src := NewBuilder().Select("a").From("s").Build()
	sql, _ = render(t, NewBuilder().Insert("t").Columns("a").FromSelect(src).Returning("Id"))
	assert.Equal(t, "INSERT INTO [t] ([a]) OUTPUT INSERTED.[Id] SELECT [a] FROM [s]", sql)
}

func TestInsertRowWidthMismatch(t *testing.T) {
	_, _, err := NewBuilder().Insert("t").Columns("a", "b").Values(1).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VALUES row 1 has 1 values for 2 columns")

	_, _, err = NewBuilder().Insert("t").Columns("a", "b").Values(1, 2).Values(3, 4, 5).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VALUES row 2 has 3 values for 2 columns")

	_, _, err = NewBuilder().Insert("t").Values(1, 2).Values(3).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VALUES row 2 has 1 values for 2 columns")
}

// TestInsertMoreThan1000RowsIsRefused: a VALUES list takes at most 1000 rows (Msg 10738).
func TestInsertMoreThan1000RowsIsRefused(t *testing.T) {
	b := NewBuilder().Insert("t").Columns("a")
	for i := 0; i < MaxInsertRows; i++ {
		b = b.Values(i)
	}
	_, args := render(t, b)
	assert.Len(t, args, 1000)

	_, _, err := b.Values(1000).ToSQL()
	requireUnsupported(t, err, "an INSERT of 1001 rows")
	assert.Contains(t, err.Error(), "Msg 10738")
}

// TestUpsertOfMoreThan1000RowsIsNotRefused: the 1000-row limit is INSERT … VALUES's. Either
// upsert reads its VALUES list as a derived table, which has none, so only the bind-parameter
// cap bounds it: 1500 one-column rows fit, and 1050 two-column ones do not.
func TestUpsertOfMoreThan1000RowsIsNotRefused(t *testing.T) {
	tags := NewBuilder().Insert("Tag").Columns("Name")
	for i := 0; i < 1500; i++ {
		tags = tags.Values(i)
	}
	for name, b := range map[string]dbCore.IQueryBuilder{
		"DO NOTHING": tags.OnConflict("Name").DoNothing(),
		"DO UPDATE":  tags.OnConflict("Name").DoUpdate(map[string]any{"Seen": 1}),
	} {
		sql, args, err := b.ToSQL()
		require.NoErrorf(t, err, "%s of 1500 rows", name)
		assert.Contains(t, sql, " (VALUES (?), (?), ", name)
		assert.GreaterOrEqual(t, len(args), 1500, name)
	}

	pairs := NewBuilder().Insert("Tag").Columns("Name", "Kind")
	for i := 0; i < 1050; i++ {
		pairs = pairs.Values(i, i)
	}
	_, _, err := pairs.OnConflict("Name").DoNothing().ToSQL()
	requireUnsupported(t, err, "a statement with 2100 bound parameters")
}

// TestInsertIgnoredClausesAreRefused: the other dialects drop clauses an INSERT does not take.
// Dropped silently, a Limit or a WHERE changes what the caller asked for, so they are refused.
func TestInsertIgnoredClausesAreRefused(t *testing.T) {
	base := func() dbCore.IQueryBuilder { return NewBuilder().Insert("t").Columns("a").Values(1) }
	tests := map[string]struct {
		b         dbCore.IQueryBuilder
		construct string
	}{
		"WHERE":    {base().Eq("a", 1), "WHERE on an INSERT"},
		"JOIN":     {base().InnerJoin("u", &dbCore.RawCondition{SQL: "1=1"}), "JOIN on an INSERT"},
		"GROUP BY": {base().GroupBy("a"), "GROUP BY on an INSERT"},
		"HAVING":   {base().Having(&dbCore.RawCondition{SQL: "1=1"}), "HAVING on an INSERT"},
		"ORDER BY": {base().OrderBy("a", "asc"), "ORDER BY on an INSERT"},
		"Limit":    {base().Limit(1), "Limit on an INSERT"},
		"Offset":   {base().Offset(1), "Offset on an INSERT"},
		"UNION":    {base().Union(NewBuilder().Select("a").From("s").Build()), "UNION on an INSERT"},
		"SELECT":   {base().Select("a"), "a SELECT list on an INSERT"},
		"FROM":     {base().From("s"), "FROM on an INSERT"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			sql, args, err := tt.b.ToSQL()
			requireUnsupported(t, err, tt.construct)
			assert.Contains(t, err.Error(), "FromSelect")
			assert.Empty(t, sql)
			assert.Nil(t, args)
		})
	}
}

func TestInsertFromSelect(t *testing.T) {
	src := NewBuilder().Select("Id", "Name").From("staging_users").Eq("Ok", true).Limit(10).Build()
	sql, args := render(t, NewBuilder().Insert("users").Columns("Id", "Name").FromSelect(src))
	assert.Equal(t, "INSERT INTO [users] ([Id], [Name]) SELECT TOP (10) [Id], [Name] FROM [staging_users] WHERE [Ok] = ?", sql)
	assert.Equal(t, []any{true}, args)

	_, _, err := d().FormatQuery(&dbCore.Query{Insert: &dbCore.InsertClause{
		Table: "t", Columns: []string{"a"}, Values: [][]any{{1}}, FromQuery: src,
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both VALUES and a SELECT")
}

// TestInsertSelectHoistsCTEs: T-SQL takes WITH only at the start of a statement, so the
// source's CTEs move in front of the INSERT, after any the INSERT carries itself.
func TestInsertSelectHoistsCTEs(t *testing.T) {
	src := NewBuilder().
		WithCTE("big", NewBuilder().Select("CustomerId", "Total").From("dbo.2024Orders").Gt("Total", 55).Build()).
		Select("CustomerId", "Total").From("big").OrderBy("Total", "asc").Build()

	sql, args := render(t, NewBuilder().
		WithCTE("own", NewBuilder().Select("Id").From("x").Eq("k", "own").Build()).
		Insert("dbo.2024Orders").Columns("CustomerId", "Total").FromSelect(src).Returning("Id"))
	assert.Equal(t,
		"WITH [own] AS (SELECT [Id] FROM [x] WHERE [k] = ?), "+
			"[big] AS (SELECT [CustomerId], [Total] FROM [dbo].[2024Orders] WHERE [Total] > ?) "+
			"INSERT INTO [dbo].[2024Orders] ([CustomerId], [Total]) OUTPUT INSERTED.[Id] "+
			"SELECT [CustomerId], [Total] FROM [big] ORDER BY [Total] ASC",
		sql)
	assert.Equal(t, []any{"own", 55}, args)
	assert.Len(t, src.CTEs, 1, "hoisting leaves the caller's query alone")
}

// ------------------------------------------------------------------ upserts

func TestDoNothingIsInsertWhereNotExists(t *testing.T) {
	sql, args := render(t, NewBuilder().Insert("user_roles").Columns("UserId", "RoleId").Values(1, 7).
		OnConflict("UserId", "RoleId").DoNothing())
	assert.Equal(t,
		"INSERT INTO [user_roles] ([UserId], [RoleId]) "+
			"SELECT [src].[UserId], [src].[RoleId] FROM (VALUES (?, ?)) AS [src] ([UserId], [RoleId]) "+
			"WHERE NOT EXISTS (SELECT 1 FROM [user_roles] AS [tgt] WITH (UPDLOCK, HOLDLOCK) "+
			"WHERE [tgt].[UserId] = [src].[UserId] AND [tgt].[RoleId] = [src].[RoleId])",
		sql)
	assert.Equal(t, []any{1, 7}, args, "each value is bound once")
}

func TestDoNothingMultiRow(t *testing.T) {
	sql, args := render(t, NewBuilder().Insert("dbo.Order").Columns("Key", "Name").Values("a", 1).Values("b", 2).
		OnConflict("key").DoNothing().Returning("Id"))
	// The conflict column is matched as the INSERT spells it.
	assert.Equal(t,
		"INSERT INTO [dbo].[Order] ([Key], [Name]) OUTPUT INSERTED.[Id] "+
			"SELECT [src].[Key], [src].[Name] FROM (VALUES (?, ?), (?, ?)) AS [src] ([Key], [Name]) "+
			"WHERE NOT EXISTS (SELECT 1 FROM [dbo].[Order] AS [tgt] WITH (UPDLOCK, HOLDLOCK) WHERE [tgt].[Key] = [src].[Key])",
		sql)
	assert.Equal(t, []any{"a", 1, "b", 2}, args)
}

func TestDoUpdateIsMerge(t *testing.T) {
	sql, args := render(t, NewBuilder().Insert("dbo.Settings").Columns("Key", "Value", "Scope").Values("theme", "dark", 1).
		OnConflict("Key", "Scope").DoUpdate(map[string]any{"Value": "dark", "Changed": 5}))
	assert.Equal(t,
		"MERGE INTO [dbo].[Settings] WITH (HOLDLOCK) AS [tgt] "+
			"USING (VALUES (?, ?, ?)) AS [src] ([Key], [Value], [Scope]) "+
			"ON [tgt].[Key] = [src].[Key] AND [tgt].[Scope] = [src].[Scope] "+
			"WHEN MATCHED THEN UPDATE SET [Changed] = ?, [Value] = ? "+
			"WHEN NOT MATCHED THEN INSERT ([Key], [Value], [Scope]) VALUES ([src].[Key], [src].[Value], [src].[Scope]);",
		sql)
	assert.Equal(t, []any{"theme", "dark", 1, 5, "dark"}, args, "source values first, then SET in sorted order")
}

func TestMergeWithOutput(t *testing.T) {
	sql, args := render(t, NewBuilder().Insert("dbo.Settings").Columns("Key", "Value").Values("a", "1").Values("b", "2").
		OnConflict("Key").DoUpdate(map[string]any{"Value": "x"}).Returning("Key", "DELETED.Value", "INSERTED.Value"))
	assert.Equal(t,
		"MERGE INTO [dbo].[Settings] WITH (HOLDLOCK) AS [tgt] USING (VALUES (?, ?), (?, ?)) AS [src] ([Key], [Value]) "+
			"ON [tgt].[Key] = [src].[Key] WHEN MATCHED THEN UPDATE SET [Value] = ? "+
			"WHEN NOT MATCHED THEN INSERT ([Key], [Value]) VALUES ([src].[Key], [src].[Value]) "+
			"OUTPUT INSERTED.[Key], DELETED.[Value], INSERTED.[Value];",
		sql)
	assert.Equal(t, []any{"a", "1", "b", "2", "x"}, args)
}

// TestUpsertFromSelect: either upsert reads an INSERT … SELECT's rows as a derived table, with
// the SELECT's WITH hoisted in front of the statement and its ORDER BY held to subquery rules.
func TestUpsertFromSelect(t *testing.T) {
	src := NewBuilder().WithCTE("ids", NewBuilder().Select("Id").From("dbo.Customer").Build()).
		Select("Id", "7 AS RoleId").From("ids").Build()

	sql, args := render(t, NewBuilder().Insert("dbo.UserRoles").Columns("UserId", "RoleId").FromSelect(src).
		OnConflict("UserId", "RoleId").DoNothing())
	assert.Equal(t,
		"WITH [ids] AS (SELECT [Id] FROM [dbo].[Customer]) "+
			"INSERT INTO [dbo].[UserRoles] ([UserId], [RoleId]) SELECT [src].[UserId], [src].[RoleId] "+
			"FROM (SELECT [Id], 7 AS RoleId FROM [ids]) AS [src] ([UserId], [RoleId]) "+
			"WHERE NOT EXISTS (SELECT 1 FROM [dbo].[UserRoles] AS [tgt] WITH (UPDLOCK, HOLDLOCK) "+
			"WHERE [tgt].[UserId] = [src].[UserId] AND [tgt].[RoleId] = [src].[RoleId])",
		sql)
	assert.Empty(t, args)

	merge := NewBuilder().Select("Name", "Region").From("dbo.Customer").IsNotNull("Region").Build()
	sql, args = render(t, NewBuilder().Insert("dbo.Settings").Columns("Key", "Value").FromSelect(merge).
		OnConflict("Key").DoUpdate(map[string]any{"Value": "merged"}))
	assert.Equal(t,
		"MERGE INTO [dbo].[Settings] WITH (HOLDLOCK) AS [tgt] "+
			"USING (SELECT [Name], [Region] FROM [dbo].[Customer] WHERE [Region] IS NOT NULL) AS [src] ([Key], [Value]) "+
			"ON [tgt].[Key] = [src].[Key] WHEN MATCHED THEN UPDATE SET [Value] = ? "+
			"WHEN NOT MATCHED THEN INSERT ([Key], [Value]) VALUES ([src].[Key], [src].[Value]);",
		sql)
	assert.Equal(t, []any{"merged"}, args)

	ordered := NewBuilder().Select("Name", "Region").From("dbo.Customer").OrderBy("Name", "asc").Build()
	_, _, err := NewBuilder().Insert("dbo.Settings").Columns("Key", "Value").FromSelect(ordered).OnConflict("Key").DoNothing().ToSQL()
	requireUnsupported(t, err, "ORDER BY in a subquery without Limit or Offset")
}

func TestOnConflictWithoutColumnsIsRefused(t *testing.T) {
	_, _, err := NewBuilder().Insert("t").Columns("a").Values(1).OnConflict().DoNothing().ToSQL()
	requireUnsupported(t, err, "ON CONFLICT without conflict columns")
	assert.Contains(t, err.Error(), "OnConflict(col")
}

func TestOnConflictColumnNotInsertedIsRefused(t *testing.T) {
	_, _, err := NewBuilder().Insert("t").Columns("a").Values(1).OnConflict("b").DoNothing().ToSQL()
	requireUnsupported(t, err, "a conflict column that is not inserted")

	_, _, err = NewBuilder().Insert("t").Values(1).OnConflict("a").DoUpdate(map[string]any{"a": 1}).ToSQL()
	requireUnsupported(t, err, "a conflict column that is not inserted")

	// A bracketed spelling of an inserted column is the same column.
	sql, _ := render(t, NewBuilder().Insert("t").Columns("Key").Values(1).OnConflict("[Key]").DoNothing())
	assert.Contains(t, sql, "WHERE [tgt].[Key] = [src].[Key])")

	_, _, err = NewBuilder().Insert("t").Columns("a").Values(1).OnConflict("t.a").DoNothing().ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict column")
}

func TestDoUpdateWithoutSetIsAnError(t *testing.T) {
	_, _, err := NewBuilder().Insert("t").Columns("a").Values(1).OnConflict("a").DoUpdate(map[string]any{}).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one assignment")

	_, _, err = NewBuilder().Insert("t").Columns("a", "b").Values(1, 2).OnConflict("a").DoUpdate(map[string]any{"t.b": 1}).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DO UPDATE column")
}

func TestOnConflictWithoutActionIsAnError(t *testing.T) {
	_, _, err := NewBuilder().Insert("t").Columns("a").Values(1).OnConflict("a").ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DoNothing()")

	_, _, err = d().FormatQuery(&dbCore.Query{Insert: &dbCore.InsertClause{
		Table: "t", Columns: []string{"a"}, Values: [][]any{{1}},
		OnConflict: &dbCore.OnConflictClause{Columns: []string{"a"}, Action: "DO SOMETHING"},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DO SOMETHING")

	_, _, err = NewBuilder().Insert("t").Columns("a").OnConflict("a").DoNothing().ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no rows to insert")
}

// ------------------------------------------------------------------- UPDATE

func TestFormatQueryUpdate(t *testing.T) {
	sql, args := render(t, NewBuilder().Update("users").SetMap(map[string]any{"Name": "ann", "Age": 30}).Eq("Id", 1))
	assert.Equal(t, "UPDATE [users] SET [Age] = ?, [Name] = ? WHERE [Id] = ?", sql)
	assert.Equal(t, []any{30, "ann", 1}, args)

	sql, args = render(t, NewBuilder().Update("dbo.Order").Set("t.User", "x"))
	assert.Equal(t, "UPDATE [dbo].[Order] SET [t].[User] = ?", sql, "a qualified SET target is valid T-SQL")
	assert.Equal(t, []any{"x"}, args)

	_, _, err := NewBuilder().Update("users").Eq("Id", 1).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SET")

	_, _, err = NewBuilder().Update("users").Set("Name = 'x', Admin", 1).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SET target")
}

// TestUpdateWhereIsRenderedUnderTheContext: the WHERE of an UPDATE or DELETE is quoted and
// checked like a SELECT's, which the Postgres and MySQL dialects never did.
func TestUpdateWhereIsRenderedUnderTheContext(t *testing.T) {
	sql, args := render(t, NewBuilder().Update("dbo.2024Orders").Set("Status", "x").
		Where(&dbCore.RawCondition{SQL: "Name ILIKE ?", Args: []any{"a%"}}).
		InSubquery("CustomerId", NewBuilder().Select("Id").From("dbo.Customer").Eq("Region", "n").Build()))
	assert.Equal(t,
		"UPDATE [dbo].[2024Orders] SET [Status] = ? WHERE Name LIKE ? AND [CustomerId] IN "+
			"(SELECT [Id] FROM [dbo].[Customer] WHERE [Region] = ?)",
		sql)
	assert.Equal(t, []any{"x", "a%", "n"}, args)

	_, _, err := NewBuilder().Update("t").Set("a", 1).Where(&dbCore.BinaryCondition{Left: "1=1 OR a", Operator: "=", Right: 1}).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot render WHERE")

	_, _, err = NewBuilder().Delete("t").Where(&dbCore.IsNullCondition{Field: nil}).ToSQL()
	require.Error(t, err, "nil IS NULL would delete every row")
}

// TestFilterThatRendersEmptyIsRefused: a WHERE whose every condition renders empty would make
// the UPDATE or DELETE unfiltered.
func TestFilterThatRendersEmptyIsRefused(t *testing.T) {
	empty := &dbCore.RawCondition{SQL: ""}

	_, _, err := NewBuilder().Update("t").Set("a", 1).Where(empty).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would update every row")

	_, _, err = NewBuilder().Delete("t").Where(empty).ToSQL()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would delete every row")

	// With no conditions at all it is unfiltered on purpose.
	sql, args := render(t, NewBuilder().Delete("users"))
	assert.Equal(t, "DELETE FROM [users]", sql)
	assert.Empty(t, args)
}

func TestUpdateTop(t *testing.T) {
	sql, args := render(t, NewBuilder().Update("dbo.2024Orders").Set("Status", "x").Eq("Status", "open").Limit(1))
	assert.Equal(t, "UPDATE TOP (1) [dbo].[2024Orders] SET [Status] = ? WHERE [Status] = ?", sql)
	assert.Equal(t, []any{"x", "open"}, args)
}

func TestUpdateOutputInserted(t *testing.T) {
	sql, args := render(t, NewBuilder().Update("t").Set("a", 1).Eq("Id", 2).Returning("Id", "DELETED.a", "inserted.a"))
	assert.Equal(t, "UPDATE [t] SET [a] = ? OUTPUT INSERTED.[Id], DELETED.[a], INSERTED.[a] WHERE [Id] = ?", sql)
	assert.Equal(t, []any{1, 2}, args)
}

// TestUpdateWithCTE: T-SQL can update through a CTE, which is how the refusal of ORDER BY on
// an UPDATE says to update the first n rows in an order.
func TestUpdateWithCTE(t *testing.T) {
	last := NewBuilder().Select("*").From("dbo.2024Orders").OrderBy("Id", "desc").Limit(1).Build()
	sql, args := render(t, NewBuilder().WithCTE("c", last).Update("c").Set("Status", "last"))
	assert.Equal(t, "WITH [c] AS (SELECT TOP (1) * FROM [dbo].[2024Orders] ORDER BY [Id] DESC) UPDATE [c] SET [Status] = ?", sql)
	assert.Equal(t, []any{"last"}, args)

	sql, _ = render(t, NewBuilder().WithCTE("c", last).Delete("c"))
	assert.Equal(t, "WITH [c] AS (SELECT TOP (1) * FROM [dbo].[2024Orders] ORDER BY [Id] DESC) DELETE FROM [c]", sql)
}

func TestUpdateWithOrderByIsRefused(t *testing.T) {
	_, _, err := NewBuilder().Update("t").Set("a", 1).OrderBy("Id", "desc").Limit(1).ToSQL()
	requireUnsupported(t, err, "ORDER BY on an UPDATE")
	assert.Contains(t, err.Error(), `WithCTE("c"`)

	_, _, err = NewBuilder().Update("t").Set("a", 1).Offset(1).ToSQL()
	requireUnsupported(t, err, "Offset on an UPDATE")
}

func TestUpdateWithJoinIsRefused(t *testing.T) {
	_, _, err := NewBuilder().Update("t").Set("a", 1).InnerJoin("u", &dbCore.RawCondition{SQL: "u.Id = t.UId"}).ToSQL()
	requireUnsupported(t, err, "JOIN on an UPDATE")
	assert.Contains(t, err.Error(), "EXISTS")

	for name, b := range map[string]dbCore.IQueryBuilder{
		"FROM on an UPDATE":          NewBuilder().From("u").Update("t").Set("a", 1),
		"GROUP BY on an UPDATE":      NewBuilder().Update("t").Set("a", 1).GroupBy("a"),
		"HAVING on an UPDATE":        NewBuilder().Update("t").Set("a", 1).Having(&dbCore.RawCondition{SQL: "1=1"}),
		"UNION on an UPDATE":         NewBuilder().Update("t").Set("a", 1).Union(NewBuilder().Select("a").From("s").Build()),
		"a SELECT list on an UPDATE": NewBuilder().Update("t").Set("a", 1).Select("a"),
	} {
		_, _, err := b.ToSQL()
		requireUnsupported(t, err, name)
	}
}

// ------------------------------------------------------------------- DELETE

func TestFormatQueryDelete(t *testing.T) {
	sql, args := render(t, NewBuilder().Delete("users").Eq("Id", 1))
	assert.Equal(t, "DELETE FROM [users] WHERE [Id] = ?", sql)
	assert.Equal(t, []any{1}, args)
}

func TestDeleteTop(t *testing.T) {
	sql, args := render(t, NewBuilder().Delete("dbo.2024Orders").Eq("Status", "closed").Limit(1))
	assert.Equal(t, "DELETE TOP (1) FROM [dbo].[2024Orders] WHERE [Status] = ?", sql)
	assert.Equal(t, []any{"closed"}, args)

	_, _, err := NewBuilder().Delete("t").OrderBy("Id", "asc").Limit(1).ToSQL()
	requireUnsupported(t, err, "ORDER BY on a DELETE")
	assert.Contains(t, err.Error(), `Delete("c")`)

	_, _, err = NewBuilder().Delete("t").InnerJoin("u", &dbCore.RawCondition{SQL: "1=1"}).ToSQL()
	requireUnsupported(t, err, "JOIN on a DELETE")
}

// TestDeleteWithClausesItDoesNotTakeIsRefused: what TestUpdateWithJoinIsRefused pins for an
// UPDATE, for a DELETE. Dropped silently, each would change which rows go.
func TestDeleteWithClausesItDoesNotTakeIsRefused(t *testing.T) {
	for name, b := range map[string]dbCore.IQueryBuilder{
		"Offset on a DELETE":        NewBuilder().Delete("t").Offset(1),
		"FROM on a DELETE":          NewBuilder().From("u").Delete("t"),
		"GROUP BY on a DELETE":      NewBuilder().Delete("t").GroupBy("a"),
		"HAVING on a DELETE":        NewBuilder().Delete("t").Having(&dbCore.RawCondition{SQL: "1=1"}),
		"UNION on a DELETE":         NewBuilder().Delete("t").Union(NewBuilder().Select("a").From("s").Build()),
		"a SELECT list on a DELETE": NewBuilder().Delete("t").Select("a"),
	} {
		sql, args, err := b.ToSQL()
		requireUnsupported(t, err, name)
		assert.Empty(t, sql)
		assert.Nil(t, args)
	}
}

func TestDeleteOutputDeleted(t *testing.T) {
	sql, args := render(t, NewBuilder().Delete("dbo.2024Orders").In("Status", "a", "b").Limit(2).Returning("Id", "Status"))
	assert.Equal(t, "DELETE TOP (2) FROM [dbo].[2024Orders] OUTPUT DELETED.[Id], DELETED.[Status] WHERE [Status] IN (?, ?)", sql)
	assert.Equal(t, []any{"a", "b"}, args)

	sql, _ = render(t, NewBuilder().Delete("t").Returning("*"))
	assert.Equal(t, "DELETE FROM [t] OUTPUT DELETED.*", sql)
}

// ------------------------------------------------------------------- OUTPUT

// TestOutputPrefixRules: a column reads the statement's own pseudo-table, and an explicit
// prefix is honoured where the statement has that pseudo-table.
func TestOutputPrefixRules(t *testing.T) {
	tests := []struct {
		kind   outputKind
		fields []string
		want   string
	}{
		{insertOutput, []string{"Id"}, "OUTPUT INSERTED.[Id]"},
		{insertOutput, []string{"INSERTED.Id", "inserted.*"}, "OUTPUT INSERTED.[Id], INSERTED.*"},
		{updateOutput, []string{"Id", "DELETED.Name", "DELETED.*"}, "OUTPUT INSERTED.[Id], DELETED.[Name], DELETED.*"},
		{deleteOutput, []string{"Id", "deleted.Order", "*"}, "OUTPUT DELETED.[Id], DELETED.[Order], DELETED.*"},
		{mergeOutput, []string{"Key", "DELETED.Value"}, "OUTPUT INSERTED.[Key], DELETED.[Value]"},
		{insertOutput, []string{"[Order Date]", "2024Total"}, "OUTPUT INSERTED.[Order Date], INSERTED.[2024Total]"},
		{insertOutput, nil, ""},
	}
	for _, tt := range tests {
		got, err := formatOutput(tt.fields, tt.kind)
		require.NoErrorf(t, err, "%s %v", tt.kind.statement, tt.fields)
		assert.Equal(t, tt.want, got)
	}

	_, err := formatOutput([]string{"DELETED.Id"}, insertOutput)
	requireUnsupported(t, err, "DELETED in the OUTPUT of an INSERT")
	_, err = formatOutput([]string{"INSERTED.Id"}, deleteOutput)
	requireUnsupported(t, err, "INSERTED in the OUTPUT of a DELETE")
	_, _, err = NewBuilder().Insert("t").Columns("a").Values(1).Returning("DELETED.a").ToSQL()
	requireUnsupported(t, err, "DELETED in the OUTPUT of an INSERT")
}

func TestOutputExpressionIsRefused(t *testing.T) {
	for _, field := range []string{"Id + 1", "COUNT(*)", "t.Id", "Id AS x", "INSERTED.a.b", "1", "INSERTED.COUNT(*)"} {
		_, err := formatOutput([]string{field}, insertOutput)
		requireUnsupported(t, err, "an expression in RETURNING")
	}
	_, _, err := NewBuilder().Update("t").Set("a", 1).Returning("a * 2").ToSQL()
	requireUnsupported(t, err, "an expression in RETURNING")
}
