package migration

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// add_sessions_version_column had no tests at all, so nothing showed that its Down could not
// succeed on SQL Server: Up adds the column with a default, SQL Server gives that default a
// constraint of its own naming, and a plain DROP COLUMN is refused while the constraint is
// there (Msg 5074). These run the migration's own closures on every engine, over a fake schema
// that answers the existence checks both directions start with (see schemaDB), and assert on
// what each server would have received.

func TestVersionMigrationIdentity(t *testing.T) {
	m := NewSessionsVersionMigration()

	assert.Equal(t, "add_sessions_version_column", m.Name())
	assert.Equal(t, "version", SessionsVersionColumn)
	assert.NotNil(t, m.Up())
	assert.NotNil(t, m.Down())

	named, ok := SessionsVersionTableModel().(interface{ TableName() string })
	require.True(t, ok)
	assert.Equal(t, "sessions", named.TableName())
}

// TestVersionUpAndDownRejectNilHandle keeps a nil *gorm.DB from becoming a nil dereference
// inside the Migrator.
func TestVersionUpAndDownRejectNilHandle(t *testing.T) {
	m := NewSessionsVersionMigration()

	require.Error(t, m.Up()(nil))
	require.Error(t, m.Down()(nil))
}

// TestVersionUpAddsTheColumnOnEveryEngine pins the one statement Up sends when the column is
// missing: NOT NULL with a constant default, which Postgres 11+ and MySQL 8 add without
// rewriting the table, and which leaves every existing row at version 0.
func TestVersionUpAddsTheColumnOnEveryEngine(t *testing.T) {
	tests := map[string]string{
		"postgres":  `ALTER TABLE "sessions" ADD "version" bigint NOT NULL DEFAULT 0`,
		"mysql":     "ALTER TABLE `sessions` ADD `version` bigint NOT NULL DEFAULT 0",
		"sqlserver": `ALTER TABLE "sessions" ADD "version" bigint NOT NULL DEFAULT 0`,
	}

	for dialect, want := range tests {
		t.Run(dialect, func(t *testing.T) {
			state := &schemaState{table: true}
			require.NoError(t, NewSessionsVersionMigration().Up()(schemaDB(t, dialect, state)))

			assert.Equal(t, []string{want}, state.execSQL())
		})
	}
}

// TestVersionUpIsANoOpWhenTheColumnExists: re-running it, or running it on a table an older
// install already gave the column, sends nothing.
func TestVersionUpIsANoOpWhenTheColumnExists(t *testing.T) {
	for _, dialect := range everyEngine {
		t.Run(dialect, func(t *testing.T) {
			state := &schemaState{table: true, column: true}
			require.NoError(t, NewSessionsVersionMigration().Up()(schemaDB(t, dialect, state)))

			assert.Empty(t, state.execs)
		})
	}
}

// TestVersionUpRefusesWithoutTheSessionsTable: the column belongs to a table another
// migration creates, and adding it to nothing must say which migration comes first rather
// than fail with the engine's own error.
func TestVersionUpRefusesWithoutTheSessionsTable(t *testing.T) {
	for _, dialect := range everyEngine {
		t.Run(dialect, func(t *testing.T) {
			state := &schemaState{}
			err := NewSessionsVersionMigration().Up()(schemaDB(t, dialect, state))

			require.Error(t, err)
			assert.Contains(t, err.Error(), "create_sessions_table")
			assert.Empty(t, state.execs)
		})
	}
}

// TestVersionDownDropsTheColumnOnEveryEngine pins the whole rollback per engine. Postgres and
// MySQL drop a column whatever default it has, so the DROP COLUMN is all they get. SQL Server
// first gets the batch that drops the column's default constraint, and only then the DROP.
func TestVersionDownDropsTheColumnOnEveryEngine(t *testing.T) {
	tests := map[string][]string{
		"postgres":  {`ALTER TABLE "sessions" DROP COLUMN "version"`},
		"mysql":     {"ALTER TABLE `sessions` DROP COLUMN `version`"},
		"sqlserver": {sqlServerDropVersionDefaultSQL, `ALTER TABLE "sessions" DROP COLUMN "version"`},
	}

	for dialect, want := range tests {
		t.Run(dialect, func(t *testing.T) {
			state := &schemaState{table: true, column: true}
			require.NoError(t, NewSessionsVersionMigration().Down()(schemaDB(t, dialect, state)))

			assert.Equal(t, want, state.execSQL())
		})
	}
}

// TestVersionDownIsANoOpWithoutTheColumn: rolling back what was never applied, or was already
// rolled back, sends nothing on any engine, the constraint batch included.
func TestVersionDownIsANoOpWithoutTheColumn(t *testing.T) {
	for _, dialect := range everyEngine {
		for name, state := range map[string]*schemaState{
			"no table":  {},
			"no column": {table: true},
		} {
			t.Run(dialect+"/"+name, func(t *testing.T) {
				require.NoError(t, NewSessionsVersionMigration().Down()(schemaDB(t, dialect, state)))
				assert.Empty(t, state.execs)
			})
		}
	}
}

// TestSQLServerVersionDownDropsTheDefaultConstraintFirst pins the batch itself.
//
// The constraint's name is SQL Server's own, so it is looked up in sys.default_constraints for
// that column of that table. It is dropped through a statement built in @sql and run with
// sp_executesql: the shorter EXEC (N'…' + QUOTENAME(@df)) is not valid T-SQL, because EXEC's
// parenthesised form concatenates only literals and variables. And it reaches the server byte
// for byte as written, with nothing bound: gorm parses SQL containing '@' for named arguments,
// and this proves it wrote @df and @sql back untouched rather than turning them into
// parameters.
func TestSQLServerVersionDownDropsTheDefaultConstraintFirst(t *testing.T) {
	state := &schemaState{table: true, column: true}
	require.NoError(t, NewSessionsVersionMigration().Down()(schemaDB(t, "sqlserver", state)))

	require.Len(t, state.execs, 2)
	batch := state.execs[0]
	assert.Equal(t, sqlServerDropVersionDefaultSQL, batch.sql, "sent verbatim")
	assert.Zero(t, batch.args, "with no arguments bound")

	assert.Contains(t, batch.sql, "EXEC sp_executesql @sql")
	assert.Contains(t, batch.sql, "QUOTENAME(@df)")
	assert.Contains(t, batch.sql, "FROM sys.default_constraints")
	assert.Contains(t, batch.sql, "OBJECT_ID(N'sessions')")
	assert.Contains(t, batch.sql, "c.name = N'version'")
	assert.Contains(t, batch.sql, "IF @df IS NOT NULL", "a column without a default is not an error")
	assert.NotRegexp(t, regexp.MustCompile(`(?is)EXEC\s*\([^)]*\+\s*QUOTENAME`), batch.sql,
		"EXEC ( … + QUOTENAME(…)) is not valid T-SQL")
	assert.NotContains(t, batch.sql, "@p", "nothing may have been turned into a parameter")

	assert.Equal(t, `ALTER TABLE "sessions" DROP COLUMN "version"`, state.execs[1].sql,
		"the column goes only after its default")
}

// TestSQLServerVersionDownStopsWhenTheDefaultCannotBeDropped: if the constraint batch fails,
// the DROP COLUMN it exists to make possible is not attempted, and the error says which step
// failed. db:migrate then rolls back the transaction it runs Down in.
func TestSQLServerVersionDownStopsWhenTheDefaultCannotBeDropped(t *testing.T) {
	state := &schemaState{table: true, column: true, failExec: "sp_executesql"}
	err := NewSessionsVersionMigration().Down()(schemaDB(t, "sqlserver", state))

	require.ErrorIs(t, err, errSchemaInjected)
	assert.Contains(t, err.Error(), "cannot drop the default of the version column")
	require.Len(t, state.execs, 1)
	assert.False(t, strings.Contains(state.execs[0].sql, "DROP COLUMN"))
}

// TestSQLServerVersionMigrationLooksOnlyInItsOwnSchema: gorm's SQL Server migrator asks
// INFORMATION_SCHEMA whether a column exists in a table of that name in any schema of the
// database. With a sessions table and version column in another schema, Up used to add nothing
// to gorgany's table, and Down to drop a column gorgany's table did not have. Both now ask
// COL_LENGTH, which resolves the name as the ALTER TABLE after it does.
func TestSQLServerVersionMigrationLooksOnlyInItsOwnSchema(t *testing.T) {
	up := &schemaState{table: true, otherSchema: true}
	require.NoError(t, NewSessionsVersionMigration().Up()(schemaDB(t, "sqlserver", up)))
	assert.Equal(t, []string{`ALTER TABLE "sessions" ADD "version" bigint NOT NULL DEFAULT 0`}, up.execSQL())

	down := &schemaState{table: true, otherSchema: true}
	require.NoError(t, NewSessionsVersionMigration().Down()(schemaDB(t, "sqlserver", down)))
	assert.Empty(t, down.execs, "the column is another schema's, and not this migration's to drop")

	missing := &schemaState{otherSchema: true}
	err := NewSessionsVersionMigration().Up()(schemaDB(t, "sqlserver", missing))
	require.Error(t, err, "another schema's sessions table is not the one create_sessions_table makes")
	assert.Contains(t, err.Error(), "create_sessions_table")
}

// TestTheDocumentedDropDefaultBatchIsTheMigrations: docs/SQLSERVER.md shows the batch Down runs,
// for an app's own migration to copy, and a copy kept by hand drifts from what it copies. It
// must be the batch byte for byte, apart from the indentation of the list item it sits in.
func TestTheDocumentedDropDefaultBatchIsTheMigrations(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "docs", "SQLSERVER.md"))
	require.NoError(t, err)

	var documented []string
	for _, block := range regexp.MustCompile("(?s)\n( *)```sql\n(.*?)\n *```").FindAllStringSubmatch(string(content), -1) {
		lines := strings.Split(block[2], "\n")
		for i, line := range lines {
			lines[i] = strings.TrimPrefix(line, block[1])
		}
		if body := strings.Join(lines, "\n"); strings.Contains(body, "sp_executesql") {
			documented = append(documented, body)
		}
	}
	require.Len(t, documented, 1, "docs/SQLSERVER.md must show the batch once")
	assert.Equal(t, sqlServerDropVersionDefaultSQL, documented[0])
}
