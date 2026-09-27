package core_test

// Tests for the SQL guards in readonly.go. Each guard is run over a corpus per lexicon, since
// what a statement means depends on the engine reading it: [Update] is a name on SQL Server
// and a keyword on Postgres, 'it\'s' is one literal on MySQL and an unterminated one on SQL
// Server. The corpora pin both directions — what must pass, so the guards do not make
// ordinary reads and writes unrunnable, and what must not, so a write cannot hide in a
// comment, a literal, a CTE or a second statement.

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sqlGuard func(sql string, lex dbCore.SQLLexicon) error

type namedLexicon struct {
	name string
	lex  dbCore.SQLLexicon
}

var everyLexicon = []namedLexicon{
	{"tsql", dbCore.LexiconTSQL},
	{"postgres", dbCore.LexiconPostgres},
	{"mysql", dbCore.LexiconMySQL},
}

// corpus is SQL that every lexicon reads the same way, plus SQL only one of them reads that
// way.
type corpus struct {
	common    []string
	perEngine map[string][]string
}

func (c corpus) each(t *testing.T, check func(t *testing.T, lex dbCore.SQLLexicon, sql string)) {
	t.Helper()
	for _, l := range everyLexicon {
		t.Run(l.name, func(t *testing.T) {
			for _, sql := range append(append([]string{}, c.common...), c.perEngine[l.name]...) {
				check(t, l.lex, sql)
			}
		})
	}
}

func allows(guard sqlGuard) func(t *testing.T, lex dbCore.SQLLexicon, sql string) {
	return func(t *testing.T, lex dbCore.SQLLexicon, sql string) {
		t.Helper()
		assert.NoError(t, guard(sql, lex), "%q must pass", sql)
	}
}

func refuses(guard sqlGuard, sentinel error) func(t *testing.T, lex dbCore.SQLLexicon, sql string) {
	return func(t *testing.T, lex dbCore.SQLLexicon, sql string) {
		t.Helper()
		assert.ErrorIs(t, guard(sql, lex), sentinel, "%q must be refused", sql)
	}
}

// ------------------------------------------------------------------- GuardReadOnlySQL

func TestGuardReadOnlySQLAllowsReads(t *testing.T) {
	corpus{
		common: []string{
			"SELECT 1",
			"select id, name from legacy where id = ?",
			"WITH recent AS (SELECT id FROM legacy) SELECT id FROM recent",
			"(SELECT 1)",
			";SELECT 1;",
			"SELECT 'DELETE FROM legacy'",
			"SELECT N'DELETE FROM legacy'",
			"SELECT 'it''s'",
			`SELECT "update" FROM legacy`,
			"SELECT 1 -- DELETE FROM legacy",
			"SELECT 1 -- a comment\n",
			"SELECT /* DELETE FROM legacy */ 1",
			"SELECT id FROM legacy ORDER BY id OFFSET 5 ROWS FETCH NEXT 5 ROWS ONLY",
			"SELECT a.id FROM legacy a JOIN legacy b ON a.id = b.id WHERE a.x IN (SELECT y FROM legacy)",
			"SELECT 1e5, 0x1F, .5, 1.",
			"",
			"  /* a statement with no tokens runs nothing */  ",
		},
		perEngine: map[string][]string{
			"tsql": {
				"SELECT [Update], [Delete] FROM [dbo].[2024Orders]",
				"SELECT [odd]]name] FROM legacy",
				"SELECT /* outer /* nested */ DELETE */ 1",
				`SELECT 'C:\temp\'`,
				"SELECT @p1, @@ROWCOUNT FROM #scratch",
				"SELECT TOP (5) Id FROM legacy WITH (NOLOCK)",
				// Hints that lock less than a read otherwise would, or only choose how finely
				// it locks, are no locking clause; nor is a CTE's WITH, nor a bracketed name.
				"SELECT Id FROM legacy WITH (READUNCOMMITTED)",
				"SELECT Id FROM legacy WITH (READPAST, ROWLOCK)",
				"SELECT Id FROM legacy WITH (PAGLOCK)",
				"SELECT Id FROM legacy (NOLOCK)",
				"SELECT [updlock], [holdlock], [tablock] FROM legacy",
				"SELECT [updlock] FROM legacy WITH (NOLOCK)",
				"SELECT COUNT([updlock]), ISNULL([holdlock], 0) FROM legacy",
				"SELECT * FROM legacy WITH (INDEX([updlock]))", // an index of that name
				"WITH c ([updlock]) AS (SELECT 1) SELECT [updlock] FROM c",
				"WITH c AS (SELECT Id FROM legacy WITH (NOLOCK)) SELECT Id FROM c",
				"WITH c (x) AS (SELECT 1), d AS (SELECT 2 AS y) SELECT x, y FROM c, d",
			},
			"postgres": {
				"SELECT $$DELETE FROM legacy$$, $body$ it's $body$",
				`SELECT E'it\'s DELETE'`,
				`SELECT 'C:\temp'`,
				"SELECT id FROM legacy WHERE id = $1 AND name::text = $2",
				"SELECT /* outer /* nested */ DELETE */ 1",
				"SELECT a$b FROM legacy",
			},
			"mysql": {
				"SELECT `update` FROM legacy",
				"SELECT `odd``name` FROM legacy",
				`SELECT 'it\'s'`,
				`SELECT "it\"s"`,
				"SELECT 1 # DELETE FROM legacy",
				"SELECT /*+ MAX_EXECUTION_TIME(1000) */ id FROM legacy",
			},
		},
	}.each(t, allows(dbCore.GuardReadOnlySQL))
}

func TestGuardReadOnlySQLRefusesWrites(t *testing.T) {
	corpus{
		common: []string{
			"SELECT * INTO legacy_copy FROM legacy",
			"SELECT 1; DELETE FROM legacy",
			"select 1; delete from legacy",
			"SELECT 1; SELECT 2",
			"WITH gone AS (DELETE FROM legacy RETURNING id) SELECT id FROM gone",
			"WITH recent AS (SELECT id FROM legacy) DELETE FROM legacy WHERE id IN (SELECT id FROM recent)",
			"SELECT * FROM (SELECT * FROM (DELETE FROM legacy RETURNING *) AS d) AS e",
			"EXEC sp_who",
			"MERGE INTO legacy USING staged ON legacy.id = staged.id WHEN MATCHED THEN UPDATE SET name = staged.name",
			"SELECT NEXT VALUE FOR legacy_seq",
			"/* a comment */ DELETE FROM legacy",
			"-- a comment\nUPDATE legacy SET name = 'x'",
			"SELECT 1 -- a comment ends at a carriage return too\rDELETE FROM legacy",
			"SELECT 1 DELETE FROM legacy",       // two statements to SQL Server, which needs no ";"
			"SELECT 1.DELETE FROM legacy",       // 1. is a number, and DELETE starts the next statement
			"SELECT 1DELETE FROM legacy",        // SQL Server reads 1 and DELETE
			"SELECT 1 \u200bDELETE FROM legacy", // a zero-width space does not join words
			"ſelect 1",                          // Unicode folds a long s to S; no engine reads ſelect as SELECT
			"SELECT id FROM legacy FOR UPDATE",
			"INSERT INTO legacy VALUES (1)",
			"(DELETE FROM legacy)",
			"SELECT 'unterminated",
			"SELECT 1 /* unterminated",
			`SELECT "unterminated`,
		},
		perEngine: map[string][]string{
			"tsql": {
				"SELECT [unterminated",
				"SELECT 1 /* outer /* nested */ still open",
				`SELECT 'it\'s'`,
				"SELECT * FROM OPENROWSET('x', 'y', 'z')",
				"SELECT * FROM OPENQUERY([loopback], 'SELECT 1 AS x')",
				"SELECT * FROM OPENDATASOURCE('MSOLEDBSQL', 'Data Source=(local)').reports.dbo.legacy",
				"SELECT Id FROM legacy WHERE Id IN (SELECT Id FROM OPENQUERY([loopback], 'SELECT 1 AS Id'))",
				"SELECT * FROM legacy; DBCC CHECKDB",
			},
			"postgres": {
				`SELECT E'\'', 'x'; DELETE FROM legacy; --'`,
				`SELECT E'unterminated\'`,
				"SELECT $$unterminated",
				"SELECT $tag$ x $other$",
				"SELECT 1 /* outer /* nested */ still open",
				"SELECT * FROM legacy; COPY legacy TO STDOUT",
			},
			"mysql": {
				`SELECT 'it\'s'; DELETE FROM legacy`,
				"SELECT `unterminated",
				"SELECT 1--1; DELETE FROM legacy",
				"SELECT 1 /*! ; DELETE FROM legacy */",
				"SELECT 1 /*!50000 ; DELETE FROM legacy */",
				"SELECT 1 /*M! ; DELETE FROM legacy */",
				"SELECT 1 /*! unterminated",
				"SELECT * FROM legacy LOCK IN SHARE MODE",
			},
		},
	}.each(t, refuses(dbCore.GuardReadOnlySQL, dbCore.ErrReadOnly))
}

// TestGuardReadOnlySQLBackslashEscapes pins the engines whose backslashes depend on a server
// setting. E'…' honours them on Postgres whatever the setting, so a guard that does not know
// E-strings ends the literal early and misses the DELETE after it. And a plain literal is
// read both ways on Postgres and MySQL, since standard_conforming_strings and
// NO_BACKSLASH_ESCAPES decide where it ends and the guard cannot see either.
func TestGuardReadOnlySQLBackslashEscapes(t *testing.T) {
	pg, my, ts := dbCore.LexiconPostgres, dbCore.LexiconMySQL, dbCore.LexiconTSQL

	assert.ErrorIs(t, dbCore.GuardReadOnlySQL(`SELECT E'\'', 'x'; DELETE FROM legacy; --'`, pg), dbCore.ErrReadOnly)
	assert.NoError(t, dbCore.GuardReadOnlySQL(`SELECT E'it\'s'`, pg))
	assert.ErrorIs(t, dbCore.GuardReadOnlySQL(`SELECT '\'', 'x'; DELETE FROM legacy; --'`, pg), dbCore.ErrReadOnly,
		"with standard_conforming_strings off this is a SELECT and then a DELETE")
	assert.NoError(t, dbCore.GuardReadOnlySQL(`SELECT 'C:\temp'`, pg), "a backslash alone changes nothing either way")

	assert.NoError(t, dbCore.GuardReadOnlySQL(`SELECT 'it\'s'`, my), "one literal under MySQL's default sql_mode")
	assert.ErrorIs(t, dbCore.GuardReadOnlySQL(`SELECT 'it\'s'; DELETE FROM legacy`, my), dbCore.ErrReadOnly)
	assert.ErrorIs(t, dbCore.GuardReadOnlySQL(`SELECT 'a\'; DELETE FROM legacy; -- '`, my), dbCore.ErrReadOnly,
		"under NO_BACKSLASH_ESCAPES the literal ends at the second quote and a DELETE follows")
	assert.ErrorIs(t, dbCore.GuardReadOnlySQL(`SELECT "a\"; DELETE FROM legacy; -- "`, my), dbCore.ErrReadOnly,
		"under ANSI_QUOTES \"…\" is an identifier, which takes no backslash escapes")

	assert.NoError(t, dbCore.GuardReadOnlySQL(`SELECT 'C:\', 'DELETE'`, ts),
		"a backslash is never an escape on SQL Server, so it reads the text once")
	assert.ErrorIs(t, dbCore.GuardReadOnlySQL(`SELECT 'it\'s'`, ts), dbCore.ErrReadOnly,
		"on SQL Server that is a literal and then an unterminated one")
}

func TestGuardReadOnlySQLLexiconsDiffer(t *testing.T) {
	for _, c := range []struct {
		sql       string
		allowedOn []string
	}{
		{"SELECT [Update] FROM legacy", []string{"tsql"}},
		{"SELECT `update` FROM legacy", []string{"mysql"}},
		{"SELECT $$ DELETE $$", []string{"postgres"}},
		{"SELECT 1 # DELETE FROM legacy", []string{"mysql"}},
		{"SELECT 1--1; DELETE FROM legacy", []string{"tsql", "postgres"}},
		{`SELECT 'it\'s'`, []string{"mysql"}},
		{"SELECT /* a /* b */ DELETE FROM legacy */ 1", []string{"tsql", "postgres"}},
		{`SELECT E'\'' AS quote`, []string{"postgres", "mysql"}},
		{"SELECT holdlock FROM legacy", []string{"postgres", "mysql"}}, // a table hint on SQL Server
	} {
		for _, l := range everyLexicon {
			err := dbCore.GuardReadOnlySQL(c.sql, l.lex)
			if contains(c.allowedOn, l.name) {
				assert.NoError(t, err, "%s must pass %q", l.name, c.sql)
			} else {
				assert.ErrorIs(t, err, dbCore.ErrReadOnly, "%s must refuse %q", l.name, c.sql)
			}
		}
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------- GuardReadOnlyShape

func TestGuardReadOnlyShapeAllowsKeywordNamedColumns(t *testing.T) {
	const keywordColumns = "SELECT lock, copy, load, set, use FROM legacy"
	for _, l := range []namedLexicon{{"postgres", dbCore.LexiconPostgres}, {"mysql", dbCore.LexiconMySQL}} {
		assert.NoError(t, dbCore.GuardReadOnlyShape(keywordColumns, l.lex),
			"%s: the dialect emits these names unquoted", l.name)
		assert.ErrorIs(t, dbCore.GuardReadOnlySQL(keywordColumns, l.lex), dbCore.ErrReadOnly,
			"%s: raw SQL has to quote them", l.name)
	}
	// SQL Server's dialect brackets every identifier, and its shape check reads every word
	// (see TestGuardReadOnlyShapeOnSQLServerReadsEveryWord).
	assert.ErrorIs(t, dbCore.GuardReadOnlyShape(keywordColumns, dbCore.LexiconTSQL), dbCore.ErrReadOnly)
	assert.NoError(t, dbCore.GuardReadOnlyShape("SELECT [lock], [copy], [load], [set], [use] FROM legacy", dbCore.LexiconTSQL))

	corpus{
		common: []string{
			"SELECT id FROM legacy WHERE status = ? ORDER BY id OFFSET 5 ROWS FETCH NEXT 5 ROWS ONLY",
			"WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 5) SELECT i FROM n",
			"WITH a AS (SELECT 1), b AS MATERIALIZED (SELECT 2), c AS NOT MATERIALIZED ((SELECT 3)) SELECT * FROM a, b, c",
			"WITH v(x) AS (VALUES (1)) SELECT x FROM v",
			"WITH outer_cte AS (WITH inner_cte AS (SELECT 1 AS x) SELECT x FROM inner_cte) SELECT x FROM outer_cte",
			"WITH XMLNAMESPACES ('urn:example' AS ex) SELECT 1",
			"(SELECT 1) UNION (SELECT 2)",
			"SELECT id FROM legacy WITH (NOLOCK)",
			"SELECT CAST(stamp AS timestamp WITH TIME ZONE) FROM legacy",
			"SELECT * FROM (WITH c AS (SELECT 1 AS x) SELECT x FROM c) AS s",
			"SELECT id FROM legacy;",
			"SELECT id FROM legacy FOR JSON PATH",
		},
		perEngine: map[string][]string{
			"tsql": {
				"SELECT COUNT(*) FROM (SELECT [lock] FROM legacy GROUP BY [lock]) AS gorgany_count",
				"WITH [c] AS (SELECT [Id] FROM [legacy] WITH (READPAST)) SELECT [Id] FROM [c] WITH (NOLOCK)",
			},
			"postgres": {"SELECT COUNT(*) FROM (SELECT lock FROM legacy GROUP BY lock) AS gorgany_count"},
			"mysql":    {"SELECT COUNT(*) FROM (SELECT lock FROM legacy GROUP BY lock) AS gorgany_count"},
		},
	}.each(t, allows(dbCore.GuardReadOnlyShape))
}

// TestGuardReadOnlyShapeRefusesLocksAndSequences: a locking clause and NEXT VALUE FOR are
// not structure a dialect renders for a read, and each needs write rights, so the shape check
// refuses them wherever they stand, as the deep check does. FOR SHARE and FOR KEY SHARE have
// no word the deep check would otherwise refuse. SQL Server spells its locking clauses as
// table hints, with WITH or without it, alone or in a list with others, and in a query hint.
func TestGuardReadOnlyShapeRefusesLocksAndSequences(t *testing.T) {
	c := corpus{common: []string{
		"SELECT id FROM legacy FOR UPDATE",
		"SELECT id FROM legacy FOR UPDATE SKIP LOCKED",
		"SELECT id FROM legacy FOR NO KEY UPDATE",
		"SELECT id FROM legacy FOR SHARE",
		"SELECT id FROM legacy FOR KEY SHARE NOWAIT",
		"SELECT id FROM legacy LOCK IN SHARE MODE",
		"SELECT * FROM (SELECT id FROM legacy FOR UPDATE) AS s",
		"SELECT NEXT VALUE FOR legacy_seq",
		"SELECT id FROM legacy WHERE id = (SELECT NEXT VALUE FOR legacy_seq)",
	}, perEngine: map[string][]string{
		"tsql": {
			"SELECT * FROM legacy WITH (UPDLOCK)",
			"SELECT * FROM legacy WITH (XLOCK)",
			"SELECT * FROM legacy WITH (TABLOCKX)",
			"SELECT * FROM legacy WITH (HOLDLOCK)",
			"SELECT * FROM legacy WITH (TABLOCK)",
			"SELECT * FROM legacy WITH (SERIALIZABLE)", // HOLDLOCK's synonym
			"SELECT * FROM legacy WITH (REPEATABLEREAD)",
			"SELECT * FROM legacy with (updlock)",
			"SELECT * FROM legacy WITH (ROWLOCK, UPDLOCK)",
			"SELECT * FROM legacy WITH (READPAST, UPDLOCK, ROWLOCK)",
			"SELECT * FROM legacy WITH (NOLOCK) JOIN legacy_staged WITH (XLOCK) ON 1 = 1",
			"SELECT * FROM legacy (UPDLOCK)",
			"SELECT * FROM legacy (ROWLOCK, XLOCK)",
			"SELECT * FROM [dbo].[legacy] AS [l] WITH (UPDLOCK) WHERE [l].[Id] = @p1",
			"SELECT * FROM (SELECT Id FROM legacy WITH (UPDLOCK)) AS s",
			"WITH c AS (SELECT Id FROM legacy WITH (UPDLOCK)) SELECT Id FROM c",
			"WITH c AS (SELECT 1 AS x) SELECT x FROM c, legacy WITH (HOLDLOCK)",
			"SELECT * FROM legacy OPTION (TABLE HINT (legacy, UPDLOCK))",
			// A bracketed or quoted spelling, as an item of a hint list.
			"SELECT * FROM legacy WITH ([UPDLOCK])",
			"SELECT * FROM legacy WITH ([updlock])",
			"SELECT * FROM legacy WITH (NOLOCK, [XLOCK])",
			"SELECT * FROM legacy WITH (INDEX([ix_legacy]), [HOLDLOCK])",
			`SELECT * FROM legacy WITH ("TABLOCKX")`,
			"SELECT * FROM legacy OPTION (TABLE HINT ([legacy], [UPDLOCK]))",
		},
	}}
	c.each(t, refuses(dbCore.GuardReadOnlyShape, dbCore.ErrReadOnly))
	c.each(t, refuses(dbCore.GuardReadOnlySQL, dbCore.ErrReadOnly))

	pg := dbCore.LexiconPostgres
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT id FROM legacy FOR SHARE", pg), "SELECT … FOR SHARE statement refused")
	assert.ErrorContains(t, dbCore.GuardReadOnlySQL("SELECT id FROM legacy FOR UPDATE", pg), "SELECT … FOR UPDATE statement refused")
	assert.NoError(t, dbCore.GuardReadOnlyShape("SELECT substring(name FROM 1 FOR 2) FROM legacy", pg),
		"FOR that locks nothing is left alone")

	ts := dbCore.LexiconTSQL
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT * FROM legacy (updlock)", ts), "SELECT … WITH (UPDLOCK) statement refused",
		"a hint is named in the form SQL Server documents, whichever form it took")
	assert.ErrorContains(t, dbCore.GuardReadOnlySQL("SELECT * FROM legacy WITH (ROWLOCK, HOLDLOCK)", ts), "SELECT … WITH (HOLDLOCK) statement refused")
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT * FROM legacy WITH ([updlock])", ts), "SELECT … WITH (UPDLOCK) statement refused")
}

// TestGuardsRefuseSetConfig: set_config is SET as a function, which a SELECT can call, and
// set_config('default_transaction_read_only', 'off', false) switches off, for the rest of the
// pooled connection's life, the read-only transactions a read_only Postgres datasource asks
// the server for. Both guards refuse a call of it however the name is qualified, quoted or
// spaced, at any depth, and a call through a U&"…" name, whose escapes the guard does not
// decode. A column of that name, the name in a literal or a comment, and a U&"…" name that is
// not called are left alone.
func TestGuardsRefuseSetConfig(t *testing.T) {
	const off = "('default_transaction_read_only', 'off', false)"
	refused := corpus{common: []string{
		"SELECT set_config" + off,
		"SELECT SET_CONFIG" + off,
		"SELECT set_config /* spaced */ " + off,
		"SELECT pg_catalog.set_config" + off,
		`SELECT "set_config"` + off,
		`SELECT pg_catalog."set_config"` + off,
		"SELECT * FROM set_config" + off + " AS s",
		"SELECT id FROM legacy WHERE set_config" + off + " IS NOT NULL",
		"SELECT id FROM legacy WHERE id IN (SELECT length(set_config" + off + "))",
		"WITH s AS (SELECT set_config" + off + " AS v) SELECT v FROM s",
		`SELECT U&"set\005fconfig"` + off,
		`SELECT u&"set!005fconfig" UESCAPE '!' ` + off,
	}, perEngine: map[string][]string{
		"postgres": {"SELECT set_config($1, $2, false)", "SELECT set_config(E'default_transaction_read_only', 'off', false)"},
		"mysql":    {"SELECT `set_config`('default_transaction_read_only', 'off', 0)"},
		"tsql":     {"SELECT [set_config]('default_transaction_read_only', 'off', 0)"},
	}}
	refused.each(t, refuses(dbCore.GuardReadOnlySQL, dbCore.ErrReadOnly))
	refused.each(t, refuses(dbCore.GuardReadOnlyShape, dbCore.ErrReadOnly))

	allowed := corpus{common: []string{
		"SELECT set_config FROM legacy",
		"SELECT current_setting('default_transaction_read_only')",
		"SELECT my_set_config(1) FROM legacy",
		"SELECT 'set_config(1)' FROM legacy",
		"SELECT id FROM legacy /* set_config(1) */",
		`SELECT U&"d\0061ta" FROM legacy`,
	}}
	allowed.each(t, allows(dbCore.GuardReadOnlySQL))
	allowed.each(t, allows(dbCore.GuardReadOnlyShape))

	pg := dbCore.LexiconPostgres
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT pg_catalog.set_config"+off, pg), "set_config() statement refused")
	assert.ErrorContains(t, dbCore.GuardReadOnlySQL(`SELECT U&"set\005fconfig"`+off, pg), `U&"…"() statement refused`)
}

// TestGuardReadOnlyShapeOnSQLServerReadsEveryWord: SQL Server needs no ";" before the next
// statement, so a statement with a SELECT's shape can still end in a write, and builder SQL
// carries an app's raw fragments. Under its lexicon the shape check is the deep check.
func TestGuardReadOnlyShapeOnSQLServerReadsEveryWord(t *testing.T) {
	lex := dbCore.LexiconTSQL
	for _, sql := range []string{
		"SELECT id FROM legacy DELETE FROM legacy",
		"SELECT 1 EXEC('DROP TABLE legacy')",
		"SELECT 1 EXECUTE sp_who",
		"SELECT 1 INSERT legacy VALUES (1)",
		"SELECT 1 MERGE legacy USING staged ON 1 = 1 WHEN MATCHED THEN DELETE;",
		"WITH c AS (SELECT 1 AS x) SELECT x FROM c UPDATE legacy SET name = 'x'",
		"SELECT id FROM legacy WHERE 1=1 DELETE FROM legacy",
		"SELECT id FROM legacy UPDATE legacy SET name = 'x' --",
		"SELECT NEXT VALUE FOR legacy_seq",
		"SELECT 1 TRUNCATE TABLE legacy",
		"SELECT 1 DECLARE @x int",
		"SELECT 1 SET NOCOUNT ON",
		"SELECT 1 BEGIN TRANSACTION",
		"SELECT 1 COMMIT",
		"SELECT 1 ROLLBACK",
		"SELECT 1 SAVE TRANSACTION sp1",
		"SELECT 1 IF 1 = 1 SELECT 2",
		"SELECT 1 WHILE 1 = 1 BREAK",
		"SELECT 1 WAITFOR DELAY '00:01:00'",
		"SELECT 1 GOTO done",
		"SELECT 1 RETURN",
		"SELECT 1 PRINT 'x'",
		"SELECT 1 RAISERROR('x', 16, 1)",
		"SELECT 1 OPEN legacy_cursor",
		"SELECT 1 CLOSE legacy_cursor",
		"SELECT 1 DEALLOCATE legacy_cursor",
		"SELECT 1 SETUSER 'dbo'",
		"SELECT 1 REVERT",
		"SELECT 1 USE master",
		"SELECT 1 DBCC CHECKDB",
	} {
		assert.ErrorIs(t, dbCore.GuardReadOnlyShape(sql, lex), dbCore.ErrReadOnly, "%q must be refused", sql)
		assert.ErrorIs(t, dbCore.GuardReadOnlySQL(sql, lex), dbCore.ErrReadOnly, "%q must be refused", sql)
	}
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT id FROM legacy DELETE FROM legacy", lex), "DELETE statement refused")

	// What SQL Server's dialect renders for a read still passes: every identifier bracketed,
	// TOP, OFFSET … FETCH NEXT, table hints that lock no more than a read does, CASE … ELSE …
	// END, FOR JSON and OPENJSON.
	for _, sql := range []string{
		"SELECT TOP (5) [Id], [Order] FROM [dbo].[2024Orders] WITH (NOLOCK) WHERE [Status] = @p1",
		"SELECT [Id] FROM [dbo].[2024Orders] WITH (READPAST, ROWLOCK) WHERE [Status] = @p1",
		"SELECT [Id] FROM [dbo].[2024Orders] ORDER BY [Id] OFFSET 10 ROWS FETCH NEXT 5 ROWS ONLY",
		"SELECT CASE WHEN [Begin] > 0 THEN 1 ELSE 0 END AS [Open] FROM [legacy]",
		"SELECT [Id] FROM [legacy] FOR JSON PATH",
		"SELECT [value] FROM OPENJSON(@p1)",
		"WITH [c] AS (SELECT [Id] FROM [legacy]) SELECT [Id] FROM [c]",
	} {
		assert.NoError(t, dbCore.GuardReadOnlyShape(sql, lex), "%q must pass", sql)
	}
}

func TestGuardReadOnlyShapeRefusesDataModifyingCTE(t *testing.T) {
	corpus{common: []string{
		"WITH gone AS (DELETE FROM legacy RETURNING id) SELECT id FROM gone",
		"WITH added AS MATERIALIZED (INSERT INTO legacy VALUES (1) RETURNING id) SELECT id FROM added",
		"WITH a AS (SELECT 1), changed AS NOT MATERIALIZED (UPDATE legacy SET x = 1 RETURNING id) SELECT * FROM changed",
		"WITH RECURSIVE a(n) AS (SELECT 1), b AS ((DELETE FROM legacy RETURNING id)) SELECT * FROM b",
		"WITH recent AS (SELECT id FROM legacy) DELETE FROM legacy WHERE id IN (SELECT id FROM recent)",
		"WITH recent AS (SELECT id FROM legacy) MERGE INTO legacy USING recent ON legacy.id = recent.id WHEN MATCHED THEN DELETE",
		"WITH outer_cte AS (WITH inner_cte AS (DELETE FROM legacy RETURNING id) SELECT id FROM inner_cte) SELECT id FROM outer_cte",
		"SELECT * FROM (WITH gone AS (DELETE FROM legacy RETURNING id) SELECT id FROM gone) AS s",
		"WITH gone AS DELETE FROM legacy",
	}}.each(t, refuses(dbCore.GuardReadOnlyShape, dbCore.ErrReadOnly))
}

func TestGuardReadOnlyShapeRefusesSecondStatementAndSelectInto(t *testing.T) {
	corpus{common: []string{
		"SELECT 1; SELECT 2",
		"SELECT 1; DELETE FROM legacy",
		"SELECT 1;; DELETE FROM legacy",
		"SELECT * INTO legacy_copy FROM legacy",
		"SELECT id INTO #scratch FROM legacy",
		"(SELECT * INTO legacy_copy FROM legacy)",
		"DELETE FROM legacy",
		"VALUES (1)",
		"SELECT 'unterminated",
	}, perEngine: map[string][]string{
		// SQL Server needs no ";" before the next statement.
		"tsql": {
			"SELECT id FROM legacy DELETE FROM legacy",
			"SELECT 1 INSERT legacy VALUES (1)",
			"SELECT 1 EXEC('DROP TABLE legacy')",
			"WITH c AS (SELECT 1 AS x) SELECT x FROM c UPDATE legacy SET name = 'x'",
		},
	}}.each(t, refuses(dbCore.GuardReadOnlyShape, dbCore.ErrReadOnly))

	lex := dbCore.LexiconTSQL
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT 1; SELECT 2", lex), "second statement refused")
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT 1; DELETE FROM legacy", lex), "DELETE statement refused")
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT * INTO legacy_copy FROM legacy", lex), "SELECT … INTO statement refused")
	assert.ErrorContains(t, dbCore.GuardReadOnlyShape("SELECT 'x", lex), "malformed (unterminated string literal) statement refused")
	assert.NoError(t, dbCore.GuardReadOnlyShape("SELECT 1;;", lex), "a trailing ';' starts no statement")
}

// ------------------------------------------------------------------------ the errors

// TestGuardReadOnlySQLErrorOmitsSQLText checks every guard's refusals for the SQL's own text.
// Raw SQL is where an app interpolates values, and a refusal is logged and returned to
// callers that may show it; it names what it refused in words of its own.
func TestGuardReadOnlySQLErrorOmitsSQLText(t *testing.T) {
	const name, value = "2024Orders", "do-not-echo"
	for _, c := range []struct {
		guard sqlGuard
		sql   string
	}{
		{dbCore.GuardReadOnlySQL, "SELECT 1; DELETE FROM [dbo].[2024Orders] WHERE Note = 'do-not-echo'"},
		{dbCore.GuardReadOnlySQL, "UPDATE [dbo].[2024Orders] SET Note = 'do-not-echo'"},
		{dbCore.GuardReadOnlySQL, "usp_2024Orders 'do-not-echo'"},
		{dbCore.GuardReadOnlySQL, "SELECT Note FROM [dbo].[2024Orders] WHERE Note = 'do-not-echo"},
		{dbCore.GuardReadOnlyShape, "SELECT * INTO [dbo].[2024Orders] FROM legacy WHERE Note = 'do-not-echo'"},
		{dbCore.GuardReadOnlyShape, "WITH x AS (DELETE FROM [dbo].[2024Orders] WHERE Note = 'do-not-echo') SELECT 1"},
		{dbCore.GuardReadOnlyShape, "SELECT 1; usp_2024Orders 'do-not-echo'"},
		{dbCore.GuardExternalSchemaSQL, "DROP TABLE [dbo].[2024Orders] -- do-not-echo"},
		{dbCore.GuardExternalSchemaSQL, "EXEC sp_rename '[dbo].[2024Orders]', 'do-not-echo'"},
		{dbCore.GuardExternalSchemaSQL, "SELECT * INTO [dbo].[2024Orders] FROM legacy WHERE Note = 'do-not-echo'"},
		{dbCore.GuardExternalSchemaSQL, "CREATE TABLE [dbo].[2024Orders] (Note nvarchar(20) DEFAULT 'do-not-echo"},
		{dbCore.GuardExternalSchemaSQL, "EXEC('DROP TABLE [dbo].[2024Orders] -- do-not-echo')"},
		{dbCore.GuardExternalSchemaSQL, "EXEC sp_executesql N'DELETE FROM [dbo].[2024Orders] WHERE Note = @n', N'@n nvarchar(20)', @n = N'do-not-echo'"},
		{dbCore.GuardExternalSchemaSQL, "EXEC @usp_2024Orders 'do-not-echo'"},
		{dbCore.GuardExternalSchemaSQL, "SELECT * FROM OPENQUERY([loopback], 'SELECT Note FROM [dbo].[2024Orders] WHERE Note = ''do-not-echo''')"},
		{dbCore.GuardExternalSchemaSQL, "EXEC [ｓｐ＿ｒｅｎａｍｅ] '[dbo].[2024Orders]', 'do-not-echo'"},
		{dbCore.GuardReadOnlySQL, "SELECT Note FROM [dbo].[2024Orders] WITH (UPDLOCK) WHERE Note = 'do-not-echo'"},
	} {
		err := c.guard(c.sql, dbCore.LexiconTSQL)
		require.Error(t, err, "%q must be refused", c.sql)
		assert.NotContains(t, err.Error(), name, "the error must not quote %q", c.sql)
		assert.NotContains(t, err.Error(), value, "the error must not quote %q", c.sql)
	}

	assert.EqualError(t, dbCore.GuardReadOnlySQL("DELETE FROM legacy", dbCore.LexiconTSQL),
		"datasource is read-only (read_only: true): DELETE statement refused; only SELECT or WITH … SELECT may run on this datasource (a safety net — use a read-only principal for a guarantee)")
	assert.ErrorContains(t, dbCore.GuardReadOnlySQL("usp_2024Orders 1", dbCore.LexiconTSQL), "non-SELECT statement refused",
		"a first word that is not a keyword is the SQL's own text, and is not quoted")
	assert.ErrorContains(t, dbCore.GuardReadOnlySQL("SELECT NEXT VALUE FOR legacy_seq", dbCore.LexiconTSQL), "NEXT VALUE FOR statement refused")
	assert.EqualError(t, dbCore.GuardExternalSchemaSQL("DROP TABLE legacy", dbCore.LexiconTSQL),
		"datasource schema is owned outside gorgany (external_schema: true): DROP statement refused; rows may be read and written on this datasource, but its schema is changed only by its owner (a safety net — use a principal without DDL rights for a guarantee)")
	assert.ErrorContains(t, dbCore.GuardExternalSchemaSQL("EXEC [sys].[sp_rename] 'a', 'b'", dbCore.LexiconTSQL), "sp_rename statement refused")

	// Dynamic SQL is refused for what it hides, not for a change it is known to make, and its
	// refusal says so — and still quotes nothing of the text it could not check.
	assert.EqualError(t, dbCore.GuardExternalSchemaSQL("EXEC('DROP TABLE legacy')", dbCore.LexiconTSQL),
		"datasource schema is owned outside gorgany (external_schema: true): EXEC (…) statement refused; dynamic SQL cannot be checked, so send the statement itself (a safety net — use a principal without DDL rights for a guarantee)")
	for _, c := range []struct {
		lex  dbCore.SQLLexicon
		sql  string
		want string
	}{
		{dbCore.LexiconTSQL, "EXEC [sys].[sp_executesql] @stmt", ": sp_executesql statement refused; dynamic SQL cannot be checked"},
		{dbCore.LexiconTSQL, "EXEC @proc N'do-not-echo'", ": EXEC @variable statement refused; dynamic SQL cannot be checked"},
		{dbCore.LexiconPostgres, "DO $$ BEGIN DROP TABLE do_not_echo; END $$", ": DO statement refused; dynamic SQL cannot be checked"},
		{dbCore.LexiconMySQL, "PREPARE do_not_echo FROM 'DROP TABLE legacy'", ": PREPARE … FROM statement refused; dynamic SQL cannot be checked"},
	} {
		err := dbCore.GuardExternalSchemaSQL(c.sql, c.lex)
		assert.ErrorIs(t, err, dbCore.ErrExternalSchema, "%q must be refused", c.sql)
		assert.ErrorContains(t, err, c.want)
		assert.NotContains(t, err.Error(), "do_not_echo", "the error must not quote %q", c.sql)
		assert.NotContains(t, err.Error(), "do-not-echo", "the error must not quote %q", c.sql)
	}

	// The refusal names the statement without an article, which no fixed one fits: "a
	// INSERT", "a ALTER" and "a EXEC" are what "refusing a %s statement" made of these.
	for sql, want := range map[string]string{
		"INSERT INTO legacy (name) VALUES (1)": ": INSERT statement refused;",
		"UPDATE legacy SET name = 1":           ": UPDATE statement refused;",
		"EXEC usp_legacy":                      ": EXEC statement refused;",
	} {
		assert.ErrorContains(t, dbCore.GuardReadOnlySQL(sql, dbCore.LexiconTSQL), want)
	}
	assert.ErrorContains(t, dbCore.GuardExternalSchemaSQL("ALTER TABLE legacy ADD Note int", dbCore.LexiconTSQL), ": ALTER statement refused;")
}

// ------------------------------------------------------------ GuardExternalSchemaSQL

func TestGuardExternalSchemaRefusesDDLButAllowsTempTables(t *testing.T) {
	tsql := dbCore.LexiconTSQL
	refused := []string{
		"CREATE TABLE [dbo].[2024Orders] (Id int)",
		"create table legacy_copy (Id int)",
		"ALTER TABLE legacy ADD Note nvarchar(50)",
		"DROP TABLE legacy",
		"DROP TABLE [dbo].[Real], #t",
		"DROP TABLE #t, [dbo].[Real]",
		"DROP TABLE IF EXISTS #t, legacy",
		"TRUNCATE TABLE legacy",
		"GRANT SELECT ON legacy TO [user@example.com]",
		"REVOKE SELECT ON legacy FROM [user@example.com]",
		"DENY SELECT ON legacy TO [user@example.com]",
		"CREATE INDEX ix ON legacy (Note)",
		"CREATE INDEX ix ON #t (Id); DROP INDEX ix ON legacy",
		"CREATE VIEW legacy_view AS SELECT 1 AS x",

		// gorm's SQL Server migrator renames through sp_rename and syncs column comments
		// through the extended-property procedures, reached from AutoMigrate.
		"sp_rename @objname = ?, @newname = ?",
		"EXEC sp_rename @objname = ?, @newname = ?, @objtype = 'COLUMN'",
		"EXEC [sys].[sp_rename] 'legacy.Note', 'Remark', 'COLUMN'",
		"EXECUTE dbo.sp_rename 'legacy', 'legacy_old'",
		"EXEC master..sp_rename 'legacy', 'legacy_old'",
		"EXEC @status = sp_rename 'legacy', 'legacy_old'",
		"exec SP_RENAME 'legacy', 'legacy_old'",
		"EXEC sp_updateextendedproperty 'MS_Description', 'x', 'SCHEMA', 'dbo', 'TABLE', 'legacy'",
		"IF NOT EXISTS (SELECT 1 FROM sys.extended_properties) EXEC sp_addextendedproperty @name = N'MS_Description', @value = N'x'",
		"EXEC sp_dropextendedproperty 'MS_Description'",
		"EXEC sp_changeobjectowner 'legacy', 'dbo'",

		// Dynamic SQL: the statement it runs is a value the guard cannot read, so whatever the
		// value is, it is refused — wherever an EXEC stands, since SQL Server needs no ";".
		"EXEC('DROP TABLE legacy')",
		"EXEC ('DROP TABLE legacy')",
		"exec('DROP TABLE legacy')",
		"EXECUTE(N'DROP TABLE legacy')",
		"EXECUTE (N'DROP TABLE legacy')",
		"EXEC('DR' + 'OP TABLE legacy')",
		"EXEC(N'ALTER TABLE ' + @name + N' ADD Note int')",
		"EXEC(@sql)",
		"EXEC (@sql)",
		"DECLARE @sql nvarchar(max) = N'DROP TABLE legacy'; EXEC (@sql)",
		"EXEC('DROP TABLE legacy') AT [loopback]",
		"IF OBJECT_ID('legacy') IS NOT NULL EXEC('DROP TABLE legacy')",
		"SELECT 1 EXEC('DROP TABLE legacy')",
		"BEGIN TRY EXEC('DROP TABLE legacy') END TRY BEGIN CATCH END CATCH",
		"INSERT INTO legacy_log (Note) EXEC('SELECT 1')",
		"EXEC sp_executesql N'DROP TABLE legacy'",
		"EXECUTE sp_executesql @stmt = N'DROP TABLE legacy'",
		"exec SP_EXECUTESQL @stmt",
		"EXEC sys.sp_executesql N'DROP TABLE legacy'",
		"EXEC [sys].[sp_executesql] @stmt",
		"EXEC [sp_executesql] @stmt",
		"EXEC master.sys.sp_executesql @stmt",
		"EXEC master..sp_executesql @stmt",
		"EXEC @rc = sp_executesql @stmt",
		"sp_executesql N'DROP TABLE legacy'", // a batch's first statement needs no EXEC
		"[sys].[sp_executesql] @stmt",
		"SELECT 1 EXEC sp_executesql @stmt",
		"SET NOCOUNT ON; EXEC sp_executesql @stmt",
		"IF 1 = 1 EXEC sp_executesql @stmt",
		"INSERT INTO legacy_log (Note) EXEC sp_executesql N'SELECT 1'",
		"EXEC sp_prepexec @handle OUTPUT, NULL, N'DROP TABLE legacy'",
		"EXEC sp_prepare @handle OUTPUT, NULL, N'DROP TABLE legacy'",
		"EXEC sp_cursoropen @cursor OUTPUT, N'DROP TABLE legacy'",
		"EXEC sp_MSforeachtable 'DROP TABLE ?'",
		"EXEC sp_MSforeachdb 'USE ? DROP TABLE legacy'",
		// A procedure the guard cannot name may be sp_executesql.
		"EXEC @proc N'DROP TABLE legacy'",
		"DECLARE @proc sysname = N'sp_executesql'; EXEC @proc N'DROP TABLE legacy'",
		"EXEC @rc = @proc",
		"EXEC 'sp_executesql'",
		"EXEC N'sp_executesql'",

		"SELECT * INTO dbo.Copy FROM legacy",
		"SELECT * INTO [dbo].[2024Orders] FROM legacy",
		"WITH c AS (SELECT 1 AS x) SELECT * INTO dbo.Copy FROM c",
		"SELECT output INTO dbo.Copy FROM legacy", // a column named output is no OUTPUT clause
		"SELECT Id INTO @ids FROM legacy",         // MySQL's rules alone read INTO @x as variables

		// SQL Server needs no ";" before the next statement.
		"IF OBJECT_ID('legacy') IS NOT NULL DROP TABLE legacy",
		"SELECT 1 DROP TABLE legacy",
		"CREATE TABLE #scratch (Id int) CREATE TABLE legacy_copy (Id int)",
		"INSERT INTO legacy VALUES (1) SELECT * INTO dbo.Copy FROM legacy",
		"BEGIN TRANSACTION; ALTER TABLE legacy ADD Note int; COMMIT",

		// Switching the owner's triggers off is a change to the schema's rules.
		"DISABLE TRIGGER trg_legacy ON legacy",
		"ENABLE TRIGGER ALL ON legacy",
		"SELECT 1 DISABLE TRIGGER trg_legacy ON legacy",

		"CREATE TABLE 'unterminated",
	}
	for _, sql := range refused {
		assert.ErrorIs(t, dbCore.GuardExternalSchemaSQL(sql, tsql), dbCore.ErrExternalSchema, "%q must be refused", sql)
	}

	allowed := []string{
		"CREATE TABLE #scratch (Id int)",
		"CREATE TABLE [#scratch] (Id int)",
		"ALTER TABLE #scratch ADD Note nvarchar(50)",
		"ALTER TABLE #scratch ALTER COLUMN Note nvarchar(100)",
		"ALTER TABLE #scratch DROP COLUMN Note",
		"ALTER TABLE #scratch DROP CONSTRAINT pk_scratch",
		"TRUNCATE TABLE #scratch",
		"DROP TABLE #scratch",
		"DROP TABLE IF EXISTS #a, #b",
		"CREATE INDEX i ON #t (Id)",
		"CREATE UNIQUE NONCLUSTERED INDEX i ON #t (Id)",
		"CREATE CLUSTERED INDEX i ON [#t] (Id)",
		"SELECT * INTO #c FROM legacy",
		"SELECT * INTO ##shared FROM legacy",

		// Rows may be read and written.
		"SELECT [create], [drop] FROM legacy",
		"SELECT 'DROP TABLE legacy'",
		"INSERT INTO legacy (Note) VALUES (@p1)",
		"INSERT INTO legacy (Id) SELECT Id FROM legacy_staged",
		"UPDATE [dbo].[2024Orders] SET [Note] = @p1 WHERE [Id] = @p2",
		"UPDATE legacy SET Note = (SELECT MAX(Note) FROM legacy_staged) OUTPUT inserted.Id INTO @ids",
		"DELETE FROM legacy OUTPUT deleted.Id INTO dbo.Archive WHERE Id = 1",
		"MERGE INTO legacy AS t USING (SELECT 1 AS Id) AS s ON t.Id = s.Id WHEN MATCHED THEN UPDATE SET Note = 'x';",
		"INSERT INTO legacy (Note) VALUES (@p1); SELECT CAST(SCOPE_IDENTITY() AS BIGINT) AS [id], ROWCOUNT_BIG() AS [affected]",
		"EXEC dbo.usp_archive_orders @p1",
		// A procedure called by name runs server code, as a function does; the principal's
		// rights are what stand between it and the schema.
		"EXEC dbo.usp_legacy 1",
		"EXECUTE usp_legacy @p1 = 1, @p2 = N'x'",
		"EXEC @rc = [dbo].[usp_legacy] @p1",
		"usp_legacy 1",
		"INSERT INTO legacy_log (Note) EXEC dbo.usp_legacy",
		"SELECT 'EXEC(''DROP TABLE legacy'')', N'sp_executesql'",
		"SELECT name FROM sys.procedures WHERE name = N'sp_executesql'",
		"SELECT [exec], [sp_executesql] FROM legacy",
		"DECLARE @n int; SET @n = 1; SELECT @n",
		"SET NOCOUNT ON",
		"SET SHOWPLAN_XML ON", // plans without running
		"SELECT [disable], [trigger] FROM legacy",
		"SELECT [cluster], [refresh], [security] FROM legacy",
	}
	for _, sql := range allowed {
		assert.NoError(t, dbCore.GuardExternalSchemaSQL(sql, tsql), "%q must pass", sql)
	}
}

func TestGuardExternalSchemaOnPostgresAndMySQL(t *testing.T) {
	pg, my := dbCore.LexiconPostgres, dbCore.LexiconMySQL
	for _, c := range []struct {
		lex dbCore.SQLLexicon
		sql string
	}{
		{pg, `COMMENT ON COLUMN legacy.note IS 'x'`}, // gorm's Postgres migrator syncs comments this way
		{pg, "CREATE TEMPORARY TABLE scratch (id int)"},
		{pg, "SELECT * INTO TEMP scratch FROM legacy"},
		{pg, "ALTER TABLE legacy RENAME COLUMN a TO b"},
		{pg, "CREATE INDEX CONCURRENTLY ix ON legacy (note)"},
		{pg, "BEGIN; CREATE TABLE legacy_copy (id int); COMMIT"},
		{my, "RENAME TABLE legacy TO legacy_old"},
		{my, "CREATE TABLE #scratch (id int)"}, // # opens a comment on MySQL, so there is no temp table here
		{my, "ALTER TABLE legacy ADD COLUMN note TEXT"},

		// A setting that switches the owner's constraints off outlives the statement, on the
		// pooled connection, for every write that follows on it.
		{my, "SET FOREIGN_KEY_CHECKS = 0;"}, // gorm's MySQL Migrator().DropTable, before its DROP
		{my, "set foreign_key_checks=0"},
		{my, "SET @@foreign_key_checks = 0"},
		{my, "SET @@SESSION.foreign_key_checks = 0"},
		{my, "SET SESSION foreign_key_checks = 0"},
		{my, "SET NAMES utf8mb4, FOREIGN_KEY_CHECKS = 0"},
		{my, "/*!40014 SET FOREIGN_KEY_CHECKS=0 */"},
		{my, "SET STATEMENT foreign_key_checks = 0 FOR DELETE FROM legacy"},
		{my, "SELECT 1; SET FOREIGN_KEY_CHECKS = 1"},
		{pg, "SET session_replication_role = replica"},
		{pg, "SET LOCAL session_replication_role TO replica"},
		{pg, `SET "session_replication_role" = replica`},

		// Dynamic SQL: the statement it runs is a value the guard cannot read.
		{pg, "DO $$ BEGIN EXECUTE 'DROP TABLE legacy'; END $$"},
		{pg, "DO $$ BEGIN DROP TABLE legacy; END $$"},
		{pg, "DO LANGUAGE plpgsql $$ BEGIN DROP TABLE legacy; END $$"},
		{pg, "DO $body$ BEGIN DROP TABLE legacy; END $body$ LANGUAGE plpgsql"},
		{pg, "do 'BEGIN DROP TABLE legacy; END'"},
		{pg, `DO E'BEGIN DROP TABLE legacy; END'`},
		{pg, "SELECT 1; DO $$ BEGIN DROP TABLE legacy; END $$"},
		{pg, "DO RELEASE_LOCK('legacy')"}, // MySQL's DO; on Postgres every DO is a code block
		{my, "PREPARE stmt FROM 'DROP TABLE legacy'"},
		{my, "PREPARE stmt FROM @sql"},
		{my, "prepare `stmt` from @sql"},
		{my, "PREPARE 1st FROM @sql"}, // a name MySQL allows and the tokenizer reads as a number
		{my, "SET @sql = 'DROP TABLE legacy'; PREPARE stmt FROM @sql; EXECUTE stmt"},
		{my, "/*!50000 PREPARE stmt FROM @sql */"},

		// SELECT … INTO anything but @variables: a table on every engine, and on MySQL a file
		// the server writes.
		{my, "SELECT * INTO legacy_copy FROM legacy"},
		{my, "SELECT * FROM legacy INTO OUTFILE '/tmp/legacy.csv'"},
		{my, "SELECT note FROM legacy WHERE id = 1 INTO DUMPFILE '/tmp/note'"},
		{my, "SELECT id INTO @id, legacy_copy FROM legacy"},
		{my, "SELECT id INTO @@foreign_key_checks FROM legacy"},
		{pg, "SELECT id INTO @id FROM legacy"}, // MySQL's rules alone read INTO @x as variables

		// EXPLAIN ANALYZE runs what it explains, so that is checked as if it stood alone.
		{pg, "EXPLAIN ANALYZE CREATE TABLE legacy_copy AS SELECT 1"},
		{pg, "explain analyse create table legacy_copy as select 1"},
		{pg, "EXPLAIN ANALYZE VERBOSE CREATE MATERIALIZED VIEW legacy_mv AS SELECT * FROM legacy"},
		{pg, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) CREATE TABLE legacy_copy AS SELECT * FROM legacy"},
		{pg, "EXPLAIN (ANALYZE) SELECT * INTO legacy_copy FROM legacy"},
		{pg, "EXPLAIN CREATE TABLE legacy_copy AS SELECT 1"}, // no ANALYZE runs nothing, but reads as DDL
		{pg, "SELECT 1; EXPLAIN ANALYZE CREATE TABLE legacy_copy AS SELECT 1"},
		{pg, "EXPLAIN ANALYZE DO $$ BEGIN DROP TABLE legacy; END $$"},
		{my, "EXPLAIN ANALYZE FORMAT=TREE CREATE TABLE legacy_copy AS SELECT 1"}, // MySQL explains no DDL; the guard does not rely on it
		{my, "DESCRIBE FORMAT=JSON SELECT * FROM legacy INTO OUTFILE '/tmp/plan'"},

		// Postgres's other schema statements, and the ones that rebuild what the owner manages.
		{pg, "IMPORT FOREIGN SCHEMA remote FROM SERVER legacy_server INTO public"},
		{my, "IMPORT TABLE FROM '/tmp/legacy_*.sdi'"},
		{pg, "REASSIGN OWNED BY legacy_owner TO app_user"},
		{pg, "SECURITY LABEL FOR selinux ON TABLE legacy IS 'system_u:object_r:sepgsql_table_t:s0'"},
		{pg, "REFRESH MATERIALIZED VIEW legacy_mv"},
		{pg, "REFRESH MATERIALIZED VIEW CONCURRENTLY legacy_mv WITH DATA"},
		{pg, "CLUSTER legacy USING legacy_pkey"},
		{pg, "CLUSTER"},
		{pg, "REINDEX TABLE legacy"},
		{pg, "REINDEX INDEX CONCURRENTLY legacy_pkey"},
		{pg, "VACUUM FULL legacy"},
		{pg, "VACUUM FULL VERBOSE ANALYZE legacy"},
		{pg, "vacuum full"},
		{pg, "VACUUM (FULL) legacy"},
		{pg, "VACUUM (VERBOSE, FULL true, ANALYZE) legacy"},
		{my, "OPTIMIZE TABLE legacy"},
		{my, "OPTIMIZE NO_WRITE_TO_BINLOG TABLE legacy, legacy_archive"},
		{my, "REPAIR TABLE legacy QUICK"},
	} {
		assert.ErrorIs(t, dbCore.GuardExternalSchemaSQL(c.sql, c.lex), dbCore.ErrExternalSchema, "%q must be refused", c.sql)
	}

	for _, c := range []struct {
		lex dbCore.SQLLexicon
		sql string
	}{
		{pg, "INSERT INTO legacy (note) VALUES ($1) RETURNING id"},
		{pg, "SELECT $$DROP TABLE legacy$$"},
		{pg, "WITH moved AS (DELETE FROM legacy RETURNING *) INSERT INTO legacy_archive SELECT * FROM moved"},
		{pg, "UPDATE legacy SET note = 'CREATE TABLE x'"},
		{pg, "EXEC sp_rename 'a', 'b'"}, // the procedure rules are SQL Server's
		{my, "REPLACE INTO legacy VALUES (1)"},
		{my, "INSERT IGNORE INTO legacy VALUES (1)"},
		{my, "SELECT TRUNCATE(price, 2) FROM legacy"}, // a function on MySQL, not a statement
		{my, `UPDATE legacy SET note = 'it\'s DROP TABLE'`},
		{my, "SET NAMES utf8mb4"},
		{my, "SELECT @@foreign_key_checks"},
		{my, "UPDATE legacy SET note = 'SET FOREIGN_KEY_CHECKS = 0'"},
		{pg, "SET search_path TO legacy"},
		{pg, "SHOW session_replication_role"},
		{pg, "RESET session_replication_role"},

		// DO that is no code block, and prepared statements whose SQL is in sight.
		{pg, "INSERT INTO legacy (id) VALUES (1) ON CONFLICT (id) DO NOTHING"},
		{pg, "INSERT INTO legacy (id) VALUES (1) ON CONFLICT (id) DO UPDATE SET note = EXCLUDED.note"},
		{pg, "SELECT 'DO $$ BEGIN DROP TABLE legacy; END $$'"},
		{pg, "PREPARE fetch_legacy (int) AS SELECT * FROM legacy WHERE id = $1"},
		{pg, "PREPARE touch_legacy AS UPDATE legacy SET note = 'x'"},
		{pg, "EXECUTE fetch_legacy (1)"},
		{pg, "DEALLOCATE fetch_legacy"},
		{my, "DO RELEASE_LOCK('legacy')"}, // MySQL's DO evaluates expressions
		{my, "DO SLEEP(1)"},
		{my, "EXECUTE stmt USING @id"},
		{my, "DEALLOCATE PREPARE stmt"},
		{my, "SELECT 'PREPARE stmt FROM @sql'"},

		// MySQL's SELECT … INTO @variable assigns user variables and creates no table.
		{my, "SELECT COUNT(*) INTO @n FROM legacy"},
		{my, "SELECT id, note INTO @id, @note FROM legacy WHERE id = 1"},
		{my, "SELECT id FROM legacy WHERE id = 1 INTO @id"},
		{my, "SELECT id FROM legacy WHERE id = 1 FOR UPDATE INTO @id"},
		{my, "select max(id) into @max_id from legacy"},

		// EXPLAIN of what would pass alone passes.
		{pg, "EXPLAIN SELECT * FROM legacy"},
		{pg, "EXPLAIN ANALYZE SELECT * FROM legacy WHERE id = $1"},
		{pg, "EXPLAIN (ANALYZE, FORMAT JSON) UPDATE legacy SET note = 'x' WHERE id = 1"},
		{pg, "EXPLAIN VERBOSE WITH c AS (SELECT 1 AS x) SELECT x FROM c"},
		{my, "EXPLAIN FORMAT=JSON SELECT * FROM legacy"},
		{my, "EXPLAIN ANALYZE SELECT * FROM legacy"},
		{my, "EXPLAIN FORMAT=JSON INTO @plan SELECT * FROM legacy"},
		{my, "DESCRIBE legacy"},
		{my, "EXPLAIN legacy"},

		// Maintenance that rewrites nothing: a plain VACUUM and ANALYZE.
		{pg, "VACUUM legacy"},
		{pg, "VACUUM ANALYZE legacy"},
		{pg, "VACUUM FREEZE VERBOSE legacy"},
		{pg, "VACUUM (VERBOSE, ANALYZE) legacy"},
		{pg, "ANALYZE legacy"},
		{pg, "SELECT * FROM legacy_mv"},
		{my, "ANALYZE TABLE legacy"},
	} {
		assert.NoError(t, dbCore.GuardExternalSchemaSQL(c.sql, c.lex), "%q must pass", c.sql)
	}

	// A refusal names the statement by its phrase, and an EXPLAIN by the statement it explains.
	for sql, want := range map[string]string{
		"IMPORT FOREIGN SCHEMA remote FROM SERVER legacy_server INTO public": ": IMPORT FOREIGN SCHEMA statement refused;",
		"REASSIGN OWNED BY legacy_owner TO app_user":                         ": REASSIGN OWNED statement refused;",
		"SECURITY LABEL ON TABLE legacy IS 'x'":                              ": SECURITY LABEL statement refused;",
		"REFRESH MATERIALIZED VIEW legacy_mv":                                ": REFRESH MATERIALIZED VIEW statement refused;",
		"REINDEX TABLE legacy":                                               ": REINDEX statement refused;",
		"VACUUM (FULL) legacy":                                               ": VACUUM FULL statement refused;",
		"EXPLAIN ANALYZE CREATE TABLE legacy_copy AS SELECT 1":               ": CREATE statement refused;",
	} {
		assert.ErrorContains(t, dbCore.GuardExternalSchemaSQL(sql, pg), want)
	}
}

// TestGuardExternalSchemaRefusesLinkedServerSQL: OPENQUERY, OPENROWSET and OPENDATASOURCE have
// a linked server run SQL — a query's text, or whatever is called through the server — that
// the guard does not read, and a linked server can be this one, so on SQL Server's rules each
// is refused wherever it stands. The read-only guards refuse them already, as words no read
// needs.
func TestGuardExternalSchemaRefusesLinkedServerSQL(t *testing.T) {
	tsql := dbCore.LexiconTSQL
	const throughOpenDataSource = "EXEC OPENDATASOURCE('MSOLEDBSQL', 'Data Source=(local);Integrated Security=SSPI').reports.sys.sp_executesql N'DROP TABLE legacy'"
	for _, sql := range []string{
		"SELECT * FROM OPENQUERY([loopback], 'SELECT 1 AS x; DROP TABLE legacy')",
		"select * from openquery(loopback, 'SELECT 1')",
		"SELECT Id FROM legacy WHERE Id IN (SELECT Id FROM OPENQUERY([loopback], 'SELECT 1 AS Id'))",
		"WITH c AS (SELECT * FROM OPENQUERY([loopback], 'SELECT 1 AS x')) SELECT x FROM c",
		"SELECT * INTO #scratch FROM OPENQUERY([loopback], 'SELECT 1 AS x')", // a #temp target exempts only the INTO
		"INSERT INTO OPENQUERY([loopback], 'SELECT Note FROM legacy') VALUES (N'x')",
		"UPDATE OPENQUERY([loopback], 'SELECT Note FROM legacy') SET Note = N'x'",
		"DELETE OPENQUERY([loopback], 'SELECT Id FROM legacy')",
		"SELECT * FROM OPENROWSET('MSOLEDBSQL', 'Server=(local);Trusted_Connection=yes;', 'SELECT 1 AS x; DROP TABLE legacy')",
		"SELECT BulkColumn FROM OPENROWSET(BULK '/tmp/legacy.csv', SINGLE_CLOB) AS f",
		"SELECT * FROM OPENDATASOURCE('MSOLEDBSQL', 'Data Source=(local);Integrated Security=SSPI').reports.dbo.legacy",
		// The procedure's name does not follow the EXEC, so only the function gives it away.
		throughOpenDataSource,
		"SELECT 1 SELECT * FROM OPENQUERY([loopback], 'SELECT 1 AS x')",
	} {
		assert.ErrorIs(t, dbCore.GuardExternalSchemaSQL(sql, tsql), dbCore.ErrExternalSchema, "%q must be refused", sql)
		assert.ErrorIs(t, dbCore.GuardReadOnlySQL(sql, tsql), dbCore.ErrReadOnly, "%q must be refused", sql)
		assert.ErrorIs(t, dbCore.GuardReadOnlyShape(sql, tsql), dbCore.ErrReadOnly, "%q must be refused", sql)
	}

	for _, sql := range []string{
		"SELECT [openquery], [openrowset], [opendatasource] FROM legacy",
		`SELECT "openquery" FROM legacy`,
		"SELECT 'OPENQUERY([loopback], ''DROP TABLE legacy'')'",
		"SELECT 1 -- OPENQUERY([loopback], 'DROP TABLE legacy')",
		"SELECT * FROM loopback.reports.dbo.legacy", // a four-part name is a statement the guard reads
		"UPDATE loopback.reports.dbo.legacy SET Note = @p1 WHERE Id = @p2",
		"SELECT [value] FROM OPENJSON(@p1)", // OPENJSON and OPENXML parse a value; no server runs anything
		"SELECT * FROM OPENXML(@doc, N'/root', 1)",
	} {
		assert.NoError(t, dbCore.GuardExternalSchemaSQL(sql, tsql), "%q must pass", sql)
	}
	for _, lex := range []dbCore.SQLLexicon{dbCore.LexiconPostgres, dbCore.LexiconMySQL} {
		assert.NoError(t, dbCore.GuardExternalSchemaSQL("SELECT openquery, openrowset FROM legacy", lex),
			"the words are SQL Server's; elsewhere they are names")
	}

	// The refusal says what it cannot check, as dynamic SQL's does, and quotes nothing of it.
	// One wording covers the three ways in: a linked server (OPENQUERY), an ad hoc OLE DB
	// provider string (OPENROWSET, OPENDATASOURCE), and a file (OPENROWSET(BULK …)), none of
	// which a linked server's four-part name is the whole answer to.
	const cannotCheck = " statement refused; the SQL it hands to a linked server or an OLE DB provider, " +
		"or the file it reads, cannot be checked, so reach that data another way, such as a linked " +
		"server's four-part name (a safety net — use a principal without DDL rights for a guarantee)"
	for sql, refused := range map[string]string{
		"SELECT * FROM OPENQUERY([loopback], 'DROP TABLE legacy')":                             "OPENQUERY",
		"select * from openrowset('MSOLEDBSQL', 'x', 'SELECT 1')":                              "OPENROWSET",
		"SELECT BulkColumn FROM OPENROWSET(BULK '/tmp/legacy.csv', SINGLE_CLOB) AS f":          "OPENROWSET",
		"SELECT * FROM OPENDATASOURCE('MSOLEDBSQL', 'Data Source=(local)').reports.dbo.legacy": "OPENDATASOURCE",
		throughOpenDataSource: "OPENDATASOURCE",
	} {
		err := dbCore.GuardExternalSchemaSQL(sql, tsql)
		assert.EqualError(t, err, dbCore.ErrExternalSchema.Error()+": "+refused+cannotCheck, "%q", sql)
		assert.NotContains(t, err.Error(), "reports")
		assert.NotContains(t, err.Error(), "legacy")
		assert.NotContains(t, err.Error(), "loopback")
	}

	// Anything else the statement does that is refused is named first, so an operator who
	// replaces the function is not met by a second refusal of the same statement.
	for sql, want := range map[string]string{
		"SELECT * FROM OPENQUERY([loopback], 'SELECT 1') DROP TABLE legacy":       ": DROP statement refused; rows may be read",
		"SELECT * INTO legacy_copy FROM OPENQUERY([loopback], 'SELECT 1 AS x')":   ": SELECT … INTO statement refused; rows may be read",
		"SELECT * FROM OPENROWSET(BULK '/tmp/x.csv', SINGLE_CLOB) AS f EXEC (@s)": ": EXEC (…) statement refused; dynamic SQL cannot be checked",
		"SELECT * FROM OPENQUERY([loopback], 'SELECT 1'); DROP TABLE legacy":      ": OPENQUERY statement refused;", // a statement of its own, which is refused first
	} {
		assert.ErrorContains(t, dbCore.GuardExternalSchemaSQL(sql, tsql), want, "%q", sql)
	}
}

// TestGuardExternalSchemaFoldsProcedureNames: SQL Server looks a procedure's name up under
// the database's collation, and under a width- or accent-insensitive one [ｓｐ＿ｒｅｎａｍｅ] and
// [sp_rénamé] are sp_rename. So a refused procedure is refused under every spelling such a
// collation would resolve to it, and the refusal names it in the guard's spelling, not the
// SQL's. Whole names are compared: folding makes no prefix a match.
func TestGuardExternalSchemaFoldsProcedureNames(t *testing.T) {
	tsql := dbCore.LexiconTSQL
	for _, c := range []struct{ sql, want string }{
		{"EXEC [ｓｐ＿ｒｅｎａｍｅ] 'legacy', 'legacy_old'", "sp_rename"}, // fullwidth, low line included
		{"EXEC sp_ｒｅｎａｍｅ 'legacy', 'legacy_old'", "sp_rename"},   // a fullwidth letter is a letter, so this is one word
		{"EXEC [SP_ＲＥＮＡＭＥ] 'legacy', 'legacy_old'", "sp_rename"}, // fullwidth capitals
		{"EXEC [sp_rénamé] 'legacy', 'legacy_old'", "sp_rename"}, // precomposed accents
		{"EXEC sp_rénamé 'legacy', 'legacy_old'", "sp_rename"},   // é is a letter, so this is one word
		{"EXEC [sp_réname] 'legacy', 'legacy_old'", "sp_rename"},
		{"EXEC \"sp_réname\" 'legacy', 'legacy_old'", "sp_rename"},
		{"EXEC [ſp_rename] 'legacy', 'legacy_old'", "sp_rename"},       // a long s
		{"EXEC [sp_rena­me] 'legacy', 'legacy_old'", "sp_rename"},      // a soft hyphen, which weighs nothing
		{"EXEC [sp_rena​me] 'legacy', 'legacy_old'", "sp_rename"},      // a zero-width space, likewise
		{"EXEC [sys].[sp_ｒｅｎａｍｅ] 'legacy', 'legacy_old'", "sp_rename"}, // only the last part counts
		{"[ｓｐ＿ｒｅｎａｍｅ] 'legacy', 'legacy_old'", "sp_rename"},            // a batch's first statement needs no EXEC
		{"EXEC [sp_addéxtendedproperty] 'MS_Description', 'x'", "sp_addextendedproperty"},
		{"EXEC [sp_bindrulé] 'rule', 'legacy.Note'", "sp_bindrule"},
		{"EXEC sp_ｅｘｅｃｕｔｅｓｑｌ @stmt", "sp_executesql"},
		{"EXEC [sys].[ｓｐ＿ｅｘｅｃｕｔｅｓｑｌ] N'DROP TABLE legacy'", "sp_executesql"},
		{"sp_éxécutésql N'DROP TABLE legacy'", "sp_executesql"},
		{"EXEC [sp_MSforeachtablé] 'DROP TABLE ?'", "sp_msforeachtable"},
	} {
		err := dbCore.GuardExternalSchemaSQL(c.sql, tsql)
		if !assert.ErrorIs(t, err, dbCore.ErrExternalSchema, "%q must be refused", c.sql) {
			continue
		}
		assert.ErrorContains(t, err, ": "+c.want+" statement refused;", "%q is refused as %s", c.sql, c.want)
		for _, r := range c.sql {
			if r >= utf8.RuneSelf {
				assert.NotContains(t, err.Error(), string(r), "the refusal of %q names the procedure in its own spelling", c.sql)
			}
		}
	}
	assert.ErrorContains(t, dbCore.GuardExternalSchemaSQL("EXEC sp_ｅｘｅｃｕｔｅｓｑｌ @stmt", tsql),
		"dynamic SQL cannot be checked", "a folded name still counts as dynamic SQL")

	for _, sql := range []string{
		"EXEC [sp_renamé_legacy] 1",  // folds to sp_rename_legacy: a whole name, not a prefix
		"EXEC [ｓｐ＿ｗｈｏ]",              // folds to sp_who, which changes nothing
		"EXEC [sp_ﬁnd] 1",            // folds to sp_find, likewise
		"EXEC dbo.[usp_ｒｅｐｏｒｔｓ] @p1", // an app's own procedure, however it is spelt
		"SELECT N'ｓｐ＿ｒｅｎａｍｅ', [sp_rénamé] FROM legacy",
	} {
		assert.NoError(t, dbCore.GuardExternalSchemaSQL(sql, tsql), "%q must pass", sql)
	}
	assert.NoError(t, dbCore.GuardExternalSchemaSQL("EXEC [ｓｐ＿ｒｅｎａｍｅ] 'a', 'b'", dbCore.LexiconPostgres),
		"the procedure rules are SQL Server's")
}

// TestGuardsFoldNamesOnlyToRefuse pins that folding makes the guards refuse more and never
// less. Every spelling refused before names were folded, when the guard compared them with
// strings.ToLower, is still refused — including those with the characters outside ASCII that
// strings.ToLower takes into it, the dotted capital I and the Kelvin sign. And keywords are
// never folded: the server's parser reads them in ASCII, so a word that only folds to SELECT,
// WITH, TABLE or COLUMN is a name to it, and reading it as the keyword would let through a
// statement the server does not read as the guard did.
func TestGuardsFoldNamesOnlyToRefuse(t *testing.T) {
	tsql := dbCore.LexiconTSQL

	// The characters outside ASCII that strings.ToLower maps into it, by what it maps them to.
	lowersToASCII := map[byte][]rune{}
	for r := rune(utf8.RuneSelf); r <= unicode.MaxRune; r++ {
		if lower := strings.ToLower(string(r)); len(lower) == 1 && lower[0] < utf8.RuneSelf {
			lowersToASCII[lower[0]] = append(lowersToASCII[lower[0]], r)
		}
	}
	require.Contains(t, lowersToASCII, byte('i'), "the dotted capital I")
	require.Contains(t, lowersToASCII, byte('k'), "the Kelvin sign")

	for _, name := range []string{
		"sp_rename", "sp_addextendedproperty", "sp_updateextendedproperty", "sp_dropextendedproperty",
		"sp_addtype", "sp_droptype", "sp_bindrule", "sp_unbindrule", "sp_bindefault", "sp_unbindefault",
		"sp_changeobjectowner", "sp_executesql", "sp_prepare", "sp_prepexec", "sp_cursoropen",
		"sp_cursorprepare", "sp_cursorprepexec", "sp_MSforeachtable", "sp_MSforeachdb",
	} {
		spellings := []string{name, strings.ToUpper(name), strings.ToLower(name)}
		for i := 0; i < len(name); i++ {
			for _, r := range lowersToASCII[strings.ToLower(name)[i]] {
				spellings = append(spellings, name[:i]+string(r)+name[i+1:])
			}
		}
		for _, spelling := range spellings {
			for _, sql := range []string{"EXEC [" + spelling + "] 1", "EXEC dbo." + spelling + " 1", spelling + " 1"} {
				assert.ErrorIs(t, dbCore.GuardExternalSchemaSQL(sql, tsql), dbCore.ErrExternalSchema, "%q must be refused", sql)
			}
		}
	}

	// A keyword spelt outside ASCII is not the keyword on the side that lets a statement
	// through: a read has to begin with SELECT or WITH itself, …
	for _, sql := range []string{
		"ſelect 1", "ｓｅｌｅｃｔ 1", "ＳＥＬＥＣＴ 1", "sélect 1", "SELÉCT 1", "ｗｉｔｈ c AS (SELECT 1) SELECT 1",
	} {
		for _, l := range everyLexicon {
			assert.ErrorIs(t, dbCore.GuardReadOnlySQL(sql, l.lex), dbCore.ErrReadOnly, "%s: %q must be refused", l.name, sql)
			assert.ErrorIs(t, dbCore.GuardReadOnlyShape(sql, l.lex), dbCore.ErrReadOnly, "%s: %q must be refused", l.name, sql)
		}
	}
	// … and SQL Server's exemptions still need their own words: TABLE for a #temp table, and
	// COLUMN for a clause of an ALTER TABLE. In ASCII the same statements pass.
	for _, sql := range []string{
		"DROP ＴＡＢＬＥ #scratch",
		"CREATE ｔａｂｌｅ #scratch (Id int)",
		"ALTER TABLE #scratch DROP ＣＯＬＵＭＮ Note",
	} {
		assert.ErrorIs(t, dbCore.GuardExternalSchemaSQL(sql, tsql), dbCore.ErrExternalSchema, "%q must be refused", sql)
	}
	for _, sql := range []string{"DROP TABLE #scratch", "CREATE TABLE #scratch (Id int)", "ALTER TABLE #scratch DROP COLUMN Note"} {
		assert.NoError(t, dbCore.GuardExternalSchemaSQL(sql, tsql), "%q must pass", sql)
	}
}
