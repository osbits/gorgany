package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mysqldrv "gorm.io/driver/mysql"
	pgdrv "gorm.io/driver/postgres"
	sqlserverdrv "gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	gorgdb "github.com/osbits/gorgany/v2/db"
)

// This suite asserts the DDL each engine actually receives, with no database
// running. GORM is opened in DryRun mode over a lazy *sql.DB whose driver never
// dials anything, and a capturing logger collects the SQL each Migrator call
// builds. That is enough to pin portability, which is what T1.5 is about; the
// live round trip (create, second run applies nothing) is the Docker check in the
// verification section of the brief.

// ---------------------------------------------------------- no-dial sql plumbing

type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) { return nopConn{}, nil }
func (nopConnector) Driver() driver.Driver                        { return nopDriver{} }

type nopDriver struct{}

func (nopDriver) Open(string) (driver.Conn, error) { return nopConn{}, nil }

type nopConn struct{}

func (nopConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (nopConn) Close() error                        { return nil }
func (nopConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

type capturingLogger struct{ stmts *[]string }

func (c capturingLogger) LogMode(logger.LogLevel) logger.Interface      { return c }
func (c capturingLogger) Info(context.Context, string, ...interface{})  {}
func (c capturingLogger) Warn(context.Context, string, ...interface{})  {}
func (c capturingLogger) Error(context.Context, string, ...interface{}) {}
func (c capturingLogger) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	statement, _ := fc()
	*c.stmts = append(*c.stmts, statement)
}

// dryRunDB opens a GORM handle that builds SQL but never executes it.
func dryRunDB(t *testing.T, dialect string) (*gorm.DB, *[]string) {
	t.Helper()

	conn := sql.OpenDB(nopConnector{})
	t.Cleanup(func() { _ = conn.Close() })

	stmts := &[]string{}

	var dialector gorm.Dialector
	switch dialect {
	case "postgres":
		dialector = pgdrv.New(pgdrv.Config{Conn: conn})
	case "mysql":
		dialector = mysqldrv.New(mysqldrv.Config{Conn: conn, SkipInitializeWithVersion: true})
	case "sqlserver":
		// Only this test file imports the SQL Server driver. The migrations recognise it by
		// name, so the package itself links none of it.
		dialector = sqlserverdrv.New(sqlserverdrv.Config{Conn: conn})
	default:
		t.Fatalf("unknown dialect %q", dialect)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		DryRun: true,
		Logger: capturingLogger{stmts: stmts},
	})
	require.NoError(t, err)
	return db, stmts
}

// ------------------------------------------------------------------------ tests

// TestUpEmitsNoCreateIndexIfNotExistsOnMySQL is the T1.5 regression. The old
// hand-written DDL used `CREATE INDEX IF NOT EXISTS`, which MySQL has no syntax
// for, so `db:migrate up` failed outright on MySQL before an app's own migrations
// ever ran. Expressing the table through Migrator sidesteps it entirely: MySQL
// declares both indexes inline in CREATE TABLE.
func TestUpEmitsNoCreateIndexIfNotExistsOnMySQL(t *testing.T) {
	db, stmts := dryRunDB(t, "mysql")

	require.NoError(t, db.Migrator().CreateTable(SessionsTableModel()))

	all := strings.Join(*stmts, "\n")
	assert.NotContains(t, strings.ToUpper(all), "IF NOT EXISTS",
		"MySQL has no IF NOT EXISTS on CREATE INDEX")
	assert.Contains(t, all, "CREATE TABLE `sessions`")
	assert.Contains(t, all, "INDEX `idx_sessions_expiry` (`expiry`)")
	assert.Contains(t, all, "INDEX `idx_sessions_user_id` (`user_id`)")
}

// TestUpEmitsASingleStatementPerExec is the other reason the old Up() could not
// work on MySQL, and the one the brief missed: it passed three ';'-separated
// statements to one db.Exec. go-sql-driver/mysql rejects multi-statement queries
// unless multiStatements=true is in the DSN, which the framework does not set.
//
// go-mssqldb does run a multi-statement batch, but gorm-sqlserver creates the indexes after
// the table, each in a statement of its own, and SQL Server is held to the same rule so that
// nothing here depends on that.
func TestUpEmitsASingleStatementPerExec(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql", "sqlserver"} {
		t.Run(dialect, func(t *testing.T) {
			db, stmts := dryRunDB(t, dialect)
			require.NoError(t, db.Migrator().CreateTable(SessionsTableModelFor(dialect)))

			for _, statement := range *stmts {
				trimmed := strings.TrimSuffix(strings.TrimSpace(statement), ";")
				assert.NotContains(t, trimmed, ";",
					"each statement must be executed on its own: %q", statement)
			}
		})
	}
}

// TestCreateTableSchemaMatchesTheReplacedDDL pins the columns, so the migration
// keeps producing the same table shape it did before the rewrite.
func TestCreateTableSchemaMatchesTheReplacedDDL(t *testing.T) {
	tests := map[string]struct {
		dialect     string
		wantColumns []string
	}{
		"postgres": {
			dialect: "postgres",
			wantColumns: []string{
				`"id" varchar(255)`,
				`"user_id" varchar(255)`,
				`"expiry" timestamptz NOT NULL`,
				`"created_at" timestamptz NOT NULL`,
				`"last_activity" timestamptz NOT NULL`,
				`"attributes" text`,
				`PRIMARY KEY ("id")`,
			},
		},
		"mysql": {
			dialect: "mysql",
			wantColumns: []string{
				"`id` varchar(255)",
				"`user_id` varchar(255)",
				"`expiry` datetime(3) NOT NULL",
				"`created_at` datetime(3) NOT NULL",
				"`last_activity` datetime(3) NOT NULL",
				"`attributes` text",
				"PRIMARY KEY (`id`)",
			},
		},
		// The same table in SQL Server's types: Unicode strings (see sessionsSchemaSQLServer)
		// and datetimeoffset, the type gorm gives a time.Time there.
		"sqlserver": {
			dialect: "sqlserver",
			wantColumns: []string{
				`"id" nvarchar(255)`,
				`"user_id" nvarchar(255)`,
				`"expiry" datetimeoffset NOT NULL`,
				`"created_at" datetimeoffset NOT NULL`,
				`"last_activity" datetimeoffset NOT NULL`,
				`"attributes" nvarchar(max)`,
				`PRIMARY KEY ("id")`,
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			db, stmts := dryRunDB(t, tt.dialect)
			require.NoError(t, db.Migrator().CreateTable(SessionsTableModelFor(tt.dialect)))

			create := findStatement(t, *stmts, "CREATE TABLE")
			for _, want := range tt.wantColumns {
				assert.Contains(t, create, want)
			}
		})
	}
}

// TestPostgresKeepsCreateIndexIfNotExists documents that the portable path does not
// cost Postgres anything: its driver still emits the idempotent form.
func TestPostgresKeepsCreateIndexIfNotExists(t *testing.T) {
	db, stmts := dryRunDB(t, "postgres")
	require.NoError(t, db.Migrator().CreateTable(SessionsTableModel()))

	all := strings.Join(*stmts, "\n")
	assert.Contains(t, all, `CREATE INDEX IF NOT EXISTS "idx_sessions_expiry" ON "sessions" ("expiry")`)
	assert.Contains(t, all, `CREATE INDEX IF NOT EXISTS "idx_sessions_user_id" ON "sessions" ("user_id")`)
}

// TestDownIsValidOnEveryEngine covers the DROP.
//
// Note: contrary to what one might expect, `DROP TABLE ... CASCADE` is *not* a
// MySQL syntax error — MySQL 8 documents RESTRICT and CASCADE as accepted no-ops
// "to make porting easier". The old Down() was therefore already portable; it is
// rewritten only so both directions go through the same Migrator. Delegating does
// preserve the meaningful CASCADE on Postgres, where it is not a no-op. SQL Server has
// no CASCADE on DROP TABLE, and its driver does not emit one.
//
// Each engine runs the migration's own Down against a table that exists (see schemaDB): SQL
// Server's DropTable first reads the foreign keys that point at the table, which a dry run
// cannot answer.
func TestDownIsValidOnEveryEngine(t *testing.T) {
	tests := map[string]struct {
		want    string
		notWant string
	}{
		"postgres":  {want: `DROP TABLE IF EXISTS "sessions" CASCADE`},
		"mysql":     {want: "DROP TABLE IF EXISTS `sessions`"},
		"sqlserver": {want: `DROP TABLE IF EXISTS "sessions"`, notWant: "CASCADE"},
	}

	for dialect, tt := range tests {
		t.Run(dialect, func(t *testing.T) {
			state := &schemaState{table: true}
			require.NoError(t, NewSessionsMigration().Down()(schemaDB(t, dialect, state)))

			all := strings.Join(state.execSQL(), "\n")
			assert.Contains(t, all, tt.want)
			if tt.notWant != "" {
				assert.NotContains(t, all, tt.notWant)
			}
		})
	}
}

// TestDownWithoutTheTableSendsNothing: the rollback of a table that is not there is a no-op
// on every engine, not a DROP.
func TestDownWithoutTheTableSendsNothing(t *testing.T) {
	for _, dialect := range everyEngine {
		t.Run(dialect, func(t *testing.T) {
			state := &schemaState{}
			require.NoError(t, NewSessionsMigration().Down()(schemaDB(t, dialect, state)))
			assert.Empty(t, state.execs)
		})
	}
}

// TestUpCreatesTheTableInEachEnginesTypes runs the migration's own Up on a database without
// the table, so it proves Up picks the snapshot by the handle's dialect: the SQL Server table
// gets Unicode keys and the others keep the types they always had.
func TestUpCreatesTheTableInEachEnginesTypes(t *testing.T) {
	tests := map[string][]string{
		"postgres":  {`"id" varchar(255)`, `"attributes" text`},
		"mysql":     {"`id` varchar(255)", "`attributes` text"},
		"sqlserver": {`"id" nvarchar(255)`, `"user_id" nvarchar(255)`, `"attributes" nvarchar(max)`},
	}

	for dialect, want := range tests {
		t.Run(dialect, func(t *testing.T) {
			state := &schemaState{}
			require.NoError(t, NewSessionsMigration().Up()(schemaDB(t, dialect, state)))

			create := findStatement(t, state.execSQL(), "CREATE TABLE")
			for _, column := range want {
				assert.Contains(t, create, column)
			}
		})
	}
}

// TestSQLServerSessionsMigrationLooksOnlyInItsOwnSchema: gorm's SQL Server migrator asks
// INFORMATION_SCHEMA whether a table of that name exists in any schema of the database. With a
// sessions table in another schema, Up used to skip CREATE TABLE and fail creating an index on a
// table that was not there, and Down to drop a table that was not there. Both now ask OBJECT_ID,
// which resolves the name as the DDL after it does.
func TestSQLServerSessionsMigrationLooksOnlyInItsOwnSchema(t *testing.T) {
	up := &schemaState{otherSchema: true}
	require.NoError(t, NewSessionsMigration().Up()(schemaDB(t, "sqlserver", up)))
	findStatement(t, up.execSQL(), "CREATE TABLE")

	down := &schemaState{otherSchema: true}
	require.NoError(t, NewSessionsMigration().Down()(schemaDB(t, "sqlserver", down)))
	assert.Empty(t, down.execs)
}

// TestIndexNamesAreStable guards the constants other code and operators rely on.
func TestIndexNamesAreStable(t *testing.T) {
	assert.Equal(t, "idx_sessions_expiry", SessionsExpiryIndex)
	assert.Equal(t, "idx_sessions_user_id", SessionsUserIDIndex)
}

func TestMigrationIdentity(t *testing.T) {
	m := NewSessionsMigration()
	assert.Equal(t, "create_sessions_table", m.Name())
	assert.NotNil(t, m.Up())
	assert.NotNil(t, m.Down())
}

// TestUpAndDownRejectNilHandle keeps a nil *gorm.DB from turning into a nil
// dereference deep inside Migrator.
func TestUpAndDownRejectNilHandle(t *testing.T) {
	m := NewSessionsMigration()

	require.Error(t, m.Up()(nil))
	require.Error(t, m.Down()(nil))
}

// TestSessionsTableName pins the table the auth session storage reads, on every engine.
func TestSessionsTableName(t *testing.T) {
	for _, model := range []any{
		SessionsTableModel(),
		SessionsTableModelFor("postgres"),
		SessionsTableModelFor("mysql"),
		SessionsTableModelFor("sqlserver"),
	} {
		named, ok := model.(interface{ TableName() string })
		require.True(t, ok)
		assert.Equal(t, "sessions", named.TableName())
	}
}

// TestSessionsTableModelForKeepsTheSnapshotOffSQLServer: every dialect but SQL Server gets the
// snapshot SessionsTableModel always returned, so what this migration creates on Postgres and
// MySQL, and what an app addressing the model sees, is unchanged.
func TestSessionsTableModelForKeepsTheSnapshotOffSQLServer(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql", "", "sqlite"} {
		assert.IsTypef(t, SessionsTableModel(), SessionsTableModelFor(dialect), "dialect %q", dialect)
	}
	assert.IsType(t, &sessionsSchemaSQLServer{}, SessionsTableModelFor("sqlserver"))
}

// TestTheSQLServerNameIsGormsOwn: the migrations recognise SQL Server by name so that this
// package links no SQL Server driver; the test may, and checks that the name is the one the
// driver reports.
func TestTheSQLServerNameIsGormsOwn(t *testing.T) {
	assert.Equal(t, sqlserverdrv.New(sqlserverdrv.Config{}).Name(), sqlServerDialect)
}

// TestSQLServerSessionsSchemaAvoidsNonUnicodeAndDeprecatedTypes: on SQL Server varchar and
// text store the code page of the collation, so a user id or an attribute outside it comes
// back with question marks in it, and text (like ntext and datetime's lack of an offset) is a
// type SQL Server has deprecated or that makes the sweep's cut-off depend on the server's time
// zone. The Postgres snapshot's tags would have produced exactly those.
func TestSQLServerSessionsSchemaAvoidsNonUnicodeAndDeprecatedTypes(t *testing.T) {
	db, stmts := dryRunDB(t, "sqlserver")
	require.NoError(t, db.Migrator().CreateTable(SessionsTableModelFor("sqlserver")))

	create := findStatement(t, *stmts, "CREATE TABLE")
	lowered := strings.ToLower(create)
	for _, forbidden := range []string{" varchar(", " text", " ntext", " datetime ", " datetime,", " datetime2"} {
		assert.NotContainsf(t, lowered, forbidden, "%q has no place in the SQL Server table:\n%s", forbidden, create)
	}

	// The keys are bounded, so SQL Server can index them (Msg 1919 refuses a MAX key column).
	assert.Contains(t, create, `"id" nvarchar(255)`)
	assert.Contains(t, create, `"user_id" nvarchar(255)`)

	// gorm-sqlserver creates the indexes after the table, one statement each.
	all := strings.Join(*stmts, "\n")
	assert.Contains(t, all, `CREATE INDEX "idx_sessions_expiry" ON "sessions"("expiry")`)
	assert.Contains(t, all, `CREATE INDEX "idx_sessions_user_id" ON "sessions"("user_id")`)
}

// TestTheSQLServerSnapshotHasTheSameColumnsAndIndexes: the SQL Server snapshot differs from
// sessionsSchema in its string types and nothing else, so SQL Server's table cannot drift apart
// from Postgres's and MySQL's in a column or an index.
func TestTheSQLServerSnapshotHasTheSameColumnsAndIndexes(t *testing.T) {
	portable := reflect.TypeOf(sessionsSchema{})
	sqlServer := reflect.TypeOf(sessionsSchemaSQLServer{})
	require.Equal(t, portable.NumField(), sqlServer.NumField())

	typeTag := regexp.MustCompile(`;?type:[^;]*`)
	for i := 0; i < portable.NumField(); i++ {
		a, b := portable.Field(i), sqlServer.Field(i)
		assert.Equal(t, a.Name, b.Name)
		assert.Equal(t, a.Type, b.Type)
		assert.Equalf(t,
			typeTag.ReplaceAllString(a.Tag.Get("gorm"), ""),
			typeTag.ReplaceAllString(b.Tag.Get("gorm"), ""),
			"%s may differ only in its type", a.Name)
	}
}

// TestBookkeepingTablesAreLegalOnSQLServer: db:migrate and db:seed create the migrations and
// seeders tables with AutoMigrate, and both have a unique name column. nvarchar(max) cannot be
// a key column on SQL Server (Msg 1919), so an unbounded string there would fail the very first
// db:migrate. gorm-sqlserver bounds a string that carries a unique or index tag, and this pins
// that it still does, at the nvarchar(256) docs/SQLSERVER.md states. The datasource opens the
// dialector as this handle does, with no DefaultStringSize, which would change the length.
func TestBookkeepingTablesAreLegalOnSQLServer(t *testing.T) {
	for name, model := range map[string]any{"migrations": &gorgdb.Migration{}, "seeders": &gorgdb.Seeder{}} {
		t.Run(name, func(t *testing.T) {
			db, stmts := dryRunDB(t, "sqlserver")
			require.NoError(t, db.Migrator().CreateTable(model))

			create := findStatement(t, *stmts, "CREATE TABLE")
			assert.Contains(t, create, `"name" nvarchar(256)`)
			assert.Contains(t, create, "UNIQUE")
			assert.Contains(t, create, `"date" datetimeoffset`)
		})
	}
}

func findStatement(t *testing.T, stmts []string, prefix string) string {
	t.Helper()

	re := regexp.MustCompile(`\s+`)
	for _, statement := range stmts {
		normalized := re.ReplaceAllString(strings.TrimSpace(statement), " ")
		if strings.HasPrefix(strings.ToUpper(normalized), strings.ToUpper(prefix)) {
			return normalized
		}
	}
	t.Fatalf("no statement starting with %q in %v", prefix, stmts)
	return ""
}
