package testsupport

import (
	"context"
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

// Driver reports which engine this is: DriverPostgres or DriverMySQL.
//
// Tests need it more often than they should have to — a dialect-specific assertion about
// quoting or a RETURNING clause has to know which engine it is talking to.
func (d *Database) Driver() string {
	return d.config.Driver
}

// IsPostgres and IsMySQL are the readable forms of a Driver comparison.
func (d *Database) IsPostgres() bool { return d.config.Driver == DriverPostgres }
func (d *Database) IsMySQL() bool    { return d.config.Driver == DriverMySQL }

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
// checks are suspended for the duration instead.
func (d *Database) truncate(tables ...string) error {
	targets := tables
	if len(targets) == 0 {
		targets = d.tables
	}
	if len(targets) == 0 {
		return nil
	}

	quoted := make([]string, 0, len(targets))
	for i := len(targets) - 1; i >= 0; i-- {
		quoted = append(quoted, d.quoteIdentifier(targets[i]))
	}

	if d.config.Driver == DriverPostgres {
		statement := "TRUNCATE TABLE " + strings.Join(quoted, ", ") + " RESTART IDENTITY CASCADE"
		return d.gorm.Exec(statement).Error
	}

	// MySQL. Restore the check even if a truncate fails, or every later test in the
	// process runs without referential integrity and passes when it should not.
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
	if d.config.Driver == DriverMySQL {
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	}
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
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

	datasource, err := waitForEngine(database, cfg.EngineWait)
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
func waitForEngine(database DatabaseConfig, wait time.Duration) (dbCore.IDataSource, error) {
	dsConfig := database.datasourceConfig()

	deadline := time.Now().Add(wait)
	var lastErr error

	for {
		datasource, err := newDatasource(dsConfig)
		if err == nil {
			if pingErr := ping(datasource); pingErr == nil {
				return datasource, nil
			} else {
				lastErr = pingErr
				_ = datasource.Close()
			}
		} else {
			lastErr = err
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"testsupport: %s at %s:%d not reachable within %s: %w",
				database.Label(), database.Host, database.Port, wait, lastErr)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// newDatasource parses a config map and builds the datasource through the framework's own
// registry, so the harness connects exactly the way a booting app does.
func newDatasource(dsConfig map[string]any) (dbCore.IDataSource, error) {
	parsed, err := dsconfig.Parse(dsConfig)
	if err != nil {
		return nil, err
	}
	return driver.New(parsed)
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
