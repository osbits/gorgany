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

		model := &sessionsVersionSchema{}

		exists, err := hasTable(db, model, sessionsTable)
		if err != nil {
			return fmt.Errorf("sessions version migration: cannot tell whether the sessions table exists: %w", err)
		}
		if !exists {
			return fmt.Errorf(
				"sessions version migration: the sessions table does not exist; run " +
					"create_sessions_table first")
		}

		present, err := hasColumn(db, model, sessionsTable, SessionsVersionColumn)
		if err != nil {
			return fmt.Errorf("sessions version migration: cannot tell whether the %s column exists: %w",
				SessionsVersionColumn, err)
		}
		if present {
			return nil
		}

		if err := db.Migrator().AddColumn(model, SessionsVersionColumn); err != nil {
			return fmt.Errorf("sessions version migration: cannot add the %s column: %w",
				SessionsVersionColumn, err)
		}

		return nil
	}
}

// sqlServerDropVersionDefaultSQL drops the DEFAULT constraint on sessions.version, if there
// is one. See Down.
//
// SQL Server names the constraint itself when a column is added with a default, as Up adds
// this one (DF__sessions__versi__ and a hash), so the name has to be looked up. It cannot be
// dropped with EXEC (N'ALTER TABLE … ' + QUOTENAME(@df)) either: EXEC's parenthesised form
// concatenates only string literals and variables, not a function call. So the statement is
// built in a variable, with QUOTENAME bracketing the looked-up name, and run through
// sp_executesql.
//
// It is one batch, sent through Exec with no arguments. SQL containing '@' makes gorm parse
// it for named arguments, and a name it holds no value for is written back unchanged, so @df
// and @sql reach the server exactly as they are written here. The table and column are
// literals: this migration manages that one column of that one table.
const sqlServerDropVersionDefaultSQL = `DECLARE @df sysname, @sql nvarchar(max);
SELECT @df = dc.name FROM sys.default_constraints AS dc
JOIN sys.columns AS c ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id
WHERE dc.parent_object_id = OBJECT_ID(N'sessions') AND c.name = N'version';
IF @df IS NOT NULL
BEGIN
    SET @sql = N'ALTER TABLE [sessions] DROP CONSTRAINT ' + QUOTENAME(@df);
    EXEC sp_executesql @sql;
END`

// Down drops the column again.
//
// Rolling back leaves the sessions themselves alone: without the column every write is
// unguarded, which is the pre-2.2 behaviour rather than a broken one, so a downgrade degrades
// concurrency safety instead of breaking authentication.
//
// On SQL Server the column's DEFAULT constraint is dropped first. gorm's DropColumn there is a
// plain ALTER TABLE … DROP COLUMN, which SQL Server refuses while a constraint depends on the
// column (Msg 5074, then Msg 4922), so without this the rollback could never succeed there.
// db:migrate runs Down in a transaction, and SQL Server's DDL is transactional, so a column
// drop that fails after the constraint drop takes the constraint drop back with it. The
// dynamic SQL runs only where the sessions migrations do, on a default gorgany owns; the guard
// that refuses dynamic SQL on an external_schema datasource never sees it.
func (m *SessionsVersionMigration) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		if db == nil {
			return fmt.Errorf("sessions version migration: no database handle")
		}

		model := &sessionsVersionSchema{}

		exists, err := hasTable(db, model, sessionsTable)
		if err != nil {
			return fmt.Errorf("sessions version migration: cannot tell whether the sessions table exists: %w", err)
		}
		if !exists {
			return nil
		}
		present, err := hasColumn(db, model, sessionsTable, SessionsVersionColumn)
		if err != nil {
			return fmt.Errorf("sessions version migration: cannot tell whether the %s column exists: %w",
				SessionsVersionColumn, err)
		}
		if !present {
			return nil
		}

		if dialectOf(db) == sqlServerDialect {
			if err := db.Exec(sqlServerDropVersionDefaultSQL).Error; err != nil {
				return fmt.Errorf("sessions version migration: cannot drop the default of the %s column: %w",
					SessionsVersionColumn, err)
			}
		}

		if err := db.Migrator().DropColumn(model, SessionsVersionColumn); err != nil {
			return fmt.Errorf("sessions version migration: cannot drop the %s column: %w",
				SessionsVersionColumn, err)
		}

		return nil
	}
}
