package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	mysqldrv "gorm.io/driver/mysql"
	pgdrv "gorm.io/driver/postgres"
	sqlserverdrv "gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The dry-run handle in sessions_migration_test.go shows the DDL a Migrator call builds, but
// it cannot run a migration's Up or Down: both ask the Migrator whether the table and the
// column exist first, and in DryRun mode gorm answers those questions with a nil row (SQL
// Server and MySQL scan it and fail) or with zero (Postgres), so every guarded branch past the
// first is out of reach. This driver answers them instead. It holds a schema of one table with
// one optional column, answers the existence queries every dialect sends from that, and
// records each statement it is asked to execute, with the number of arguments bound to it, so
// a test can run the real closure on each engine and assert on exactly what the server would
// have received.

// schemaState is the fake database: whether the sessions table and its version column exist,
// what was executed, and an Exec to fail.
type schemaState struct {
	table  bool
	column bool

	// otherSchema puts a sessions table with a version column in another schema of the
	// database. INFORMATION_SCHEMA lists it, with no schema filter in the queries gorm's SQL
	// Server migrator sends, while OBJECT_ID and COL_LENGTH, which resolve the unqualified name
	// in the default schema, do not find it.
	otherSchema bool

	// failExec makes any Exec whose SQL contains it fail with errSchemaInjected.
	failExec string

	execs   []executed
	queries []string
}

// executed is one statement the driver was asked to execute.
type executed struct {
	sql  string
	args int
}

var errSchemaInjected = errors.New("injected failure")

// execSQL returns the SQL of every executed statement, in order.
func (s *schemaState) execSQL() []string {
	out := make([]string, 0, len(s.execs))
	for _, e := range s.execs {
		out = append(out, e.sql)
	}
	return out
}

func (s *schemaState) Connect(context.Context) (driver.Conn, error) { return &schemaConn{s: s}, nil }
func (s *schemaState) Driver() driver.Driver                        { return schemaDriver{s: s} }

type schemaDriver struct{ s *schemaState }

func (d schemaDriver) Open(string) (driver.Conn, error) { return &schemaConn{s: d.s}, nil }

type schemaConn struct{ s *schemaState }

func (c *schemaConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("schema driver: Prepare is not supported")
}
func (c *schemaConn) Close() error { return nil }
func (c *schemaConn) Begin() (driver.Tx, error) {
	return nil, errors.New("schema driver: transactions are not supported")
}

func (c *schemaConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.s.execs = append(c.s.execs, executed{sql: query, args: len(args)})
	if c.s.failExec != "" && strings.Contains(query, c.s.failExec) {
		return nil, errSchemaInjected
	}
	return driver.RowsAffected(0), nil
}

// QueryContext answers the questions the Migrators and the migrations ask here: which foreign
// keys point at the table (none: SQL Server's DropTable asks before it drops), whether the
// column exists in the default schema (COL_LENGTH), whether the table does (OBJECT_ID, which
// SQL Server's HasIndex asks with too), whether a column exists in any schema (a count over
// the columns view), whether a table does (any other count), and the name of the current
// database or schema (anything else).
func (c *schemaConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.s.queries = append(c.s.queries, query)

	lowered := strings.ToLower(query)
	switch {
	case strings.Contains(lowered, "sys.foreign_keys"):
		return &oneValueRows{done: true}, nil
	case strings.Contains(lowered, "col_length("):
		return &oneValueRows{value: countOf(c.s.column)}, nil
	case strings.Contains(lowered, "object_id("):
		return &oneValueRows{value: countOf(c.s.table)}, nil
	case strings.Contains(lowered, "count(*)") && strings.Contains(lowered, "columns"):
		return &oneValueRows{value: countOf(c.s.column || c.s.otherSchema)}, nil
	case strings.Contains(lowered, "count(*)"):
		return &oneValueRows{value: countOf(c.s.table || c.s.otherSchema)}, nil
	default:
		return &oneValueRows{value: "gorgany_test"}, nil
	}
}

func countOf(exists bool) int64 {
	if exists {
		return 1
	}
	return 0
}

// oneValueRows is a result of one row holding one value, or of no rows when done is set.
type oneValueRows struct {
	value driver.Value
	done  bool
}

func (r *oneValueRows) Columns() []string { return []string{"value"} }
func (r *oneValueRows) Close() error      { return nil }
func (r *oneValueRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.value
	return nil
}

// schemaDB opens a gorm handle with dialect's dialector over state. Unlike dryRunDB it executes
// what it builds, against the fake schema.
func schemaDB(t *testing.T, dialect string, state *schemaState) *gorm.DB {
	t.Helper()

	conn := sql.OpenDB(state)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	var dialector gorm.Dialector
	switch dialect {
	case "postgres":
		dialector = pgdrv.New(pgdrv.Config{Conn: conn})
	case "mysql":
		dialector = mysqldrv.New(mysqldrv.Config{Conn: conn, SkipInitializeWithVersion: true})
	case "sqlserver":
		dialector = sqlserverdrv.New(sqlserverdrv.Config{Conn: conn})
	default:
		t.Fatalf("unknown dialect %q", dialect)
	}

	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	return db
}

// everyEngine is the dialects the migrations run on.
var everyEngine = []string{"postgres", "mysql", "sqlserver"}
