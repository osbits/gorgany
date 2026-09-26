package core

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// SQLLexicon says how an engine's SQL splits into tokens, as far as the guards below need to
// know it: which quotes delimit identifiers, which delimit literals, and what is a comment.
//
// The guards look for keywords, and a keyword inside a string literal, a quoted identifier or
// a comment is not one. Telling them apart is where a guard gets fooled: 'it\'s' is one
// literal on MySQL and a literal followed by s' on SQL Server, and [Update] is an identifier
// on SQL Server but a keyword between brackets on Postgres. So the guards take the engine's
// rules as an argument instead of guessing them. Every engine gets -- and /* */ comments and
// '…' and "…", in which a doubled quote stands for one; each field adds a construct the
// engine has, and a field left false means the engine does not have it.
type SQLLexicon struct {
	// BracketIdentifiers: [name] is an identifier, with ]] for a ] inside it (SQL Server).
	// It also switches on the guards' SQL Server rules, since SQL Server is the engine whose
	// statements need no ";" between them, that changes a schema through stored procedures
	// such as sp_rename, that runs SQL text through EXEC (…) and sp_executesql, and that hands
	// it to a linked server through OPENQUERY: GuardReadOnlySQL refuses the words that begin
	// its statements and its lock-taking table hints, GuardReadOnlyShape reads every word as
	// GuardReadOnlySQL does, and GuardExternalSchemaSQL refuses its schema-changing words and
	// procedures, its dynamic SQL and its linked-server functions, anywhere.
	BracketIdentifiers bool

	// BacktickIdentifiers: `name` is an identifier, with `` for a ` inside it (MySQL).
	BacktickIdentifiers bool

	// DollarQuotes: $tag$…$tag$, and $$…$$, is a literal whose body is taken verbatim
	// (Postgres).
	DollarQuotes bool

	// HashComments stands for MySQL's comment rules as a whole: # opens a comment that runs
	// to the end of the line; -- opens one only when a space or control character follows it,
	// so 1--1 is arithmetic; and /*! … */ is no comment at all, since MySQL runs the text in
	// it, so the guards read that text as SQL. It also switches on the guards' MySQL rules,
	// which let two statements through GuardExternalSchemaSQL that mean something else on
	// other engines: SELECT … INTO @variable, which assigns user variables rather than
	// creating a table, and DO, which evaluates expressions rather than running a code block
	// as Postgres's DO does.
	HashComments bool

	// NestedBlockComments: a /* inside a comment opens another, so a comment ends at the */
	// that balances its opener, not at the first one (Postgres, SQL Server).
	NestedBlockComments bool

	// BackslashEscapes: a backslash escapes the character after it in '…' and "…", so
	// 'it\'s' is one literal (MySQL, unless the server runs with NO_BACKSLASH_ESCAPES).
	BackslashEscapes bool

	// EStrings: E'…' is a literal in which a backslash escapes the character after it,
	// whatever the server's other settings (Postgres).
	EStrings bool
}

// The lexicons of the engines gorgany ships. Nothing in gorgany modifies them, and nothing
// else should: they are variables only because Go has no constant structs.
var (
	// LexiconTSQL is SQL Server's: [bracketed] identifiers and nesting comments. "…" is an
	// identifier, or a literal under SET QUOTED_IDENTIFIER OFF, and the guards read it the
	// same way either way. A backslash is never an escape on SQL Server.
	LexiconTSQL = SQLLexicon{BracketIdentifiers: true, NestedBlockComments: true}

	// LexiconPostgres is Postgres's: dollar quotes, E'…' literals and nesting comments. '…'
	// takes no backslash escapes, as with standard_conforming_strings on, the default since
	// Postgres 9.1; the guards also read the text as the setting's other value would (see
	// GuardReadOnlySQL).
	LexiconPostgres = SQLLexicon{DollarQuotes: true, NestedBlockComments: true, EStrings: true}

	// LexiconMySQL is MySQL's under the server's default sql_mode: `backtick` identifiers,
	// MySQL's comment rules, and backslash escapes in '…' and "…". Under NO_BACKSLASH_ESCAPES
	// or ANSI_QUOTES a literal can end where this lexicon has it go on, so the guards also
	// read the text as those modes would (see GuardReadOnlySQL): either mode makes them refuse
	// more, never less.
	LexiconMySQL = SQLLexicon{BacktickIdentifiers: true, HashComments: true, BackslashEscapes: true}
)

// GuardReadOnlySQL reports whether sql, written by hand, may run on a read-only datasource,
// returning an error that wraps ErrReadOnly when it may not.
//
// It is the check for SQL no gorgany dialect rendered — an app's db.Raw or db.Exec, a
// migration's statement — so it assumes nothing about how the text was built and looks
// everywhere. The statement has to begin with SELECT or WITH, once any leading "(" and ";"
// are skipped; it may not be followed by another statement; and none of the words that
// write, lock, change the session or reach outside the database may appear in it, at any
// depth: INSERT, UPDATE, DELETE, MERGE, UPSERT, INTO, EXEC, EXECUTE, CALL, CREATE, ALTER,
// DROP, TRUNCATE, RENAME, GRANT, REVOKE, DENY, BACKUP, RESTORE, DBCC, BULK, OPENROWSET,
// OPENDATASOURCE, OPENQUERY, WRITETEXT, UPDATETEXT, SHUTDOWN, KILL, RECONFIGURE, USE, SET,
// DECLARE, LOCK, COPY, VACUUM, LOAD, HANDLER and CHECKPOINT; nor the sequence NEXT VALUE FOR,
// which draws from a sequence (FETCH NEXT … ROWS ONLY does not); nor a locking clause, FOR
// UPDATE, FOR NO KEY UPDATE, FOR SHARE, FOR KEY SHARE or LOCK IN SHARE MODE, which locks the
// rows it reads and needs write rights to do it. Under SQL Server's rules
// (lex.BracketIdentifiers) the reserved words that begin a statement and have no place in a
// query are refused as well: BEGIN, COMMIT, ROLLBACK, SAVE, IF, WHILE, BREAK, CONTINUE, GOTO,
// RETURN, WAITFOR, PRINT, RAISERROR, OPEN, CLOSE, DEALLOCATE, READTEXT, SETUSER and REVERT.
// Looking everywhere is what catches a data-modifying CTE (WITH gone AS (DELETE …) SELECT …),
// SELECT … INTO, SELECT … FOR UPDATE, and SQL Server running two statements that no ";"
// separates (SELECT 1 DELETE FROM t): SQL Server needs no ";" before the next statement, so
// on its rules a statement that follows another is found by its first word. A second read
// there (SELECT 1 SELECT 2) has no word to find it by, and passes; it writes nothing. The
// price of looking everywhere is that a column named like one of those words has to be
// quoted in raw SQL; dialect-rendered SQL on Postgres and MySQL gets GuardReadOnlyShape
// instead, which does not pay it.
//
// SQL Server spells its locking clauses as table hints, and under its rules those are refused
// wherever they stand too — in WITH (…), in the older form without WITH, FROM t (UPDLOCK), and
// in OPTION (TABLE HINT (…)) alike: UPDLOCK, XLOCK, TABLOCK and TABLOCKX, which take a lock a
// read does not otherwise take, and HOLDLOCK, its synonym SERIALIZABLE, and REPEATABLEREAD,
// which hold a read's locks until its transaction ends. A CTE's WITH is no hint, since what is
// refused is the hint's own word. A bracketed or quoted spelling, WITH ([UPDLOCK]), is refused
// too, as an item of a WITH (…) or TABLE HINT (…) list, though SQL Server documents its hints
// as keywords and not as names; anywhere else [updlock] is the name it looks like, since
// t ([updlock]) reads no differently from a function call on a column of that name. NOLOCK,
// READUNCOMMITTED and READPAST, which lock less than a read otherwise would, pass, and so do
// ROWLOCK and PAGLOCK, which only choose how finely a read locks what it reads.
//
// The words count only as words: in a literal, a quoted identifier or a comment they are
// text, and lex says which is which. Text the lexicon cannot finish reading — a literal, an
// identifier or a comment still open at the end — is refused, since an engine that reads it
// differently could find a statement the guard did not. An engine whose backslashes depend
// on a server setting (lex.BackslashEscapes or lex.EStrings) is read both ways whenever the
// text holds a backslash, and whatever either reading refuses is refused, so the setting
// cannot hide a statement. In the second reading a literal left open is not itself refused —
// 'it\'s' never ends on a server that takes no backslash escapes — but the text before it is
// still checked.
//
// This is a safety net, not a sandbox: a SELECT can still call a function that writes, and
// only a read-only database principal is a guarantee. The error never quotes the SQL, which
// may carry values an app interpolated into it; it names only what it refused, in words of
// its own.
func GuardReadOnlySQL(sql string, lex SQLLexicon) error {
	if refused := guardTokens(sql, lex, readOnlyRefusal); refused != "" {
		return readOnlyError(refused)
	}
	return nil
}

// GuardReadOnlyShape reports whether sql, rendered by a gorgany dialect, may run on a
// read-only datasource, returning an error that wraps ErrReadOnly when it may not.
//
// A dialect renders a query's structure itself and binds its values, so what could make a
// rendered statement write is its structure, and that is all this checks: it begins with
// SELECT or WITH, once any leading "(" and ";" are skipped; no ";" is followed by another
// statement; INTO, a locking clause (FOR UPDATE, FOR NO KEY UPDATE, FOR SHARE, FOR KEY SHARE,
// LOCK IN SHARE MODE) and NEXT VALUE FOR appear nowhere in it; and each common table
// expression — the parenthesised body after AS, AS MATERIALIZED or AS NOT MATERIALIZED — and
// the statement the WITH introduces begin with SELECT, WITH or VALUES. Unlike
// GuardReadOnlySQL it does not look for keywords anywhere else, so a column named lock, copy
// or load, which the Postgres and MySQL dialects emit unquoted, does not make a builder query
// unrunnable. What a dialect could otherwise render that writes is the dialect's to refuse
// before it renders it.
//
// Under SQL Server's rules (lex.BracketIdentifiers) it is GuardReadOnlySQL. SQL Server needs
// no ";" between statements, so a structure check cannot tell where a statement ends: SELECT
// id FROM t DELETE FROM t has a SELECT's shape up to its last word. Dialect-rendered SQL is
// not only structure either — a builder carries an app's raw fragments (a Select field, a
// RawCondition, an OrderByRaw) into it verbatim. And the reason the shape check spares
// words does not hold there: SQL Server's dialect brackets every identifier, and most of the
// words GuardReadOnlySQL refuses are reserved on SQL Server anyway.
//
// The reading of lex, the refusal of unfinished text, the second reading of backslashes and
// the error are GuardReadOnlySQL's.
func GuardReadOnlyShape(sql string, lex SQLLexicon) error {
	if refused := guardTokens(sql, lex, readOnlyShapeRefusal); refused != "" {
		return readOnlyError(refused)
	}
	return nil
}

// GuardExternalSchemaSQL reports whether sql may run on a datasource whose schema is owned
// outside gorgany, returning an error that wraps ErrExternalSchema when it would change that
// schema.
//
// Rows may be read and written there, so unlike the read-only guards this lets every query
// and every INSERT, UPDATE, DELETE and MERGE through, and refuses only what changes a schema.
// Each statement, the text between one ";" and the next, is refused when it begins with
// CREATE, ALTER, DROP, TRUNCATE, GRANT, REVOKE, DENY, COMMENT or RENAME, or when it is a
// SELECT … INTO, which creates the table it names. So is a SET of a setting that switches the
// schema's own rules off for the session: MySQL's foreign_key_checks, and Postgres's
// session_replication_role, which stops foreign keys and triggers firing. Both last as long
// as the pooled connection they were set on, so the rows written on it afterwards, by whoever
// is handed that connection, skip the owner's constraints; gorm's MySQL Migrator().DropTable
// sets foreign_key_checks off before its DROP, and on to restore it only after.
//
// Postgres's other statements that change what an object is, who owns it or how it is
// labelled are refused with them: IMPORT FOREIGN SCHEMA, which creates foreign tables (as
// MySQL's IMPORT TABLE creates tables), REASSIGN OWNED and SECURITY LABEL. So are the ones
// that rebuild what the owner manages — REFRESH MATERIALIZED VIEW, CLUSTER, REINDEX and VACUUM
// FULL, which write a view's rows, a table's storage or an index anew: work that is the
// owner's to schedule, and that, but for the CONCURRENTLY forms, locks out every read and
// write of the object while it runs. A plain VACUUM and ANALYZE pass: they reclaim space in
// place and refresh the planner's statistics, change no definition and no row, and take no
// lock that stops a read or a write.
//
// An EXPLAIN, and MySQL's DESCRIBE and DESC with it, is no free pass: EXPLAIN ANALYZE runs the
// statement it explains, and on Postgres EXPLAIN ANALYZE CREATE TABLE t AS SELECT … creates t.
// So the statement it explains is checked again as if it stood alone, once the options are set
// aside — Postgres's (…) list, ANALYZE and VERBOSE, and MySQL's FORMAT = name and INTO
// @variable. EXPLAIN SELECT … passes, as the SELECT would.
//
// Dynamic SQL is refused as well: the statement it runs is a value the server builds, which
// the guard cannot read, and a value built at run time can be DDL. On Postgres that is a DO
// block, and on MySQL a PREPARE … FROM, whose text, a literal or a @variable, a later EXECUTE
// runs. Both are refused on every engine: a statement that begins with DO, and one that begins
// with PREPARE unless it is Postgres's PREPARE name AS statement, whose statement is in the
// text the guard reads. MySQL's EXECUTE and DEALLOCATE PREPARE pass, since they run and drop
// only what a PREPARE prepared, and so, under MySQL's rules (lex.HashComments), does MySQL's
// DO, which evaluates expressions and runs no statement. The refusal says to send the
// statement itself, which the guard can read.
//
// Under SQL Server's rules (lex.BracketIdentifiers) more is refused, because that is how SQL
// Server spells a schema change, how gorm's SQL Server migrator issues its renames and its
// column comments, and how SQL Server runs SQL text. One is a call, with or without EXEC, of
// a stored procedure that renames objects or changes their types, rules, defaults, owners or
// extended properties — sp_rename, sp_addextendedproperty, sp_updateextendedproperty,
// sp_dropextendedproperty, sp_addtype, sp_droptype, sp_bindrule, sp_unbindrule,
// sp_bindefault, sp_unbindefault and sp_changeobjectowner — however its name is bracketed or
// qualified. Another is SQL Server's dynamic SQL, wherever an EXEC stands: EXEC or EXECUTE
// followed by "(", as in EXEC ('…'), EXEC (N'…' + @name) and EXEC (@sql); an EXEC of a
// procedure the guard cannot name, because a variable holds its name or a literal spells it;
// and a call, with or without EXEC and named as the procedures above are, of a system
// procedure that runs the SQL text it is handed — sp_executesql, sp_prepare, sp_prepexec,
// sp_cursoropen, sp_cursorprepare, sp_cursorprepexec, sp_MSforeachtable and sp_MSforeachdb.
// Another is OPENQUERY, OPENROWSET and OPENDATASOURCE, at any depth, through each of which
// another server, which may be this one, runs SQL the guard cannot read: OPENQUERY the query
// text it hands a linked server, OPENROWSET the query text it hands an OLE DB provider, and
// OPENDATASOURCE whatever is called through the provider it names, as in
// EXEC OPENDATASOURCE(…).reports.sys.sp_executesql N'…'. OPENROWSET(BULK …) reads a file
// rather than a query, and is refused with them all the same. Their refusal says what it
// cannot check and to reach that data another way: a four-part name lets an app query a
// linked server's tables in a statement the guard reads. It is reported only when nothing
// else in the same statement is refused, so SELECT … FROM OPENQUERY(…) DROP TABLE t names the
// DROP, and an operator who replaces the function is not met by a second refusal.
// Another is CREATE, ALTER, DROP, TRUNCATE, GRANT, REVOKE or DENY anywhere outside
// parentheses, since SQL Server needs no ";" before the next statement: IF … DROP TABLE t is
// a DROP, and so is SELECT 1 DROP TABLE t. (An ALTER COLUMN, DROP COLUMN or DROP CONSTRAINT
// inside an ALTER TABLE is part of that statement, not a new one.) The last is ENABLE TRIGGER
// and DISABLE TRIGGER, anywhere outside parentheses, which switch the owner's triggers on and
// off. An EXEC of any other procedure by name, EXEC dbo.usp_archive 1, passes: what a
// procedure does is server code, as what a function does is, and the principal's rights are
// what stand between it and the schema.
//
// A procedure's name is compared as a width-, accent- and case-insensitive collation would
// compare it, since the server looks the name up under the database's collation, and under
// such a collation [ｓｐ＿ｒｅｎａｍｅ] and [sp_rénamé] are sp_rename: the guard folds the name
// to its compatibility decomposition, without combining marks or format characters, in lower
// case (see foldName). (SQL Server's regular identifiers take no combining mark, so an accent
// written as one needs a bracketed or quoted name, which the guard reads whole.) The folding
// only ever refuses. The keywords the guards look for, and the SELECT or WITH that
// GuardReadOnlySQL lets a statement begin with, are matched in ASCII, since that is how the
// server's parser reads them whatever the collation: ſelect is a name to it, and reading it as
// SELECT would let a statement through, not stop one.
//
// A temporary table belongs to the session that creates it, not to the schema's owner, so
// SQL Server's #temp tables are exempt: CREATE TABLE #t, ALTER TABLE #t, TRUNCATE TABLE #t,
// DROP TABLE [IF EXISTS] #t — a list only when every table in it is one, so DROP TABLE
// [dbo].[Real], #t is refused — CREATE [UNIQUE] [CLUSTERED | NONCLUSTERED] INDEX i ON #t, and
// SELECT … INTO #t. A name counts as temporary only when it is a single #-prefixed part,
// bracketed or not. Postgres's and MySQL's temporary tables carry no marker in their name,
// so CREATE TEMPORARY TABLE is refused there: the guard refuses more, never less. MySQL's
// SELECT … INTO @variable creates no table at all — it assigns user variables — and passes
// under MySQL's rules when every name after INTO is a @variable; SELECT … INTO @x is refused
// on the other engines, which give it no such meaning. MySQL's INTO OUTFILE and INTO DUMPFILE
// stay refused: each writes a file on the database server's disk, which is neither a row nor
// the app's to write.
//
// Like the read-only guards it reads lex's literals, identifiers and comments, refuses text
// it cannot finish reading, never quotes the SQL in its error, and is a safety net: DDL that
// server code runs — a procedure, called with CALL or EXEC, a function or a trigger — is out
// of its sight, and so is a setting changed through a function, such as Postgres's
// set_config. So, on SQL Server, is a spelling that a collation equates with a refused
// procedure's name by some rule of its own beyond width, accents and case. Only a principal
// without DDL rights is a guarantee.
func GuardExternalSchemaSQL(sql string, lex SQLLexicon) error {
	refused := guardTokens(sql, lex, externalSchemaRefusal)
	switch {
	case refused == "":
		return nil
	case isDynamicSQL(refused):
		return fmt.Errorf("%w: %s statement refused; dynamic SQL cannot be checked, so send the statement itself (a safety net — use a principal without DDL rights for a guarantee)", ErrExternalSchema, refused)
	case sqlServerLinkedServerFunctions[refused]:
		return fmt.Errorf("%w: %s statement refused; the SQL it hands to a linked server or an OLE DB provider, or the file it reads, cannot be checked, so reach that data another way, such as a linked server's four-part name (a safety net — use a principal without DDL rights for a guarantee)", ErrExternalSchema, refused)
	}
	return fmt.Errorf("%w: %s statement refused; rows may be read and written on this datasource, but its schema is changed only by its owner (a safety net — use a principal without DDL rights for a guarantee)", ErrExternalSchema, refused)
}

func readOnlyError(refused string) error {
	return fmt.Errorf("%w: %s statement refused; only SELECT or WITH … SELECT may run on this datasource (a safety net — use a read-only principal for a guarantee)", ErrReadOnly, refused)
}

// guardTokens runs refusal over sql's tokens and returns what it refused, or "" when it let
// the statement through. See GuardReadOnlySQL for why an engine whose backslashes depend on
// a setting is read twice.
func guardTokens(sql string, lex SQLLexicon, refusal func([]sqlToken, SQLLexicon) string) string {
	tokens, unterminated := scanSQL(sql, lex, lex.BackslashEscapes)
	if unterminated != "" {
		return "malformed (unterminated " + unterminated + ")"
	}
	if refused := refusal(tokens, lex); refused != "" {
		return refused
	}
	if (lex.BackslashEscapes || lex.EStrings) && strings.IndexByte(sql, '\\') >= 0 {
		other, _ := scanSQL(sql, lex, !lex.BackslashEscapes)
		return refusal(other, lex)
	}
	return ""
}

// ---------------------------------------------------------------------- what is refused

// selectInto names a SELECT … INTO in a refusal.
const selectInto = "SELECT … INTO"

func wordSet(words ...string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[word] = true
	}
	return set
}

// readOnlyWords are the words GuardReadOnlySQL refuses wherever they stand.
var readOnlyWords = wordSet(
	"INSERT", "UPDATE", "DELETE", "MERGE", "UPSERT", "INTO", "EXEC", "EXECUTE", "CALL",
	"CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME", "GRANT", "REVOKE", "DENY",
	"BACKUP", "RESTORE", "DBCC", "BULK", "OPENROWSET", "OPENDATASOURCE", "OPENQUERY",
	"WRITETEXT", "UPDATETEXT", "SHUTDOWN", "KILL", "RECONFIGURE", "USE", "SET", "DECLARE",
	"LOCK", "COPY", "VACUUM", "LOAD", "HANDLER", "CHECKPOINT",
)

// tsqlStatementWords are the words GuardReadOnlySQL also refuses wherever they stand under
// SQL Server's rules: reserved words there, each of which begins a statement and none of
// which has a place inside a query. SQL Server needs no ";" before a statement, so its first
// word is what gives it away. (END and ELSE begin statements too, but a CASE uses them, and
// FETCH ends an OFFSET; the statements they end or begin are found by their other words.)
var tsqlStatementWords = wordSet(
	"BEGIN", "COMMIT", "ROLLBACK", "SAVE", "IF", "WHILE", "BREAK", "CONTINUE", "GOTO",
	"RETURN", "WAITFOR", "PRINT", "RAISERROR", "OPEN", "CLOSE", "DEALLOCATE", "READTEXT",
	"SETUSER", "REVERT",
)

// tsqlLockHints are the table hints GuardReadOnlySQL refuses wherever they stand under SQL
// Server's rules, as the locking clauses they are there (see GuardReadOnlySQL).
var tsqlLockHints = wordSet("UPDLOCK", "XLOCK", "TABLOCK", "TABLOCKX", "HOLDLOCK", "SERIALIZABLE", "REPEATABLEREAD")

// statementWords are the words a refusal may quote when a statement begins with one. A
// refusal names what it refused in the guard's own words, and a statement's first word is
// only one of them when it is a keyword this list knows: anything else — a stored
// procedure called without EXEC, a name — is the SQL's own text, and is named "non-SELECT".
var statementWords = func() map[string]bool {
	words := wordSet(
		"SELECT", "WITH", "VALUES", "TABLE", "SHOW", "EXPLAIN", "DESCRIBE", "DESC", "ANALYZE",
		"ANALYSE", "BEGIN", "START", "COMMIT", "ROLLBACK", "SAVE", "SAVEPOINT", "RELEASE",
		"PREPARE", "DEALLOCATE", "DO", "LISTEN", "NOTIFY", "UNLISTEN", "REFRESH", "REINDEX",
		"CLUSTER", "COMMENT", "DISCARD", "RESET", "REPLACE", "OPTIMIZE", "REPAIR", "FLUSH",
		"PURGE", "INSTALL", "UNINSTALL", "IF", "WHILE", "PRINT", "RAISERROR", "THROW",
		"WAITFOR", "GOTO", "RETURN", "OPEN", "CLOSE", "FETCH", "IMPORT", "REASSIGN",
		"SECURITY", "ENABLE", "DISABLE", "SEND", "RECEIVE", "GET", "MOVE", "SIGNAL",
		"RESIGNAL", "XA", "BINLOG", "CHANGE", "HELP", "RESTART", "CLONE", "END", "BREAK",
		"CONTINUE", "READTEXT", "REVERT", "SETUSER", "ELSE",
	)
	for word := range readOnlyWords {
		words[word] = true
	}
	return words
}()

// schemaWords begin a statement GuardExternalSchemaSQL refuses on every engine.
// sqlServerSchemaWords are the ones SQL Server reserves, so an unquoted one is a keyword
// wherever it stands. The others are Postgres's and MySQL's statements, which a column SQL
// Server lets an app call comment or cluster unquoted would otherwise be mistaken for:
// COMMENT and RENAME; IMPORT, REASSIGN and SECURITY, which begin Postgres's IMPORT FOREIGN
// SCHEMA, REASSIGN OWNED and SECURITY LABEL, and MySQL's IMPORT TABLE; REFRESH, CLUSTER
// and REINDEX, which rebuild a materialized view, a table or an index; and MySQL's OPTIMIZE
// TABLE and REPAIR TABLE, which rebuild a table the way VACUUM FULL does.
var (
	schemaWords = wordSet(
		"CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "DENY", "COMMENT", "RENAME",
		"IMPORT", "REASSIGN", "SECURITY", "REFRESH", "CLUSTER", "REINDEX", "OPTIMIZE", "REPAIR",
	)
	sqlServerSchemaWords = wordSet("CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "DENY")
)

// schemaPhrases are the words that follow the first of a statement schemaWords begins when
// the first alone would name it oddly in a refusal: SECURITY statement refused says less
// than SECURITY LABEL statement refused.
var schemaPhrases = map[string][]string{
	"IMPORT":   {"FOREIGN", "SCHEMA"},
	"REASSIGN": {"OWNED"},
	"SECURITY": {"LABEL"},
	"REFRESH":  {"MATERIALIZED", "VIEW"},
	"OPTIMIZE": {"TABLE"},
	"REPAIR":   {"TABLE"},
}

// schemaStatementName is how a refusal names the statement a schema word begins: the word,
// and the rest of its phrase when the statement goes on with it.
func schemaStatementName(statement []sqlToken) string {
	name := statement[0].upper
	phrase := schemaPhrases[name]
	for i, word := range phrase {
		if i+1 >= len(statement) || !statement[i+1].isWord(word) {
			return name
		}
	}
	for _, word := range phrase {
		name += " " + word
	}
	return name
}

// sqlServerSchemaProcedures are the system procedures through which SQL Server renames
// objects and changes their types, rules, defaults, owners and extended properties, in the
// lower-case ASCII that foldName leaves as it is.
var sqlServerSchemaProcedures = wordSet(
	"sp_rename", "sp_addextendedproperty", "sp_updateextendedproperty", "sp_dropextendedproperty",
	"sp_addtype", "sp_droptype", "sp_bindrule", "sp_unbindrule", "sp_bindefault",
	"sp_unbindefault", "sp_changeobjectowner",
)

// sqlServerDynamicProcedures are the system procedures that run, or prepare to run, the SQL
// text they are handed, in the lower-case ASCII that foldName leaves as it is.
// sp_MSforeachtable and sp_MSforeachdb run theirs once per table or database, with the name
// put in for a ?.
var sqlServerDynamicProcedures = wordSet(
	"sp_executesql", "sp_prepare", "sp_prepexec", "sp_cursoropen", "sp_cursorprepare",
	"sp_cursorprepexec", "sp_msforeachtable", "sp_msforeachdb",
)

// The dynamic SQL GuardExternalSchemaSQL refuses, as its refusals name it; a procedure in
// sqlServerDynamicProcedures is named by its own name.
const (
	execParenthesised = "EXEC (…)"       // EXEC ('…'), EXEC (@sql)
	execVariable      = "EXEC @variable" // a procedure whose name a variable holds
	execLiteral       = "EXEC '…'"       // a procedure whose name a literal spells
	prepareFrom       = "PREPARE … FROM" // MySQL's
	doBlock           = "DO"             // Postgres's
)

// sqlServerLinkedServerFunctions are the rowset functions through which SQL Server has a linked
// server run SQL (see GuardExternalSchemaSQL), as the words that name them. They are reserved
// words, so an unquoted one is the function wherever it stands; [openquery] is a name.
var sqlServerLinkedServerFunctions = wordSet("OPENQUERY", "OPENROWSET", "OPENDATASOURCE")

// isDynamicSQL reports whether refused, a GuardExternalSchemaSQL refusal, is of dynamic SQL,
// whose error says to send the statement itself rather than that the schema is the owner's.
func isDynamicSQL(refused string) bool {
	switch refused {
	case execParenthesised, execVariable, execLiteral, prepareFrom, doBlock:
		return true
	}
	return sqlServerDynamicProcedures[refused]
}

// statementName is how a refusal names the statement token begins.
func statementName(token sqlToken) string {
	if token.upper == "INTO" {
		return selectInto
	}
	if token.kind == sqlWord && statementWords[token.upper] {
		return token.upper
	}
	return "non-SELECT"
}

// secondStatementName is how a refusal names a statement that follows another. A second
// read is no more welcome than a second write, and "SELECT statement refused" would read as
// if the SELECT were the problem.
func secondStatementName(token sqlToken) string {
	if token.upper != "SELECT" && token.upper != "WITH" && token.kind == sqlWord && statementWords[token.upper] {
		return token.upper
	}
	return "second"
}

// skipLeading returns the index of the first token that is neither "(" nor ";".
func skipLeading(tokens []sqlToken) int {
	i := 0
	for i < len(tokens) && (tokens[i].isPunct("(") || tokens[i].isPunct(";")) {
		i++
	}
	return i
}

// readOnlyRefusal is GuardReadOnlySQL's check.
func readOnlyRefusal(tokens []sqlToken, lex SQLLexicon) string {
	start := skipLeading(tokens)
	if start == len(tokens) {
		return "" // nothing but punctuation and comments: nothing runs
	}
	if first := tokens[start]; !first.isWord("SELECT") && !first.isWord("WITH") {
		return statementName(first)
	}
	ended := false
	for i := start + 1; i < len(tokens); i++ {
		token := tokens[i]
		switch {
		case token.isPunct(";"):
			ended = true
		case ended:
			return secondStatementName(token)
		case token.upper == "INTO":
			return selectInto
		case lockingClause(tokens, i) != "":
			// Ahead of the words, so FOR UPDATE is named as the lock it is.
			return lockingClause(tokens, i)
		case lex.BracketIdentifiers && tsqlLockHints[token.upper]:
			// Named in the form SQL Server's documentation gives, whichever form it took.
			return "SELECT … WITH (" + token.upper + ")"
		case lex.BracketIdentifiers && token.kind == sqlQuoted && tsqlLockHints[asciiUpper(token.text)] && inHintList(tokens, i):
			return "SELECT … WITH (" + asciiUpper(token.text) + ")"
		case readOnlyWords[token.upper]:
			return token.upper
		case lex.BracketIdentifiers && tsqlStatementWords[token.upper]:
			return token.upper
		case isNextValueFor(tokens, i):
			return nextValueFor
		}
	}
	return ""
}

// inHintList reports whether tokens[i] is an item of a table-hint list, WITH (…) or OPTION's
// TABLE HINT (…): the first token after the list's "(" or after a "," in it. It is what lets
// the bracketed spelling of a lock hint, WITH ([UPDLOCK]), be refused while [updlock] stays
// a name everywhere else.
func inHintList(tokens []sqlToken, i int) bool {
	if i == 0 || !tokens[i-1].isPunct("(") && !tokens[i-1].isPunct(",") {
		return false
	}
	for open := i - 1; open > 0; open-- {
		if tokens[open].isPunct("(") && tokens[open].depth == tokens[i].depth-1 {
			return tokens[open-1].isWord("WITH") || tokens[open-1].isWord("HINT")
		}
	}
	return false
}

// nextValueFor names a NEXT VALUE FOR in a refusal.
const nextValueFor = "NEXT VALUE FOR"

// isNextValueFor reports whether tokens[i] begins NEXT VALUE FOR, which draws from a
// sequence. FETCH NEXT 5 ROWS ONLY does not.
func isNextValueFor(tokens []sqlToken, i int) bool {
	return tokens[i].isWord("NEXT") && i+2 < len(tokens) && tokens[i+1].isWord("VALUE") && tokens[i+2].isWord("FOR")
}

// lockingClause names the locking clause that begins at tokens[i] — FOR UPDATE, FOR NO KEY
// UPDATE, FOR SHARE, FOR KEY SHARE (Postgres, MySQL 8) or LOCK IN SHARE MODE (MySQL) — or
// returns "". Each locks the rows the SELECT reads; Postgres and MySQL want write rights on
// the table for it, and Postgres refuses it in a read-only transaction.
func lockingClause(tokens []sqlToken, i int) string {
	word := func(j int, upper string) bool { return j < len(tokens) && tokens[j].isWord(upper) }
	switch {
	case word(i, "FOR") && word(i+1, "UPDATE"):
		return "SELECT … FOR UPDATE"
	case word(i, "FOR") && word(i+1, "NO") && word(i+2, "KEY") && word(i+3, "UPDATE"):
		return "SELECT … FOR NO KEY UPDATE"
	case word(i, "FOR") && word(i+1, "SHARE"):
		return "SELECT … FOR SHARE"
	case word(i, "FOR") && word(i+1, "KEY") && word(i+2, "SHARE"):
		return "SELECT … FOR KEY SHARE"
	case word(i, "LOCK") && word(i+1, "IN") && word(i+2, "SHARE") && word(i+3, "MODE"):
		return "SELECT … LOCK IN SHARE MODE"
	}
	return ""
}

// readOnlyShapeRefusal is GuardReadOnlyShape's check.
func readOnlyShapeRefusal(tokens []sqlToken, lex SQLLexicon) string {
	if lex.BracketIdentifiers {
		return readOnlyRefusal(tokens, lex) // see GuardReadOnlyShape
	}
	start := skipLeading(tokens)
	if start == len(tokens) {
		return ""
	}
	if first := tokens[start]; !first.isWord("SELECT") && !first.isWord("WITH") {
		return statementName(first)
	}
	end := len(tokens)
	for i := start; i < len(tokens) && end == len(tokens); i++ {
		if !tokens[i].isPunct(";") {
			continue
		}
		for _, next := range tokens[i+1:] {
			if !next.isPunct(";") {
				return secondStatementName(next)
			}
		}
		end = i
	}
	statement := tokens[:end]
	// INTO is refused at any depth, not only the top: no read has one anywhere (INTO is
	// reserved on every engine, so a column of that name is quoted), and a nested one is
	// either invalid or an INSERT … INTO in a CTE, which is a write. A locking clause and
	// NEXT VALUE FOR are refused at any depth too: a subquery can lock, and draw from a
	// sequence, as well as the query around it.
	for i := start; i < len(statement); i++ {
		switch {
		case statement[i].upper == "INTO":
			return selectInto
		case lockingClause(statement, i) != "":
			return lockingClause(statement, i)
		case isNextValueFor(statement, i):
			return nextValueFor
		}
	}
	if refused := queryRefusal(statement, start); refused != "" {
		return refused
	}
	// A WITH that opens a subquery introduces CTEs of its own.
	for i := start + 1; i < len(statement); i++ {
		if statement[i].isWord("WITH") && statement[i-1].isPunct("(") {
			if refused := withRefusal(statement, i+1); refused != "" {
				return refused
			}
		}
	}
	return ""
}

// queryRefusal checks that the query starting at tokens[i] begins with SELECT, VALUES or a
// WITH whose CTEs and statement do, once any "(" are skipped. Running out of tokens is not a
// refusal: a statement that stops there cannot run.
func queryRefusal(tokens []sqlToken, i int) string {
	for i < len(tokens) && tokens[i].isPunct("(") {
		i++
	}
	if i >= len(tokens) {
		return ""
	}
	switch token := tokens[i]; {
	case token.isWord("SELECT"), token.isWord("VALUES"):
		return ""
	case token.isWord("WITH"):
		return withRefusal(tokens, i+1)
	default:
		return statementName(token)
	}
}

// unrecognisedWith names a WITH the shape check cannot follow. A dialect renders only the
// shapes the check knows, so one it does not is refused rather than trusted.
const unrecognisedWith = "non-standard WITH"

// withRefusal checks the CTE list that starts at tokens[i], just after a WITH, and then the
// statement the list introduces.
func withRefusal(tokens []sqlToken, i int) string {
	if i < len(tokens) && tokens[i].isWord("RECURSIVE") {
		i++
	}
	for i < len(tokens) {
		name := tokens[i]
		if name.kind != sqlWord && name.kind != sqlQuoted {
			return unrecognisedWith
		}
		i++
		if name.isWord("XMLNAMESPACES") {
			// SQL Server's WITH XMLNAMESPACES ('uri' AS ns) declares prefixes, not a query.
			if i >= len(tokens) || !tokens[i].isPunct("(") {
				return unrecognisedWith
			}
			if i = closingParen(tokens, i); i < 0 {
				return ""
			}
			i++
		} else {
			if i < len(tokens) && tokens[i].isPunct("(") { // the CTE's column list
				if i = closingParen(tokens, i); i < 0 {
					return ""
				}
				i++
			}
			if i >= len(tokens) {
				return ""
			}
			if !tokens[i].isWord("AS") {
				return unrecognisedWith
			}
			i++
			if i < len(tokens) && tokens[i].isWord("NOT") {
				i++
			}
			if i < len(tokens) && tokens[i].isWord("MATERIALIZED") {
				i++
			}
			if i >= len(tokens) {
				return ""
			}
			if !tokens[i].isPunct("(") {
				return unrecognisedWith
			}
			if refused := queryRefusal(tokens, i+1); refused != "" {
				return refused
			}
			if i = closingParen(tokens, i); i < 0 {
				return ""
			}
			i++
		}
		if i < len(tokens) && tokens[i].isPunct(",") {
			i++
			continue
		}
		return queryRefusal(tokens, i)
	}
	return ""
}

// closingParen returns the index of the ")" that closes the "(" at tokens[open], or -1.
func closingParen(tokens []sqlToken, open int) int {
	for i := open + 1; i < len(tokens); i++ {
		if tokens[i].isPunct(")") && tokens[i].depth == tokens[open].depth {
			return i
		}
	}
	return -1
}

// externalSchemaRefusal is GuardExternalSchemaSQL's check, one ";"-separated statement at a
// time.
func externalSchemaRefusal(tokens []sqlToken, lex SQLLexicon) string {
	for start := 0; start < len(tokens); {
		end := start
		for end < len(tokens) && !tokens[end].isPunct(";") {
			end++
		}
		if refused := schemaStatementRefusal(tokens[start:end], lex); refused != "" {
			return refused
		}
		start = end + 1
	}
	return ""
}

func schemaStatementRefusal(statement []sqlToken, lex SQLLexicon) string {
	if len(statement) == 0 {
		return ""
	}
	if first := statement[0]; first.kind == sqlWord && schemaWords[first.upper] && !onlyTemporaryObjects(statement, 0) {
		return schemaStatementName(statement)
	}
	if statement[0].isWord("VACUUM") && isVacuumFull(statement) {
		return vacuumFull
	}
	if statement[0].isWord("SET") {
		if setting := constraintSetting(statement); setting != "" {
			return "SET " + setting
		}
	}
	if statement[0].isWord("PREPARE") && !isPrepareAs(statement) {
		return prepareFrom
	}
	if statement[0].isWord("DO") && !lex.HashComments { // MySQL's DO runs no code block
		return doBlock
	}
	// linkedServer is the first linked-server function in the statement, which is refused
	// only if nothing else in it is (see GuardExternalSchemaSQL).
	linkedServer := ""
	if lex.BracketIdentifiers {
		// SQL Server runs a procedure named as a batch's first statement without EXEC.
		if procedure := refusedProcedure(statement, 0); procedure != "" {
			return procedure
		}
		for i, token := range statement {
			switch {
			case token.isWord("EXEC") || token.isWord("EXECUTE"):
				if refused := execRefusal(statement, i+1); refused != "" {
					return refused
				}
			case token.kind == sqlWord && sqlServerLinkedServerFunctions[token.upper]:
				// At any depth: FROM OPENQUERY(…) is where it stands in a query.
				if linkedServer == "" {
					linkedServer = token.upper
				}
			case i > 0 && token.depth == 0 && sqlServerSchemaWords[token.upper] &&
				!isTableClause(statement, i) && !onlyTemporaryObjects(statement, i):
				return token.upper
			case token.depth == 0 && (token.isWord("ENABLE") || token.isWord("DISABLE")) &&
				i+1 < len(statement) && statement[i+1].isWord("TRIGGER"):
				return token.upper + " TRIGGER"
			}
		}
	}
	// SELECT … INTO creates its target. The INTO of an INSERT, a MERGE or an OUTPUT clause
	// names a table that already exists, so an INTO counts only when the last statement
	// keyword at its own depth is SELECT — at its own depth, since the SELECT of a subquery
	// in an UPDATE's SET says nothing about the OUTPUT … INTO after it. OUTPUT is a keyword
	// only after a DML verb; after a SELECT it is a column. Under MySQL's rules an INTO of
	// @variables assigns them and creates nothing.
	lastVerb := map[int]string{}
	for i, token := range statement {
		switch token.upper {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "UPSERT":
			lastVerb[token.depth] = token.upper
		case "OUTPUT":
			if verb := lastVerb[token.depth]; verb == "INSERT" || verb == "UPDATE" || verb == "DELETE" || verb == "MERGE" {
				lastVerb[token.depth] = "OUTPUT"
			}
		case "INTO":
			if lastVerb[token.depth] == "SELECT" && !isTemporaryObject(statement, i+1) &&
				!(lex.HashComments && userVariablesEnd(statement, i+1) > 0) {
				return selectInto
			}
		}
	}
	// EXPLAIN ANALYZE runs the statement it explains, so that statement is checked again as if
	// it stood alone, which is what gives its first word the rules above.
	if explained := explainedStatement(statement, lex); explained > 0 {
		if refused := schemaStatementRefusal(statement[explained:], lex); refused != "" {
			return refused
		}
	}
	return linkedServer
}

// vacuumFull names a VACUUM FULL in a refusal.
const vacuumFull = "VACUUM FULL"

// isVacuumFull reports whether the VACUUM that begins statement is a VACUUM FULL, written in
// the older form's words (VACUUM FULL FREEZE VERBOSE ANALYZE) or in its option list (VACUUM
// (FULL, ANALYZE)). A FULL anywhere in the list counts, FULL false included: the guard
// refuses more, never less.
func isVacuumFull(statement []sqlToken) bool {
	if len(statement) > 1 && statement[1].isPunct("(") {
		end := closingParen(statement, 1)
		if end < 0 {
			end = len(statement)
		}
		for _, option := range statement[2:end] {
			if option.isWord("FULL") {
				return true
			}
		}
		return false
	}
	for _, word := range statement[1:] {
		switch {
		case word.isWord("FULL"):
			return true
		case word.isWord("FREEZE"), word.isWord("VERBOSE"), word.isWord("ANALYZE"), word.isWord("ANALYSE"):
		default:
			return false
		}
	}
	return false
}

// explainedStatement returns the index of the statement an EXPLAIN, or MySQL's DESCRIBE or
// DESC, explains, once its options are set aside — Postgres's (…) list, ANALYZE, VERBOSE,
// MySQL's FORMAT = name, EXTENDED, PARTITIONS and, under MySQL's rules, INTO @variable — or 0
// when statement is no EXPLAIN.
func explainedStatement(statement []sqlToken, lex SQLLexicon) int {
	if first := statement[0]; !first.isWord("EXPLAIN") && !first.isWord("DESCRIBE") && !first.isWord("DESC") {
		return 0
	}
	i := 1
	if i < len(statement) && statement[i].isPunct("(") {
		if i = closingParen(statement, i); i < 0 {
			return 0
		}
		i++
	}
	for i < len(statement) {
		token := statement[i]
		switch {
		case token.isWord("ANALYZE"), token.isWord("ANALYSE"), token.isWord("VERBOSE"),
			token.isWord("EXTENDED"), token.isWord("PARTITIONS"):
			i++
		case token.isWord("FORMAT") && i+2 < len(statement) && statement[i+1].isPunct("="):
			i += 3
		case token.isWord("INTO") && lex.HashComments && userVariablesEnd(statement, i+1) > 0:
			i = userVariablesEnd(statement, i+1)
		default:
			return i
		}
	}
	return i
}

// constraintSettings are the settings, in upper case, that switch a schema's own rules off
// for the session (see GuardExternalSchemaSQL).
var constraintSettings = wordSet("FOREIGN_KEY_CHECKS", "SESSION_REPLICATION_ROLE")

// constraintSetting returns the constraint setting the SET statement names, in upper case, or
// "". The name counts however it is spelt: bare, quoted, as @@name or as @@SESSION.name, and
// anywhere in the statement, since a SET can assign several settings at once.
func constraintSetting(statement []sqlToken) string {
	for _, token := range statement[1:] {
		if token.kind != sqlWord && token.kind != sqlQuoted {
			continue
		}
		if name := asciiUpper(strings.TrimLeft(token.text, "@")); constraintSettings[name] {
			return name
		}
	}
	return ""
}

// isTableClause reports whether the word at statement[i] continues an ALTER TABLE rather than
// starting a statement: ALTER COLUMN, DROP COLUMN, DROP CONSTRAINT, DROP PERIOD and DROP IF
// EXISTS are clauses, and no statement begins with any of them.
func isTableClause(statement []sqlToken, i int) bool {
	if i+1 >= len(statement) {
		return false
	}
	switch statement[i+1].upper {
	case "COLUMN", "CONSTRAINT", "PERIOD", "IF":
		return true
	}
	return false
}

// onlyTemporaryObjects reports whether the statement that begins with the verb at
// statement[i] touches #temp tables only (see GuardExternalSchemaSQL).
func onlyTemporaryObjects(statement []sqlToken, i int) bool {
	at := func(j int, word string) bool { return j < len(statement) && statement[j].isWord(word) }
	j := i + 1
	switch statement[i].upper {
	case "CREATE":
		if at(j, "TABLE") {
			return isTemporaryObject(statement, j+1)
		}
		if at(j, "UNIQUE") {
			j++
		}
		if at(j, "CLUSTERED") || at(j, "NONCLUSTERED") {
			j++
		}
		if !at(j, "INDEX") {
			return false
		}
		index, next := objectName(statement, j+1)
		return len(index) > 0 && at(next, "ON") && isTemporaryObject(statement, next+1)
	case "ALTER", "TRUNCATE":
		return at(j, "TABLE") && isTemporaryObject(statement, j+1)
	case "DROP":
		if !at(j, "TABLE") {
			return false
		}
		j++
		if at(j, "IF") && at(j+1, "EXISTS") {
			j += 2
		}
		for {
			name, next := objectName(statement, j)
			if !isTemporaryName(name) {
				return false
			}
			if next >= len(statement) || !statement[next].isPunct(",") {
				return true
			}
			j = next + 1
		}
	}
	return false
}

func isTemporaryObject(tokens []sqlToken, i int) bool {
	name, _ := objectName(tokens, i)
	return isTemporaryName(name)
}

// isTemporaryName reports whether a dotted name is a SQL Server temporary table: a single
// part that starts with #. tempdb..#t is refused rather than parsed; nothing needs it.
func isTemporaryName(parts []string) bool {
	return len(parts) == 1 && strings.HasPrefix(parts[0], "#")
}

// refusedProcedure returns the system procedure named at tokens[i] when it is one that
// changes a schema or runs SQL text, or "". Only the name's last part counts, so
// [sys].[sp_rename], dbo.sp_rename and master..sp_rename are all sp_rename, and it is compared
// folded, so SP_RENAME, sp_ｒｅｎａｍｅ and [sp_rénamé] are too (see GuardExternalSchemaSQL).
// What it returns is the list's spelling, never the SQL's.
func refusedProcedure(tokens []sqlToken, i int) string {
	parts, _ := objectName(tokens, i)
	if len(parts) == 0 {
		return ""
	}
	if name := foldName(parts[len(parts)-1]); sqlServerSchemaProcedures[name] || sqlServerDynamicProcedures[name] {
		return name
	}
	return ""
}

// foldName returns name as a width-, accent- and case-insensitive collation compares it, as
// near as the guard can tell: in lower case, in its compatibility decomposition (NFKD) — which
// takes a fullwidth ｓ, a long ſ and a ligature ﬁ to s, s and fi, and splits an accented
// letter into its letter and combining marks — and without those marks, nor the format
// characters, such as a soft hyphen or a zero-width space, that a collation gives no weight.
//
// It is for names the server looks up under the database's collation, and only for refusing
// them. It starts from strings.ToLower, which is what the guard compared before it folded,
// and leaves ASCII as it is, so every name that compared equal to a refused one still does:
// folding adds refusals and takes none away. Keywords are not folded (see sqlToken.upper).
func foldName(name string) string {
	var folded strings.Builder
	for _, r := range norm.NFKD.String(strings.ToLower(name)) {
		if !unicode.In(r, unicode.M, unicode.Cf) {
			folded.WriteRune(unicode.ToLower(r))
		}
	}
	return folded.String()
}

// execRefusal returns what GuardExternalSchemaSQL refuses of the EXEC whose target — what
// follows EXEC or EXECUTE — starts at tokens[i], or "". A target the guard cannot name is
// dynamic SQL: a "(" opens the SQL text itself, and a variable or a literal holds the name of
// the procedure to run, which may be sp_executesql. N'…' reads as the word N and a literal, so
// EXEC N 'x', a call of a procedure named N, is refused with it: the guard refuses more, never
// less.
func execRefusal(tokens []sqlToken, i int) string {
	if i+1 < len(tokens) && isVariable(tokens[i]) && tokens[i+1].isPunct("=") {
		i += 2 // EXEC @status = procedure …
	}
	if i >= len(tokens) {
		return ""
	}
	switch target := tokens[i]; {
	case target.isPunct("("):
		return execParenthesised
	case isVariable(target):
		return execVariable
	case target.kind == sqlLiteral, target.isWord("N") && i+1 < len(tokens) && tokens[i+1].kind == sqlLiteral:
		return execLiteral
	}
	return refusedProcedure(tokens, i)
}

// isVariable reports whether token is a word that begins with @: a variable, @name, or a
// @@name, which SQL Server calls a system function and MySQL a system variable.
func isVariable(token sqlToken) bool {
	return token.kind == sqlWord && strings.HasPrefix(token.text, "@")
}

// userVariablesEnd reports whether what follows an INTO, from tokens[i] on, is a list of
// MySQL user variables, @name[, @name …], which SELECT … INTO assigns, by returning the index
// of the token after the list, or -1 when it is not one. A @@name, a quoted name after @, or
// anything else in the list — a table, OUTFILE, DUMPFILE — makes it not one.
func userVariablesEnd(tokens []sqlToken, i int) int {
	for {
		if i >= len(tokens) || !isVariable(tokens[i]) || strings.HasPrefix(tokens[i].text, "@@") {
			return -1
		}
		if i++; i >= len(tokens) || !tokens[i].isPunct(",") {
			return i
		}
		i++
	}
}

// isPrepareAs reports whether the statement, which begins with PREPARE, is Postgres's
// PREPARE name [(type, …)] AS statement, whose statement is in the text the guard reads.
// Anything else is taken for MySQL's PREPARE name FROM text, whose statement is a value.
func isPrepareAs(statement []sqlToken) bool {
	name, i := objectName(statement, 1)
	if len(name) != 1 {
		return false
	}
	if i < len(statement) && statement[i].isPunct("(") {
		if i = closingParen(statement, i); i < 0 {
			return false
		}
		i++
	}
	return i < len(statement) && statement[i].isWord("AS")
}

// objectName reads the dotted name that starts at tokens[i] — words and quoted identifiers
// joined by "." — and returns its parts, unquoted, with an empty part for each extra "." (as
// in db..table), and the index of the token after it.
func objectName(tokens []sqlToken, i int) ([]string, int) {
	var parts []string
	for i < len(tokens) && (tokens[i].kind == sqlWord || tokens[i].kind == sqlQuoted) {
		parts = append(parts, tokens[i].text)
		i++
		dots := 0
		for i < len(tokens) && tokens[i].isPunct(".") {
			dots++
			i++
		}
		if dots == 0 {
			break
		}
		for ; dots > 1; dots-- {
			parts = append(parts, "")
		}
	}
	return parts, i
}

// ------------------------------------------------------------------------ the tokenizer

type sqlTokenKind uint8

const (
	sqlWord    sqlTokenKind = iota + 1 // a keyword or an unquoted name, @variable and #temp included
	sqlQuoted                          // a quoted identifier: "name", [name] or `name`
	sqlLiteral                         // a string or numeric literal
	sqlPunct                           // anything else, one character at a time
)

type sqlToken struct {
	kind sqlTokenKind
	// text is a word as written, a quoted identifier's name with its quoting undone, or the
	// punctuation itself. A literal's text is not kept: nothing reads it, and it is where an
	// app's values are.
	text string
	// upper is a word's upper case, and "" for anything else, including a word with a
	// character outside ASCII. Keywords are ASCII on every engine, and Unicode case folding
	// would make ſelect (long s) a SELECT that the server reads as a name. The names of
	// procedures, which the server looks up under a collation, are folded instead, and only
	// to refuse them (see foldName).
	upper string
	// depth is how many parentheses are open around the token. A "(" and the ")" that closes
	// it share the depth outside them.
	depth int
}

func (t sqlToken) isWord(upper string) bool { return t.kind == sqlWord && t.upper == upper }
func (t sqlToken) isPunct(p string) bool    { return t.kind == sqlPunct && t.text == p }

type sqlScanner struct {
	src string
	lex SQLLexicon
	// backslashes is whether a backslash escapes in '…' in this reading of the text (see
	// GuardReadOnlySQL), and in "…" when the lexicon has backslash escapes at all.
	backslashes bool
	pos         int
	depth       int
	// execComments counts MySQL /*! … */ comments opened and not yet closed.
	execComments int
	tokens       []sqlToken
}

// scanSQL splits sql into tokens under lex. When the text ends inside a comment, a literal
// or a quoted identifier it also names what is left open, and returns the tokens before it.
func scanSQL(sql string, lex SQLLexicon, backslashes bool) ([]sqlToken, string) {
	s := &sqlScanner{src: sql, lex: lex, backslashes: backslashes}
	for s.pos < len(s.src) {
		if unterminated := s.scan(); unterminated != "" {
			return s.tokens, unterminated
		}
	}
	if s.execComments > 0 {
		return s.tokens, "comment"
	}
	return s.tokens, ""
}

// peek returns the byte offset bytes ahead, or 0 past the end.
func (s *sqlScanner) peek(offset int) byte {
	if i := s.pos + offset; i < len(s.src) {
		return s.src[i]
	}
	return 0
}

func (s *sqlScanner) emit(kind sqlTokenKind, text string) {
	s.tokens = append(s.tokens, sqlToken{kind: kind, text: text, depth: s.depth})
}

// scan consumes one token, comment or run of white space at s.pos, and returns what is left
// unterminated when that is where the text ends.
func (s *sqlScanner) scan() string {
	c := s.src[s.pos]
	switch {
	case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
		s.pos++
	case c >= utf8.RuneSelf:
		r, size := utf8.DecodeRuneInString(s.src[s.pos:])
		switch {
		case unicode.IsSpace(r):
			s.pos += size
		case isWordRune(r):
			return s.scanWord()
		default:
			s.emit(sqlPunct, s.src[s.pos:s.pos+size])
			s.pos += size
		}
	case c == '-' && s.peek(1) == '-' && (!s.lex.HashComments || s.peek(2) <= ' '):
		s.skipLine()
	case c == '#' && s.lex.HashComments:
		s.skipLine()
	case c == '/' && s.peek(1) == '*':
		if s.lex.HashComments && (s.peek(2) == '!' || s.peek(2) == 'M' && s.peek(3) == '!') {
			s.openExecComment()
		} else if !s.skipBlockComment() {
			return "comment"
		}
	case c == '*' && s.peek(1) == '/' && s.execComments > 0:
		s.execComments--
		s.pos += 2
	case c == '\'':
		if _, ok := s.quoted('\'', s.backslashes); !ok {
			return "string literal"
		}
		s.emit(sqlLiteral, "")
	case c == '"':
		name, ok := s.quoted('"', s.backslashes && s.lex.BackslashEscapes)
		if !ok {
			return "quoted identifier"
		}
		s.emit(sqlQuoted, name)
	case c == '`' && s.lex.BacktickIdentifiers:
		name, ok := s.quoted('`', false)
		if !ok {
			return "quoted identifier"
		}
		s.emit(sqlQuoted, name)
	case c == '[' && s.lex.BracketIdentifiers:
		name, ok := s.bracketed()
		if !ok {
			return "bracketed identifier"
		}
		s.emit(sqlQuoted, name)
	case c == '$' && s.lex.DollarQuotes:
		delimiter := s.dollarDelimiter()
		if delimiter == "" { // a $1 placeholder, or an operator
			s.emit(sqlPunct, "$")
			s.pos++
			break
		}
		body := s.pos + len(delimiter)
		end := strings.Index(s.src[body:], delimiter)
		if end < 0 {
			return "dollar-quoted string"
		}
		s.pos = body + end + len(delimiter)
		s.emit(sqlLiteral, "")
	case isDigit(c) || c == '.' && isDigit(s.peek(1)):
		s.scanNumber()
	case isLetter(c) || c == '_' || (c == '@' || c == '#') && s.isWordByte(s.peek(1)):
		return s.scanWord()
	case c == '(':
		s.emit(sqlPunct, "(")
		s.depth++
		s.pos++
	case c == ')':
		if s.depth > 0 {
			s.depth--
		}
		s.emit(sqlPunct, ")")
		s.pos++
	default:
		s.emit(sqlPunct, s.src[s.pos:s.pos+1])
		s.pos++
	}
	return ""
}

// skipLine skips a line comment. It ends at the first character any engine ends a line at,
// so a comment is never read as longer than the server reads it.
func (s *sqlScanner) skipLine() {
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case '\n', '\r', '\f', '\v':
			return
		}
		s.pos++
	}
}

// skipBlockComment skips the /* … */ at s.pos and reports whether it ends.
func (s *sqlScanner) skipBlockComment() bool {
	depth := 1
	for i := s.pos + 2; i+1 < len(s.src); i++ {
		switch {
		case s.src[i] == '*' && s.src[i+1] == '/':
			depth--
			i++
			if depth == 0 {
				s.pos = i + 1
				return true
			}
		case s.lex.NestedBlockComments && s.src[i] == '/' && s.src[i+1] == '*':
			depth++
			i++
		}
	}
	return false
}

// openExecComment skips the opener of a MySQL /*! … */ or MariaDB /*M! … */ comment and the
// version number that may follow it. The text up to the matching */ is read as SQL, since
// that is how MySQL treats it (a version newer than the server's makes MySQL skip it, so
// reading it anyway refuses more, never less).
func (s *sqlScanner) openExecComment() {
	if s.peek(2) == 'M' {
		s.pos += 4
	} else {
		s.pos += 3
	}
	for s.pos < len(s.src) && isDigit(s.src[s.pos]) {
		s.pos++
	}
	s.execComments++
}

// quoted reads the literal or identifier quoted with q that starts at s.pos, doubling q to
// escape it and, when backslashes is set, taking a backslash to escape the character after
// it. It returns the text inside with the escapes undone, and whether the quote closes.
func (s *sqlScanner) quoted(q byte, backslashes bool) (string, bool) {
	var text strings.Builder
	for i := s.pos + 1; i < len(s.src); i++ {
		c := s.src[i]
		switch {
		case backslashes && c == '\\':
			i++
			if i < len(s.src) {
				text.WriteByte(s.src[i])
			}
		case c == q && i+1 < len(s.src) && s.src[i+1] == q:
			text.WriteByte(q)
			i++
		case c == q:
			s.pos = i + 1
			return text.String(), true
		default:
			text.WriteByte(c)
		}
	}
	return "", false
}

// bracketed reads the [identifier] that starts at s.pos, in which ]] stands for ].
func (s *sqlScanner) bracketed() (string, bool) {
	var name strings.Builder
	for i := s.pos + 1; i < len(s.src); i++ {
		switch {
		case s.src[i] == ']' && i+1 < len(s.src) && s.src[i+1] == ']':
			name.WriteByte(']')
			i++
		case s.src[i] == ']':
			s.pos = i + 1
			return name.String(), true
		default:
			name.WriteByte(s.src[i])
		}
	}
	return "", false
}

// dollarDelimiter returns the $tag$ or $$ that opens a dollar-quoted literal at s.pos, or ""
// when the $ opens none: a tag follows the rules of an unquoted name, without $ or a leading
// digit, so $1 is a placeholder.
func (s *sqlScanner) dollarDelimiter() string {
	i := s.pos + 1
	if i < len(s.src) && s.src[i] == '$' {
		return "$$"
	}
	if i >= len(s.src) || !(isLetter(s.src[i]) || s.src[i] == '_' || s.src[i] >= utf8.RuneSelf) {
		return ""
	}
	for i++; i < len(s.src); i++ {
		c := s.src[i]
		if c == '$' {
			return s.src[s.pos : i+1]
		}
		if !(isLetter(c) || isDigit(c) || c == '_' || c >= utf8.RuneSelf) {
			return ""
		}
	}
	return ""
}

// scanNumber reads a numeric literal: digits with an optional fraction and exponent, or a
// 0x hexadecimal. Letters after it start a new token, so 1DELETE reads as 1 and DELETE, which
// is how SQL Server reads it; a trailing "." belongs to the number, so 1.DELETE does too.
func (s *sqlScanner) scanNumber() {
	i := s.pos
	if s.src[i] == '0' && (s.peek(1) == 'x' || s.peek(1) == 'X') && isHexDigit(s.peek(2)) {
		for i += 2; i < len(s.src) && isHexDigit(s.src[i]); i++ {
		}
	} else {
		for i < len(s.src) && isDigit(s.src[i]) {
			i++
		}
		if i < len(s.src) && s.src[i] == '.' {
			for i++; i < len(s.src) && isDigit(s.src[i]); i++ {
			}
		}
		if i < len(s.src) && (s.src[i] == 'e' || s.src[i] == 'E') {
			j := i + 1
			if j < len(s.src) && (s.src[j] == '+' || s.src[j] == '-') {
				j++
			}
			if j < len(s.src) && isDigit(s.src[j]) {
				for i = j; i < len(s.src) && isDigit(s.src[i]); i++ {
				}
			}
		}
	}
	s.pos = i
	s.emit(sqlLiteral, "")
}

// scanWord reads the word at s.pos, or the E'…' literal it begins on an engine with EStrings.
func (s *sqlScanner) scanWord() string {
	start := s.pos
	for s.pos < len(s.src) {
		c := s.src[s.pos]
		if c < utf8.RuneSelf {
			if !s.isWordByte(c) {
				break
			}
			s.pos++
			continue
		}
		r, size := utf8.DecodeRuneInString(s.src[s.pos:])
		if !isWordRune(r) {
			break
		}
		s.pos += size
	}
	word := s.src[start:s.pos]
	if s.lex.EStrings && (word == "E" || word == "e") && s.pos < len(s.src) && s.src[s.pos] == '\'' {
		if _, ok := s.quoted('\'', true); !ok {
			return "string literal"
		}
		s.emit(sqlLiteral, "")
		return ""
	}
	s.tokens = append(s.tokens, sqlToken{kind: sqlWord, text: word, upper: asciiUpper(word), depth: s.depth})
	return ""
}

// isWordByte reports whether c continues a word: letters, digits, _, $, @ and — except
// where it opens a comment — #, the characters SQL Server, Postgres and MySQL allow in
// unquoted names and variables between them. Reading a word as longer than an engine does
// could hide a keyword in it, so a word ends at any other character.
func (s *sqlScanner) isWordByte(c byte) bool {
	return isLetter(c) || isDigit(c) || c == '_' || c == '$' || c == '@' || c == '#' && !s.lex.HashComments
}

// isWordRune reports whether a character outside ASCII continues a word: a letter or a
// digit. Anything else — a combining mark, a zero-width space — ends it.
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func isLetter(c byte) bool   { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func isDigit(c byte) bool    { return '0' <= c && c <= '9' }
func isHexDigit(c byte) bool { return isDigit(c) || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F' }

// asciiUpper returns word in upper case, or "" when it has a character outside ASCII.
func asciiUpper(word string) string {
	for i := 0; i < len(word); i++ {
		if word[i] >= utf8.RuneSelf {
			return ""
		}
	}
	return strings.ToUpper(word)
}
