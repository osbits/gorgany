// Package guard enforces a datasource's policy (see core.DataSourcePolicy) inside gorm, at two
// points every statement on a guarded handle passes.
//
// The first is a callback on each of gorm's Create, Update, Delete, Query, Row and Raw, which
// checks the SQL a statement was given before gorm's own callbacks run: an app's db.Raw and
// db.Exec, gorm's Migrator, and the SQL of gorgany's ORM and query builders wherever their
// executor sends it with db.Raw or db.Exec. The second is the connection pool, which checks
// each statement as it is sent: the SQL gorm builds from a model and from the fragments an app
// adds to it, which no callback sees, and anything sent on the handle's ConnPool directly,
// such as the Postgres and MySQL executors' ExecInsert, which goes there to read the
// generated key. An install guards the pool the handle was opened with, every transaction
// begun from it, and whatever pool a statement carries when it reaches the callbacks, such as
// the connection db.Connection pins.
//
// What it does not see is a statement sent on a pool it did not guard: the *sql.DB that
// db.DB() returns and a *sql.Conn taken from that; the pool of a handle derived from db before
// the install, when something sends on it directly rather than through gorm's methods; and
// the pool a plugin puts on a statement after the guard's callback has run — gorm's
// dbresolver, for one, switches a statement to a pool of its own there. Nor does it see what
// the server does on a statement's behalf: a function or procedure the statement calls, or a
// trigger it fires. Dynamic SQL, whose statement is a value the server builds — a Postgres DO
// block, MySQL's PREPARE … FROM, SQL Server's EXEC (…) and sp_executesql — is refused under
// either policy rather than trusted, since the guard cannot read what it would run.
//
// Gorgany is to refuse what it can see coming earlier, and say more precisely why: a command
// refusing an external-schema datasource before it runs any SQL, and a dialect refusing to
// render a write for a read-only one, in the changes that add them. None of those checks sees
// what an app, or gorm itself, sends to the handle directly. The guard does, before the
// driver sees it. So these are the last check, not the only one, and like the core guards
// they call they are a safety net: the database principal's own rights are the guarantee.
//
// Only gorgany's own engines can mark a statement as rendered by their dialect, which gives
// it the read-only shape check in the callback (see InstallReadOnly); the mark is internal to
// db/sql/gorm. The package imports gorm and core and no driver, so an engine of any kind can
// install it. The lexicon it is given says how that engine's SQL reads (see core.SQLLexicon).
package guard

import (
	"errors"
	"fmt"
	"strings"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/gorm/internal/rendered"
	"gorm.io/gorm"
)

// The callbacks' names. gorm keys callbacks by name within each of its processors, which is
// what lets an install tell that it already ran.
const (
	readOnlyCallback       = "gorgany:read_only"
	externalSchemaCallback = "gorgany:external_schema"
)

// InstallReadOnly makes db refuse writes, as the read-only half of a datasource's policy
// (read_only: true). It returns an error only when db cannot take callbacks.
//
// gorm's Create, Update and Delete, and everything built on them — Save, Updates,
// FirstOrCreate, association writes — are refused outright, since writing is all they do.
// SQL set with db.Raw or db.Exec is checked rather than refused: gorm sets it before it runs
// its callbacks, so a Query, Row or Raw callback that finds SQL already set is looking at text
// someone wrote or a dialect rendered. The callback checks that text with
// core.GuardReadOnlySQL, and with core.GuardReadOnlyShape when a gorgany engine marked it as
// its dialect's. Every statement is then checked again as it is sent, with
// core.GuardReadOnlyShape; that is the only check a query gorm builds from a model gets, since
// no callback sees its SQL. So what an app adds to one, in Select, Table, Where, Joins, Group,
// Having, Order, Clauses or a gorm.Expr, cannot add a second statement, an INTO, a locking
// clause such as FOR UPDATE, a NEXT VALUE FOR or a data-modifying CTE; on SQL Server's lexicon
// every word of it is checked (see core.GuardReadOnlyShape), so a table hint that locks, such
// as WITH (UPDLOCK), is refused there as FOR UPDATE is elsewhere. On Postgres and MySQL the
// words of such a fragment are not: a function it calls, which may write, runs.
//
// The SAVEPOINT (SAVE TRANSACTION on SQL Server), and its ROLLBACK TO, that gorm itself sends
// for a nested Transaction are let through on purpose; a read-only transaction takes
// savepoints like any other, and they write nothing. What the guard does not see at all is in
// the package documentation.
//
// Every refusal wraps core.ErrReadOnly, and a refused statement never reaches the database —
// not even as the BEGIN of gorm's default transaction, since the writes that open one are
// refused in their callbacks, and gorm's callbacks do nothing once a statement has an error.
// The callbacks run ahead of every callback the handle had when they were installed; gorm
// puts one registered later with Before("*") ahead of them, which is why the pool checks too.
// A refused Row() returns a *sql.Row whose Scan and Err report the refusal, as they would a
// database error. (Under gorm's PrepareStmt, a query gorm builds that is refused as it is sent
// gets the empty *sql.Row gorm returns for any statement it could not prepare; use Rows or
// Scan there.)
//
// Install on the handle gorm.Open returned, before deriving any from it, as gorgany's
// datasources do: a handle derived from it afterwards shares its guarded pool. Installing it
// again, on db or on any handle that shares its callbacks, adds nothing.
func InstallReadOnly(db *gorm.DB, lex dbCore.SQLLexicon) error {
	if err := installable(db); err != nil {
		return err
	}
	st, err := stateOf(db)
	if err != nil {
		return fmt.Errorf("guard: %w", err)
	}
	st.enable(readOnlyPolicy, lex)
	st.guardHandle(db)

	callbacks := db.Callback()
	reads := st.sending(guardReads(lex))
	return install(readOnlyCallback, []hook{
		{callbacks.Create().Get, callbacks.Create().Before("*").Register, st.sending(refuseWrites("create"))},
		{callbacks.Update().Get, callbacks.Update().Before("*").Register, st.sending(refuseWrites("update"))},
		{callbacks.Delete().Get, callbacks.Delete().Before("*").Register, st.sending(refuseWrites("delete"))},
		{callbacks.Query().Get, callbacks.Query().Before("*").Register, reads},
		{callbacks.Row().Get, callbacks.Row().Before("*").Register, forRow(reads)},
		{callbacks.Raw().Get, callbacks.Raw().Before("*").Register, reads},
	})
}

// InstallExternalSchema makes db refuse schema changes, as the external-schema half of a
// datasource's policy (external_schema: true). It returns an error only when db cannot take
// callbacks.
//
// Rows may still be read and written, so a Create, Update or Delete is checked like any other
// statement, not refused. Every statement is checked with core.GuardExternalSchemaSQL twice.
// The callbacks check the SQL a statement was given before they run, on every processor that
// can send it: Raw, which db.Exec and gorm's Migrator use for their DDL; Query and Row, since
// db.Raw(…).Scan and .Rows send their SQL that way and a SELECT … INTO creates a table
// whichever method sends it; and Create, Update and Delete, which send SQL set with db.Raw
// verbatim instead of building their own, so db.Raw("DROP TABLE t").Create(&row) is DDL too.
// The pool checks every statement as it is sent, which is what sees the SQL gorm builds: a
// fragment an app adds to a query or a write in Select, Table, Where, Joins, Group, Having,
// Order, Clauses or a gorm.Expr is refused when it adds a SELECT … INTO, a second statement
// that changes the schema, or, on SQL Server's lexicon, a DROP with no ";" before it.
//
// Every refusal wraps core.ErrExternalSchema, and a refused statement never reaches the
// database. One the callbacks refuse sends nothing at all. One the pool refuses may, when
// gorm opened its default transaction for it, have sent that transaction's BEGIN, which gorm
// then rolls back. A MySQL Migrator().DropTable, which switches foreign_key_checks off before
// its DROP and on only after, is refused at the first of those, so it leaves no connection in
// the pool with the owner's constraints off. The callbacks run ahead of every callback the
// handle had when they were installed; gorm puts one registered later with Before("*") ahead
// of them, which is why the pool checks too. A refused Row() returns a *sql.Row whose Scan and
// Err report the refusal, as InstallReadOnly's does, with the same exception. What the guard
// does not see is in the package documentation.
//
// Install on the handle gorm.Open returned, before deriving any from it, as gorgany's
// datasources do. Installing it again, on db or on any handle that shares its callbacks, adds
// nothing.
func InstallExternalSchema(db *gorm.DB, lex dbCore.SQLLexicon) error {
	if err := installable(db); err != nil {
		return err
	}
	st, err := stateOf(db)
	if err != nil {
		return fmt.Errorf("guard: %w", err)
	}
	st.enable(externalSchemaPolicy, lex)
	st.guardHandle(db)

	callbacks := db.Callback()
	schema := st.sending(guardSchema(lex))
	return install(externalSchemaCallback, []hook{
		{callbacks.Create().Get, callbacks.Create().Before("*").Register, schema},
		{callbacks.Update().Get, callbacks.Update().Before("*").Register, schema},
		{callbacks.Delete().Get, callbacks.Delete().Before("*").Register, schema},
		{callbacks.Query().Get, callbacks.Query().Before("*").Register, schema},
		{callbacks.Row().Get, callbacks.Row().Before("*").Register, forRow(schema)},
		{callbacks.Raw().Get, callbacks.Raw().Before("*").Register, schema},
	})
}

// hook is one callback to register on one of gorm's processors. gorm's processor type is
// unexported, so a hook holds the two of its methods an install needs instead.
type hook struct {
	installed func(name string) func(*gorm.DB)
	register  func(name string, fn func(*gorm.DB)) error
	handler   func(*gorm.DB)
}

// install registers each hook under name unless its processor already has a callback of
// that name. Before("*") puts it ahead of gorm's own callbacks, and of every other registered
// so far; one registered later with Before("*") goes ahead of it.
func install(name string, hooks []hook) error {
	for _, h := range hooks {
		if h.installed(name) != nil {
			continue
		}
		if err := h.register(name, h.handler); err != nil {
			return fmt.Errorf("guard: registering %s: %w", name, err)
		}
	}
	return nil
}

// installable reports why db cannot take callbacks, if it cannot. Only a handle gorm.Open
// built has them; a bare &gorm.DB{} does not.
func installable(db *gorm.DB) error {
	if db == nil || db.Config == nil || db.Callback() == nil {
		return errors.New("guard: this *gorm.DB has no callbacks to install into; install on a handle from gorm.Open")
	}
	return nil
}

// sending makes check guard the pool the statement is about to be sent on before it checks
// the statement's SQL, so that what gorm builds after it is checked as it is sent.
func (s *state) sending(check func(*gorm.DB)) func(*gorm.DB) {
	return func(db *gorm.DB) {
		s.guardStatement(db)
		check(db)
	}
}

// forRow adapts a check to gorm's Row processor. Row() returns the *sql.Row its callbacks
// leave in the statement's Dest, and a refused statement leaves none, so it would return nil,
// on which Scan panics. On a refusal forRow leaves a *sql.Row that reports it. Rows() returns
// the statement's error, and is left alone.
func forRow(check func(*gorm.DB)) func(*gorm.DB) {
	return func(db *gorm.DB) {
		failed := db.Error != nil
		check(db)
		if failed || db.Error == nil {
			return
		}
		if rows, _ := db.Get("rows"); rows != true {
			db.Statement.Dest = refusedRow(db.Error)
		}
	}
}

// refuseWrites refuses every statement of one of gorm's write processors.
func refuseWrites(op string) func(*gorm.DB) {
	refusal := fmt.Errorf("%w: gorm %s refused", dbCore.ErrReadOnly, op)
	return func(db *gorm.DB) {
		if db.Error == nil {
			db.AddError(refusal)
		}
	}
}

// guardReads checks the SQL a statement was given before the callbacks ran.
func guardReads(lex dbCore.SQLLexicon) func(*gorm.DB) {
	return func(db *gorm.DB) {
		sql := presetSQL(db)
		if sql == "" || isGormSavepoint(sql) {
			return
		}
		check := dbCore.GuardReadOnlySQL
		if rendered.Marked(db) {
			check = dbCore.GuardReadOnlyShape
		}
		if err := check(sql, lex); err != nil {
			db.AddError(err)
		}
	}
}

// guardSchema checks the SQL a statement was given before the callbacks ran.
func guardSchema(lex dbCore.SQLLexicon) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if sql := presetSQL(db); sql != "" {
			if err := dbCore.GuardExternalSchemaSQL(sql, lex); err != nil {
				db.AddError(err)
			}
		}
	}
}

// presetSQL returns the SQL db.Raw or db.Exec gave the statement, or "" when gorm is still to
// build it from a model — or when the statement has already failed. Nothing runs after an
// error, and adding a refusal to one would demote the first error, which gorm keeps as text
// once a second is joined to it, to where errors.Is no longer finds it.
func presetSQL(db *gorm.DB) string {
	if db.Error != nil || db.Statement == nil || db.Statement.SQL.Len() == 0 {
		return ""
	}
	return db.Statement.SQL.String()
}

// gormSavepoints are the statements gorm's dialects send for a nested Transaction — SAVEPOINT
// and ROLLBACK TO SAVEPOINT on Postgres and MySQL, SAVE TRANSACTION and ROLLBACK TRANSACTION
// on SQL Server — each followed by nothing but the savepoint's name.
var gormSavepoints = []string{"SAVEPOINT ", "ROLLBACK TO SAVEPOINT ", "SAVE TRANSACTION ", "ROLLBACK TRANSACTION "}

// isGormSavepoint reports whether sql is exactly one of gormSavepoints and a plain name, as
// gorm writes them. Anything longer is somebody else's SQL, and is checked.
func isGormSavepoint(sql string) bool {
	for _, prefix := range gormSavepoints {
		if name, ok := strings.CutPrefix(sql, prefix); ok {
			return isPlainName(name)
		}
	}
	return false
}

func isPlainName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		letter := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '_'
		if !letter && !(i > 0 && '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}
