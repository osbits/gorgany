package migration

import (
	"fmt"
	"time"

	"github.com/osbits/gorgany/app/core"
	"gorm.io/gorm"
)

// SessionsMigration creates the sessions table used by the database-backed session
// storage (auth.session.storage: database).
//
// It used to execute hand-written Postgres DDL, which made it invalid MySQL on two
// counts: `CREATE INDEX IF NOT EXISTS` (MySQL has no IF NOT EXISTS on CREATE
// INDEX) and `DROP TABLE ... CASCADE` (not MySQL syntax). DbProvider.Boot adds this
// migration unconditionally, so `db:migrate up` failed outright on MySQL before an
// app's own migrations ever ran.
//
// It is now expressed through GORM's Migrator, which renders the DDL per dialect,
// so it is portable by construction rather than by two hand-maintained strings.
// The Postgres driver's DropTable still emits CASCADE and the MySQL driver's does
// not, exactly as each engine requires.
type SessionsMigration struct{}

func NewSessionsMigration() *SessionsMigration {
	return &SessionsMigration{}
}

func (m *SessionsMigration) Name() string {
	return "create_sessions_table"
}

// Index names this migration manages.
const (
	SessionsExpiryIndex = "idx_sessions_expiry"
	SessionsUserIDIndex = "idx_sessions_user_id"
)

// sessionsSchema is the migration's own snapshot of the sessions table.
//
// It deliberately does not reuse auth.DbSessionEntity: a migration pins the shape
// the schema had when it was written, and the runtime model is free to drift
// afterwards. The columns match the DDL this migration replaced, except that the
// timestamp type is left to GORM — timestamptz on Postgres, datetime(3) on
// MySQL — rather than pinned to TIMESTAMP, which on MySQL cannot represent a date
// past 2038.
//
// The indexes are declared here so Migrator emits them as part of CreateTable, and
// so HasIndex/CreateIndex can resolve them by name on a table that already exists
// from an older install.
type sessionsSchema struct {
	ID           string    `gorm:"column:id;type:varchar(255);primaryKey"`
	UserID       string    `gorm:"column:user_id;type:varchar(255);index:idx_sessions_user_id"`
	Expiry       time.Time `gorm:"column:expiry;not null;index:idx_sessions_expiry"`
	CreatedAt    time.Time `gorm:"column:created_at;not null"`
	LastActivity time.Time `gorm:"column:last_activity;not null"`
	Attributes   *string   `gorm:"column:attributes;type:text"`
}

// TableName pins the table this migration manages.
func (sessionsSchema) TableName() string { return "sessions" }

// SessionsTableModel returns the schema snapshot this migration operates on, so
// callers and tests can address the same model the migration uses.
func SessionsTableModel() any { return &sessionsSchema{} }

// Up creates the sessions table and its two indexes.
//
// Every step is guarded, so running it against a database where the table already
// exists — for instance one migrated by the pre-v2 hand-written DDL — adds only
// what is missing, and is a no-op when nothing is.
func (m *SessionsMigration) Up() core.MigrationClosure {
	return func(db *gorm.DB) error {
		if db == nil {
			return fmt.Errorf("sessions migration: no database handle")
		}

		migrator := db.Migrator()
		model := &sessionsSchema{}

		if !migrator.HasTable(model) {
			// CreateTable emits the declared indexes along with the table.
			if err := migrator.CreateTable(model); err != nil {
				return fmt.Errorf("sessions migration: cannot create table: %w", err)
			}
			return nil
		}

		for _, index := range []string{SessionsExpiryIndex, SessionsUserIDIndex} {
			if migrator.HasIndex(model, index) {
				continue
			}
			if err := migrator.CreateIndex(model, index); err != nil {
				return fmt.Errorf("sessions migration: cannot create index %s: %w", index, err)
			}
		}

		return nil
	}
}

// Down drops the sessions table.
func (m *SessionsMigration) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		if db == nil {
			return fmt.Errorf("sessions migration: no database handle")
		}

		migrator := db.Migrator()
		model := &sessionsSchema{}

		if !migrator.HasTable(model) {
			return nil
		}

		if err := migrator.DropTable(model); err != nil {
			return fmt.Errorf("sessions migration: cannot drop table: %w", err)
		}
		return nil
	}
}
