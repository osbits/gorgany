package migration

import (
	"fmt"

	"github.com/osbits/gorgany/v2/app/core"
	"gorm.io/gorm"
)

// SessionsVersionMigration adds the optimistic-concurrency counter to the sessions table.
//
// It is a second migration rather than an edit to create_sessions_table, and it has to be.
// Applied migrations are recorded by Name() and skipped when the name is already in the
// migrations table, so amending the first one changes nothing for any database that has
// already run it — which is every deployed install. SessionsMigration's own doc says the same
// thing from the other direction: a migration pins the shape the schema had when it was
// written, and the runtime model is free to move on.
//
// What the column is for: DbSessionEntity guards any write that changes the user id or the
// attribute bag on the version it read, so a replica writing from a copy another replica has
// already superseded matches no row and is told to reconcile instead of silently winning. See
// DbSessionEntity.Snapshot.
type SessionsVersionMigration struct{}

func NewSessionsVersionMigration() *SessionsVersionMigration {
	return &SessionsVersionMigration{}
}

func (m *SessionsVersionMigration) Name() string {
	return "add_sessions_version_column"
}

// SessionsVersionColumn is the column this migration manages.
const SessionsVersionColumn = "version"

// sessionsVersionSchema is this migration's own snapshot: the key it addresses rows by, and
// the one column it adds. Deliberately not sessionsSchema — that one is create_sessions_table's
// record of the table as it was then, and adding a column to it would rewrite history.
type sessionsVersionSchema struct {
	ID string `gorm:"column:id;type:varchar(255);primaryKey"`
	// NOT NULL DEFAULT 0 is what makes this cheap on a large table: Postgres 11+ and MySQL 8
	// both add a column with a non-volatile default as metadata only, with no rewrite and no
	// backfill. Existing rows read as version 0, and the first guarded write matches on that.
	Version int64 `gorm:"column:version;not null;default:0"`
}

func (sessionsVersionSchema) TableName() string { return "sessions" }

// SessionsVersionTableModel returns the schema snapshot this migration operates on, so tests
// can address the same model.
func SessionsVersionTableModel() any { return &sessionsVersionSchema{} }

// Up adds the version column when it is missing.
//
// Guarded both ways: an install whose sessions table does not exist yet gets the column from
// create_sessions_table's successor run, and one that already has the column is a no-op, so
// re-running is safe.
func (m *SessionsVersionMigration) Up() core.MigrationClosure {
	return func(db *gorm.DB) error {
		if db == nil {
			return fmt.Errorf("sessions version migration: no database handle")
		}

		migrator := db.Migrator()
		model := &sessionsVersionSchema{}

		if !migrator.HasTable(model) {
			return fmt.Errorf(
				"sessions version migration: the sessions table does not exist; run " +
					"create_sessions_table first")
		}

		if migrator.HasColumn(model, SessionsVersionColumn) {
			return nil
		}

		if err := migrator.AddColumn(model, SessionsVersionColumn); err != nil {
			return fmt.Errorf("sessions version migration: cannot add the %s column: %w",
				SessionsVersionColumn, err)
		}

		return nil
	}
}

// Down drops the column again.
//
// Rolling back leaves the sessions themselves alone: without the column every write is
// unguarded, which is the pre-2.2 behaviour rather than a broken one, so a downgrade degrades
// concurrency safety instead of breaking authentication.
func (m *SessionsVersionMigration) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		if db == nil {
			return fmt.Errorf("sessions version migration: no database handle")
		}

		migrator := db.Migrator()
		model := &sessionsVersionSchema{}

		if !migrator.HasTable(model) || !migrator.HasColumn(model, SessionsVersionColumn) {
			return nil
		}

		if err := migrator.DropColumn(model, SessionsVersionColumn); err != nil {
			return fmt.Errorf("sessions version migration: cannot drop the %s column: %w",
				SessionsVersionColumn, err)
		}

		return nil
	}
}
