package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	mysqldrv "gorm.io/driver/mysql"
	pgdrv "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// This suite asserts the DDL both engines actually receive, with no database
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
func TestUpEmitsASingleStatementPerExec(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			db, stmts := dryRunDB(t, dialect)
			require.NoError(t, db.Migrator().CreateTable(SessionsTableModel()))

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
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			db, stmts := dryRunDB(t, tt.dialect)
			require.NoError(t, db.Migrator().CreateTable(SessionsTableModel()))

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

// TestDownIsValidOnBothEngines covers the DROP.
//
// Note: contrary to what one might expect, `DROP TABLE ... CASCADE` is *not* a
// MySQL syntax error — MySQL 8 documents RESTRICT and CASCADE as accepted no-ops
// "to make porting easier". The old Down() was therefore already portable; it is
// rewritten only so both directions go through the same Migrator. Delegating does
// preserve the meaningful CASCADE on Postgres, where it is not a no-op.
func TestDownIsValidOnBothEngines(t *testing.T) {
	pg, pgStmts := dryRunDB(t, "postgres")
	require.NoError(t, pg.Migrator().DropTable(SessionsTableModel()))
	assert.Contains(t, strings.Join(*pgStmts, "\n"), `DROP TABLE IF EXISTS "sessions" CASCADE`)

	my, myStmts := dryRunDB(t, "mysql")
	require.NoError(t, my.Migrator().DropTable(SessionsTableModel()))
	assert.Contains(t, strings.Join(*myStmts, "\n"), "DROP TABLE IF EXISTS `sessions`")
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

// TestSessionsTableName pins the table the auth session storage reads.
func TestSessionsTableName(t *testing.T) {
	named, ok := SessionsTableModel().(interface{ TableName() string })
	require.True(t, ok)
	assert.Equal(t, "sessions", named.TableName())
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
