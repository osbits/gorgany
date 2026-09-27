package v2

import (
	"strings"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin what the dialect counts as a name. The cases are the shapes an EF Core schema
// has — digit-leading tables, reserved words, spaces in bracketed names — next to the literals
// and expressions that look like them and must not be bracketed.

func TestIsIdentifierAcceptsEFNames(t *testing.T) {
	for _, name := range []string{
		"Id",
		"CustomerId",
		"2024Orders",
		"dbo.2024Orders",
		"[dbo].[2024Orders]",
		"dbo.[2024Orders]",
		"Order",
		"User",
		"[Order]",
		"__EFMigrationsHistory",
		"[Order Lines]",
		"[Order Lines].Qty",
		"[a.b].c",
		"[a]]b]",
	} {
		assert.Truef(t, isIdentifier(name), "%q is an identifier", name)
	}

	for _, name := range []string{
		"server.catalog.dbo.t", // four parts, the most T-SQL has
		"_private",
		"a$b",
		"1st",
		"Ünïcödé",
		"t1.c2",
		"[NULL]", // bracketed, so a column named NULL
		"[Example-db].dbo.[2024Orders]",
	} {
		assert.Truef(t, isIdentifier(name), "%q is an identifier", name)
	}
}

func TestIsIdentifierRejects(t *testing.T) {
	for _, s := range []string{
		// Numbers: all digits, and the float and binary literal shapes that bareSegment's
		// digits-then-letters would otherwise take.
		"1", "42", "1e5", "2E10", "0x1F", "0x", "0XABC", "1.5",
		// Not names at all.
		"", " ", "a b", "a.", ".a", "a..b", "a.b.c.d.e", "*", "t.*", "COUNT(*)", "lower(Name)",
		"a-b", "Example-db", "a;b", "a = 1 OR 1=1", "'x'", `"quoted"`, "#temp", "@p1", "$1",
		// Brackets that do not close, are empty, or end early.
		"[a", "a]", "[]", "[a]b", "[a]]", "[a].",
		// Niladic keywords and NULL, in any case.
		"CURRENT_TIMESTAMP", "current_user", "SESSION_USER", "SYSTEM_USER", "CURRENT_DATE", "NULL", "null",
	} {
		assert.Falsef(t, isIdentifier(s), "%q is not an identifier", s)
	}
}

// TestBracketedPartWithAtOrQuestionMarkIsRefused: gorm reads every "?" as a placeholder, and
// an "@" anywhere switches it to named parameters, so a bracketed name holding either would
// misplace the arguments bound after it.
func TestBracketedPartWithAtOrQuestionMarkIsRefused(t *testing.T) {
	for _, s := range []string{"[a@b]", "[a?b]", "dbo.[x?]", "[@p1]", "[?]"} {
		assert.Falsef(t, isIdentifier(s), "%q must not be an identifier", s)
	}
	_, err := columnName("[a?b]", "INSERT column")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a column name")
}

// TestBracketedMarkerIsRefusedWhereTextPassesThrough: a table reference, a select item and a
// GROUP BY item emit what is not an identifier as the caller's SQL. A bracketed name that
// fails only for its "@" or "?" is a name all the same, meant as one, and is refused there
// too, rather than sent for gorm to read as a parameter: two "?" and one arg, misbound.
func TestBracketedMarkerIsRefusedWhereTextPassesThrough(t *testing.T) {
	for name, b := range map[string]dbCore.IQueryBuilder{
		"[Orders@2024]":    NewBuilder().Select("Id").From("[Orders@2024]").Eq("Id", 1),
		"[o?]":             NewBuilder().Select("Id").From("[Orders] [o?]").Eq("Id", 1),
		"[a?b]":            NewBuilder().Select("[a?b]").From("t").Eq("Id", 1),
		"[x@y]":            NewBuilder().Select("Id AS [x@y]").From("t"),
		"dbo.[x?]":         NewBuilder().Select("COUNT(*)").From("t").GroupBy("dbo.[x?]"),
		"[Archive@2023].t": NewBuilder().Select("Id").From("t").InnerJoin("[Archive@2023].t", &dbCore.RawCondition{SQL: "1=1"}),
		"[dbo].[Orders?]":  NewBuilder().Delete("[dbo].[Orders?]").Eq("Id", 1),
		"[Orders@2024] ":   NewBuilder().Update("[Orders@2024] ").Set("a", 1),
	} {
		_, _, err := b.ToSQL()
		requireUnsupported(t, err, "the bracketed name "+strings.TrimSpace(name))
		assert.Contains(t, err.Error(), "rename the object")
	}

	// An expression is still the caller's SQL, as written.
	sql, _ := render(t, NewBuilder().Select("COUNT([Id]) AS n", "fn_x(1)").From("dbo.fn_orders(1) f"))
	assert.Equal(t, "SELECT COUNT([Id]) AS n, fn_x(1) FROM dbo.fn_orders(1) f", sql)
}

func TestQuoteIdentifierBrackets(t *testing.T) {
	d := &SQLServerDialect{}
	tests := []struct{ in, want string }{
		{"id", "[id]"},
		{"2024Orders", "[2024Orders]"},
		{"dbo.2024Orders", "[dbo].[2024Orders]"},
		{"Order", "[Order]"},
		{"User", "[User]"},
		{"server.catalog.dbo.t", "[server].[catalog].[dbo].[t]"},
		// A "]" is doubled, QUOTENAME's escape; other quote characters mean nothing inside.
		{"a]b", "[a]]b]"},
		{`a"b`, `[a"b]`},
		{"a`b", "[a`b]"},
		// Niladic keywords stay bare: bracketed, they would name a column that does not exist.
		{"CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP"},
		{"null", "null"},
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, d.QuoteIdentifier(tt.in), "QuoteIdentifier(%q)", tt.in)
	}
}

func TestQuoteIdentifierIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"id", "dbo.2024Orders", "[dbo].[2024Orders]", "a]b", "[a.b].c", "Order Lines", "x] FROM t --",
		"[a@b]", "[unclosed", "a..b",
	} {
		once := quoteIdentifier(in)
		assert.Equalf(t, once, quoteIdentifier(once), "quoting %q twice must not change it", in)
	}
}

func TestQuoteIdentifierKeepsDotsInsideBrackets(t *testing.T) {
	assert.Equal(t, "[a.b].[c]", quoteIdentifier("[a.b].c"))
	assert.Equal(t, "[dbo].[Order.Lines]", quoteIdentifier("dbo.[Order.Lines]"))

	parts, ok := splitParts("[a.b].[c]]d].e")
	require.True(t, ok)
	assert.Equal(t, []string{"[a.b]", "[c]]d]", "e"}, parts)

	_, ok = splitParts("[a.b")
	assert.False(t, ok, "an unclosed bracket is not a name")
}

// TestQuoteIdentifierCannotBeBrokenOut is the security-relevant case: whatever the input, each
// part's text ends up inside one bracket pair, so a payload stays a name.
func TestQuoteIdentifierCannotBeBrokenOut(t *testing.T) {
	for _, payload := range []string{
		"id] FROM users WHERE 1=1 --",
		"id]]] ; DROP TABLE t --",
		"[x]; DROP TABLE t; --",
		"a[b",
		"]",
		"[a.b",
	} {
		got := quoteIdentifier(payload)
		names, ok := readBracketedName(got)
		require.Truef(t, ok, "%q quoted as %q must read back as bracketed parts", payload, got)
		assert.Equalf(t, payload, unquotedJoin(names), "%q must survive quoting as the name it is", payload)
	}
}

// readBracketedName reads s as T-SQL reads a delimited multi-part name: one or more [..]
// parts, separated by dots, with ]] standing for ]. It fails if anything is left over.
func readBracketedName(s string) ([]string, bool) {
	var names []string
	for i := 0; i < len(s); {
		if s[i] != '[' {
			return nil, false
		}
		var name strings.Builder
		i++
		closed := false
		for i < len(s) {
			if s[i] == ']' {
				if i+1 < len(s) && s[i+1] == ']' {
					name.WriteByte(']')
					i += 2
					continue
				}
				i++
				closed = true
				break
			}
			name.WriteByte(s[i])
			i++
		}
		if !closed {
			return nil, false
		}
		names = append(names, name.String())
		if i == len(s) {
			return names, true
		}
		if s[i] != '.' {
			return nil, false
		}
		i++
	}
	return nil, false
}

// unquotedJoin rejoins parts that each came from one input part. A payload with no dot outside
// brackets is one part, so this is only ever compared for those.
func unquotedJoin(names []string) string { return strings.Join(names, ".") }

func TestSelectItemQuoting(t *testing.T) {
	tests := []struct{ in, want string }{
		{"*", "*"},
		{"t.*", "[t].*"},
		{"dbo.Order.*", "[dbo].[Order].*"},
		{"Id", "[Id]"},
		{"o.Total", "[o].[Total]"},
		{"[Order Date]", "[Order Date]"},
		{"Total AS Sum", "[Total] AS [Sum]"},
		{"o.Total as [Order Total]", "[o].[Total] AS [Order Total]"},
		{" Id ", "[Id]"},
		// Expressions are the caller's SQL and are emitted as written.
		{"COUNT(*)", "COUNT(*)"},
		{"COUNT(*) AS n", "COUNT(*) AS n"},
		{"SUM(o.Total) AS total", "SUM(o.Total) AS total"},
		{"1", "1"},
		{"1e5", "1e5"},
		{"0x1F", "0x1F"},
		{"CURRENT_TIMESTAMP", "CURRENT_TIMESTAMP"},
		{"NULL AS x", "NULL AS x"},
		{"Id alias", "Id alias"},
		{"N'copied' AS Status", "N'copied' AS Status"},
		{"CASE WHEN a = 1 THEN 'x' END AS y", "CASE WHEN a = 1 THEN 'x' END AS y"},
	}
	for _, tt := range tests {
		got, err := selectItem(tt.in)
		require.NoErrorf(t, err, "selectItem(%q)", tt.in)
		assert.Equalf(t, tt.want, got, "selectItem(%q)", tt.in)
	}
}

func TestTableRefQuoting(t *testing.T) {
	tests := []struct {
		in, want string
		aliased  bool
	}{
		{"users", "[users]", false},
		{"dbo.2024Orders", "[dbo].[2024Orders]", false},
		{"[dbo].[Order]", "[dbo].[Order]", false},
		{"users u", "[users] AS [u]", true},
		{"users AS u", "[users] AS [u]", true},
		{"dbo.Order as o", "[dbo].[Order] AS [o]", true},
		{"[Order Lines] l", "[Order Lines] AS [l]", true},
		// Anything else is the caller's SQL: a function, a hint, a #temp table.
		{"dbo.fn_orders(1) f", "dbo.fn_orders(1) f", false},
		{"orders WITH (NOLOCK)", "orders WITH (NOLOCK)", false},
		{"#staging", "#staging", false},
		{"db..t", "db..t", false},
		{"users u extra", "users u extra", false},
	}
	for _, tt := range tests {
		got, aliased, err := tableRef(tt.in)
		require.NoErrorf(t, err, "tableRef(%q)", tt.in)
		assert.Equalf(t, tt.want, got, "tableRef(%q)", tt.in)
		assert.Equalf(t, tt.aliased, aliased, "tableRef(%q) aliased", tt.in)
	}
}

func TestTokensKeepBracketedSpaces(t *testing.T) {
	assert.Equal(t, []string{"[Order Lines]", "AS", "l"}, tokens("[Order Lines] AS l"))
	assert.Equal(t, []string{"[a]] b]", "x"}, tokens("  [a]] b]\tx "))
	assert.Empty(t, tokens("   "))
}

func TestColumnAndAliasNamesMustBeOnePartIdentifiers(t *testing.T) {
	got, err := columnName("2024Total", "INSERT column")
	require.NoError(t, err)
	assert.Equal(t, "[2024Total]", got)

	for _, bad := range []string{"t.col", "COUNT(*)", "a b", "", "1"} {
		_, err := columnName(bad, "INSERT column")
		require.Errorf(t, err, "columnName(%q)", bad)
		assert.Contains(t, err.Error(), "INSERT column")
	}

	got, err = aliasName("Order", "table alias")
	require.NoError(t, err)
	assert.Equal(t, "[Order]", got)

	_, err = aliasName("o; DROP TABLE t", "table alias")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table alias")
}
