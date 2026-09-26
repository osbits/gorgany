// Package rendered marks the statement a gorm handle is about to run as SQL a gorgany
// dialect rendered, for the read-only guard in db/sql/gorm/guard to check with
// core.GuardReadOnlyShape instead of core.GuardReadOnlySQL.
//
// The mark is internal to db/sql/gorm so that only gorgany's own engines can set it: marked
// text gets the lighter check whoever wrote it, so a mark an app could set, or one that
// spread to statements nobody marked, would downgrade the guard for SQL no dialect rendered.
// So the mark is stored under a key of an unexported type, which neither gorm's Set nor
// InstanceSet can spell, and the key holds the statement it marks: gorm copies a statement's
// settings into every statement it derives from it — for a Session, a Transaction, or the
// next call on a handle that has already begun one — and a copied mark names the statement
// it was copied from, not the one being run, so it marks nothing there.
package rendered

import "gorm.io/gorm"

// mark is the settings key of a marked statement.
type mark struct{ statement *gorm.Statement }

// Mark marks the statement db is about to run as rendered by a gorgany dialect, and returns
// the handle it runs on.
//
// gorm keeps settings on a statement, not on a connection, and a handle that has not begun a
// statement yet — gorm.Open's, or one from Session, WithContext or Begin — begins a fresh one
// here. So mark the handle the statement runs on, and run it on the handle Mark returns, as
// in Mark(db.WithContext(ctx)).Raw(sql, args...).Scan(dest): the mark never reaches db itself,
// nor any handle derived from the one Mark returns.
func Mark(db *gorm.DB) *gorm.DB {
	// Clauses with no clauses adds nothing; it is gorm's way to give the caller a statement
	// of its own, the one the next call on tx runs.
	tx := db.Clauses()
	tx.Statement.Settings.Store(mark{tx.Statement}, true)
	return tx
}

// Marked reports whether Mark marked the statement db is running.
func Marked(db *gorm.DB) bool {
	if db == nil || db.Statement == nil {
		return false
	}
	marked, _ := db.Statement.Settings.Load(mark{db.Statement})
	return marked == true
}
