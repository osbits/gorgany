package testsupport

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	_ "github.com/osbits/gorgany/v2/db/sql/driver/builtin" // register the framework's drivers
	"gorm.io/gorm"
)

// Database is one engine, migrated and ready, scoped to one test.
//
// Obtain it with RequireDatabase (skip when absent) or MustDatabase (fail when absent).
// Never construct one directly: the cleanup that keeps tests isolated is registered by
// those functions.
type Database struct {
	config     DatabaseConfig
	isolation  Isolation
	datasource dbCore.IDataSource
	gorm       *gorm.DB

	// tables are the tables the migrations created, in creation order. Truncation walks
	// them in reverse so a foreign key never blocks the delete.
	tables []string

	// session is the session handed to the test. Under IsolateByRollback it is bound to
	// the test's transaction.
	session dbCore.ISession

	// tx is the test's transaction under IsolateByRollback.
	tx dbCore.IDBTransaction

	// truncating is the engine's, shared by every test on it; see truncateSQLServer. Nil in a
	// Database no engine prepared.
	truncating *sync.Mutex
}

// Session is the database session the test should use.
//
// Under IsolateByRollback the test *must* use this one: anything opening its own
// connection will not see the uncommitted rows.
func (d *Database) Session() dbCore.ISession {
	return d.session
}

// DataSource is the underlying datasource, for a test that needs to open its own session.
func (d *Database) DataSource() dbCore.IDataSource {
	return d.datasource
}

// Gorm is the raw *gorm.DB, for assertions the ORM cannot express.
//
// Under IsolateByRollback it is NOT inside the test's transaction — gorm's transaction is
// a separate handle the session does not expose — so it cannot see the test's uncommitted
// rows. That is occasionally what you want (proving a rollback actually happened is the
// obvious case), but for reading back what the test just wrote, use Exec, CountRows, or
// Session.
func (d *Database) Gorm() *gorm.DB {
	return d.gorm
}

// Driver reports which engine this is: DriverPostgres, DriverMySQL or DriverSQLServer.
//
// Tests need it more often than they should have to — a dialect-specific assertion about
// quoting or a RETURNING clause has to know which engine it is talking to.
func (d *Database) Driver() string {
	return d.config.Driver
}

// IsPostgres, IsMySQL and IsSQLServer are the readable forms of a Driver comparison.
func (d *Database) IsPostgres() bool  { return d.config.Driver == DriverPostgres }
func (d *Database) IsMySQL() bool     { return d.config.Driver == DriverMySQL }
func (d *Database) IsSQLServer() bool { return d.config.Driver == DriverSQLServer }

// Label is how this database appears in test output.
func (d *Database) Label() string { return d.config.Label() }

// Tables lists the tables the migrations created.
func (d *Database) Tables() []string {
	out := make([]string, len(d.tables))
	copy(out, d.tables)
	return out
}

// CountRows returns how many rows a table holds. Fails the test on a query error, so a
// caller can use it inline in an assertion.
func (d *Database) CountRows(t *testing.T, table string) int {
	t.Helper()

	count, err := d.executor().CountRaw(context.Background(),
		"SELECT COUNT(*) FROM "+d.quoteIdentifier(table))
	if err != nil {
		t.Fatalf("testsupport: counting %s: %v", table, err)
	}
	return int(count)
}

// Exec runs a statement, failing the test on error.
func (d *Database) Exec(t *testing.T, sql string, args ...any) {
	t.Helper()

	if result := d.executor().ExecRaw(context.Background(), sql, args...); result.Error != nil {
		t.Fatalf("testsupport: %s: %v", sql, result.Error)
	}
}

// Truncate empties the given tables, or every migrated table when none are named.
//
// Called automatically after each test under IsolateByTruncation; exported for a test that
// wants a clean slate part-way through.
func (d *Database) Truncate(t *testing.T, tables ...string) {
	t.Helper()

	if err := d.truncate(tables...); err != nil {
		t.Fatalf("testsupport: truncating: %v", err)
	}
}

// executor is where a helper's statements go.
//
// Under IsolateByRollback that has to be the *transaction*, not the session and not the
// raw gorm handle. The first version of this reached for a *gorm.DB and fell back to the
// non-transactional one when the transaction did not expose it — which the transaction
// does not — so every helper write committed and rollback isolation silently did nothing.
// Caught by running the live test, which is the point of having one.
func (d *Database) executor() dbCore.IQueryExecutor {
	if d.tx != nil {
		return d.tx
	}
	return d.session.Executor()
}

// truncate empties tables in reverse creation order.
//
// Reverse order matters: the migrations create parents before children, so deleting
// forwards hits a foreign key. Postgres gets one TRUNCATE ... CASCADE, which is both
// faster and immune to ordering; MySQL has no CASCADE on TRUNCATE, so its foreign key
// checks are suspended for the duration instead. SQL Server has neither, and its constraint
// switch outlives the session, so it gets a sequence of its own; see truncateSQLServer.
//
// Every other engine is an error that says so. This used to treat anything that was not
// Postgres as MySQL, so another engine was sent MySQL's statements and the test failed on
// whatever that engine made of them, an error that pointed at MySQL syntax rather than at
// the harness.
func (d *Database) truncate(tables ...string) error {
	targets := tables
	if len(targets) == 0 {
		targets = d.tables
	}
	if len(targets) == 0 {
		return nil
	}

	reversed := make([]string, 0, len(targets))
	for i := len(targets) - 1; i >= 0; i-- {
		reversed = append(reversed, targets[i])
	}

	switch d.config.Driver {
	case DriverPostgres:
		statement := "TRUNCATE TABLE " + strings.Join(d.quoteAll(reversed), ", ") + " RESTART IDENTITY CASCADE"
		return d.gorm.Exec(statement).Error
	case DriverMySQL:
		return d.truncateMySQL(d.quoteAll(reversed))
	case DriverSQLServer:
		return d.truncateSQLServer(reversed, len(tables) > 0)
	default:
		return fmt.Errorf("%s: cannot empty tables on driver %q: truncation is implemented for "+
			"%s, %s and %s only; use IsolateByRollback", d.Label(), d.config.Driver,
			DriverPostgres, DriverMySQL, DriverSQLServer)
	}
}

// quoteAll quotes each table name for this engine.
func (d *Database) quoteAll(tables []string) []string {
	quoted := make([]string, 0, len(tables))
	for _, table := range tables {
		quoted = append(quoted, d.quoteIdentifier(table))
	}
	return quoted
}

// truncateMySQL empties the quoted tables with foreign key checks suspended, since MySQL has
// no TRUNCATE ... CASCADE. The switch is scoped to the session, which is why suspending it is
// safe here and would not be on an engine where it persists.
func (d *Database) truncateMySQL(quoted []string) error {
	// Restore the check even if a truncate fails, or every later test in the process runs
	// without referential integrity and passes when it should not.
	if err := d.gorm.Exec("SET FOREIGN_KEY_CHECKS = 0").Error; err != nil {
		return err
	}
	defer func() { _ = d.gorm.Exec("SET FOREIGN_KEY_CHECKS = 1").Error }()

	for _, table := range quoted {
		if err := d.gorm.Exec("TRUNCATE TABLE " + table).Error; err != nil {
			return fmt.Errorf("truncating %s: %w", table, err)
		}
	}
	return nil
}

// quoteIdentifier quotes a table name for this engine.
//
// The dialect would be the right place to ask, but it is reached through a builder and
// this runs raw SQL. The embedded-quote escape is the part that matters: a table name is
// developer-supplied, not caller-supplied, but silently producing broken SQL for an
// unusual name is still worse than producing correct SQL.
func (d *Database) quoteIdentifier(name string) string {
	switch d.config.Driver {
	case DriverMySQL:
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case DriverSQLServer:
		return quoteSQLServerIdentifier(name)
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

// ---------------------------------------------------------------- connection

// engine is one connected, migrated engine, shared by every test in the package.
//
// Connecting and migrating once per package rather than once per test is the difference
// between a suite that runs in seconds and one nobody runs.
type engine struct {
	config     DatabaseConfig
	datasource dbCore.IDataSource
	gorm       *gorm.DB
	tables     []string

	// err is why this engine is unusable, if it is. Kept rather than returned so every
	// test gets the same explanation instead of the first one getting it and the rest
	// timing out again.
	err error

	// migrateOnceGuard keeps the migrations to one run per engine per process.
	migrateOnceGuard sync.Once

	// truncating keeps this engine's SQL Server truncations to one at a time; see
	// truncateSQLServer.
	truncating sync.Mutex
}

var (
	enginesMu sync.Mutex
	engines   = map[string]*engine{}
)

// connect brings up one engine, at most once per process.
func connect(cfg Config, database DatabaseConfig) *engine {
	key := fmt.Sprintf("%s|%s|%d|%s", database.Driver, database.Host, database.Port, database.Database)

	enginesMu.Lock()
	defer enginesMu.Unlock()

	if existing, ok := engines[key]; ok {
		return existing
	}

	e := &engine{config: database}
	engines[key] = e

	// Parsed once, outside the retry loop: a config that does not parse is wrong on every
	// attempt, and retrying it for the whole engine wait only to skip reported it as an
	// engine that was not running. resolved has already parsed it, so this failing means the
	// harness itself is inconsistent, which is never a reason to skip either.
	parsed, err := dsconfig.Parse(database.datasourceConfig())
	if err != nil {
		e.err = configError{fmt.Errorf("testsupport: %s: %w", database.Label(), err)}
		return e
	}

	datasource, err := waitForEngine(database, parsed, cfg.EngineWait)
	if err != nil {
		e.err = err
		return e
	}
	e.datasource = datasource

	raw, err := datasource.GetDriver()
	if err != nil {
		e.err = fmt.Errorf("testsupport: %s: %w", database.Label(), err)
		return e
	}
	gormDb, ok := raw.(*gorm.DB)
	if !ok {
		e.err = fmt.Errorf(
			"testsupport: %s: driver is %T, not *gorm.DB; the harness only supports the "+
				"framework's gorm-backed datasources", database.Label(), raw)
		return e
	}
	e.gorm = gormDb

	return e
}

// waitForEngine retries until the engine accepts a connection or the budget runs out.
//
// A container started in the same CI step is usually not listening yet, and a suite that
// gives up on the first refused dial is a suite that fails intermittently.
//
// The datasource is built through the framework's own registry, so the harness connects
// exactly the way a booting app does. Only a failure to connect is retried. A driver the
// registry does not know, or a setting the driver refuses, is the same answer on every
// attempt, so it returns at once as a configError, which prepare never turns into a skip.
//
// On SQL Server each attempt first creates the database if it is missing, until that has
// worked once; see ensureSQLServerDatabase. It is inside the loop because a server that has
// just started accepts logins before it can create a database, and it is attempted before
// connecting because connecting to a database that does not exist fails as surely as
// connecting to a server that is not running. If it fails, the attempt still tries the
// database itself, which a login that may not create databases can reach once someone has
// created it.
//
// A database whose name the harness will not create (see sqlServerCreatableName) is never
// offered to CREATE DATABASE, which would refuse it on every attempt. When connecting to it
// fails, master is asked whether it exists, and a database that does not is a configError:
// no wait will bring it into being, and reporting it as an engine that never came up, as the
// harness did, sends the reader to start a container that is already running.
func waitForEngine(database DatabaseConfig, parsed dsconfig.DataSource, wait time.Duration) (dbCore.IDataSource, error) {
	if err := requireRegistered(database); err != nil {
		return nil, configError{err}
	}

	createDatabase := createsItsDatabase(database)
	uncreatable := createDatabase && !sqlServerCreatableName.MatchString(database.Database)
	if uncreatable {
		createDatabase = false
	}
	deadline := time.Now().Add(wait)
	var lastErr error

	for {
		var createErr error
		if createDatabase {
			createErr = ensureSQLServerDatabase(database, driver.New)
			if createErr == nil {
				createDatabase = false
			} else if isRefusal(createErr) {
				return nil, configError{fmt.Errorf("testsupport: %s: %w", database.Label(), createErr)}
			}
		}

		datasource, err := driver.New(parsed)
		if err == nil {
			if pingErr := ping(datasource); pingErr == nil {
				return datasource, nil
			} else {
				lastErr = pingErr
				_ = datasource.Close()
			}
		} else if isRefusal(err) {
			return nil, configError{fmt.Errorf("testsupport: %s: %w", database.Label(), err)}
		} else {
			lastErr = err
		}
		if createErr != nil {
			lastErr = fmt.Errorf("%w; creating the database first: %w", lastErr, createErr)
		}
		if uncreatable {
			// An error asking is left to the retry: master may be as far from ready as the target.
			if exists, err := sqlServerDatabaseExists(database, driver.New); err == nil && !exists {
				return nil, configError{fmt.Errorf("testsupport: %s: database %q does not exist on %s:%d, "+
					"and the harness creates only a SQL Server database named with letters, digits and "+
					"underscores, so it will not create this one; create it yourself, or rename it: %w",
					database.Label(), database.Database, database.Host, database.Port, lastErr)}
			}
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"testsupport: %s at %s:%d not reachable within %s: %w",
				database.Label(), database.Host, database.Port, wait, lastErr)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// configError is an engine failure that no wait can fix and no skip may hide: the harness
// was told something wrong, rather than finding nothing listening.
//
// prepare skips a test whose engine is absent when RequireDatabase asked, and a
// misconfiguration reported that way reads as "start the container", which sends the reader
// to the wrong place.
type configError struct{ err error }

func (e configError) Error() string { return e.err.Error() }
func (e configError) Unwrap() error { return e.err }

// isRefusal reports whether err is a driver's UnsupportedError, found through any wrapping:
// an engine may prefix its refusals with its name, and wrapped they are still refusals.
func isRefusal(err error) bool {
	var unsupported *dbCore.UnsupportedError
	return errors.As(err, &unsupported)
}

// isConfigError reports whether err is a configError.
func isConfigError(err error) bool {
	var target configError
	return errors.As(err, &target)
}

// ping verifies the connection is actually usable.
//
// Building a datasource does not necessarily dial — a lazily-connecting driver reports
// success and fails on first query — so the harness has to make a round trip before
// claiming the engine is up.
func ping(datasource dbCore.IDataSource) error {
	raw, err := datasource.GetDriver()
	if err != nil {
		return err
	}
	gormDb, ok := raw.(*gorm.DB)
	if !ok {
		return fmt.Errorf("driver is %T, not *gorm.DB", raw)
	}

	sqlDb, err := gormDb.DB()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return sqlDb.PingContext(ctx)
}

// migrate runs every registered migration, recording the tables that appeared so
// truncation knows what to empty.
func (e *engine) migrate(migrations []core.IMigration, migrateDown bool) error {
	if e.gorm == nil {
		return e.err
	}

	before, err := e.tableNames()
	if err != nil {
		return err
	}

	if migrateDown {
		// Reverse order, so a child table goes before its parent. Errors are ignored:
		// tearing down a schema that was never created is the normal case, and the Up
		// pass below is what has to succeed.
		for i := len(migrations) - 1; i >= 0; i-- {
			if down := migrations[i].Down(); down != nil {
				_ = down(e.gorm)
			}
		}
		before, err = e.tableNames()
		if err != nil {
			return err
		}
	}

	for _, migration := range migrations {
		up := migration.Up()
		if up == nil {
			return fmt.Errorf("testsupport: migration %q has a nil Up closure", migration.Name())
		}
		if err := up(e.gorm); err != nil {
			return fmt.Errorf("testsupport: migration %q: %w", migration.Name(), err)
		}

		// Capture after each migration, so the recorded order is creation order — which
		// is what makes reverse-order truncation respect foreign keys.
		after, err := e.tableNames()
		if err != nil {
			return err
		}
		e.tables = append(e.tables, added(before, after)...)
		before = after
	}

	return nil
}

// tableNames lists the tables currently in the database, sorted so `added` is
// deterministic.
func (e *engine) tableNames() (map[string]bool, error) {
	names, err := e.gorm.Migrator().GetTables()
	if err != nil {
		return nil, fmt.Errorf("testsupport: listing tables: %w", err)
	}

	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out, nil
}

// added returns the names in after that are not in before, sorted.
func added(before, after map[string]bool) []string {
	var out []string
	for name := range after {
		if !before[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
