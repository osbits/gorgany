package testsupport

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// Harness holds a suite's configuration and migrations.
//
// Most suites never touch it: Main and RequireDatabase use the default harness, configured
// from the environment. Use New when one package needs settings of its own.
type Harness struct {
	config     Config
	migrations []core.IMigration

	resolveOnce sync.Once
	resolved    Config
	resolveErr  error
}

// New creates a harness with explicit settings.
//
// A zero Config is the same as FromEnv, so New(Config{}) is a valid way to get the default
// behaviour with room to add migrations.
func New(config Config) *Harness {
	return &Harness{config: config}
}

var defaultHarness = New(Config{})

// Default returns the harness Main and RequireDatabase use.
func Default() *Harness { return defaultHarness }

// AddMigration registers a migration to run before the suite.
//
// Call it from an init function or from TestMain before Main, since Main runs them.
func (h *Harness) AddMigration(migrations ...core.IMigration) *Harness {
	h.migrations = append(h.migrations, migrations...)
	return h
}

// AddMigration registers a migration on the default harness.
func AddMigration(migrations ...core.IMigration) *Harness {
	return defaultHarness.AddMigration(migrations...)
}

// Configure replaces the default harness's settings.
//
// Call it before Main. A suite that needs two harnesses at once should use New instead.
func Configure(config Config) *Harness {
	defaultHarness.config = config
	defaultHarness.resolveOnce = sync.Once{}
	return defaultHarness
}

// Main runs a suite, connecting and migrating once for the whole package.
//
//	func TestMain(m *testing.M) {
//	    testsupport.AddMigration(migrations.All()...)
//	    testsupport.Main(m)
//	}
//
// It does not fail when no engine is reachable: individual tests decide that, through
// RequireDatabase (skip) or MustDatabase (fail). A package with some tests that need a
// database and some that do not stays runnable either way.
func Main(m *testing.M) {
	os.Exit(defaultHarness.Run(m))
}

// Run is Main without the os.Exit, for a TestMain that has its own teardown.
func (h *Harness) Run(m *testing.M) int {
	code := m.Run()
	h.closeEngines()
	return code
}

// closeEngines releases every connection this process opened.
func (h *Harness) closeEngines() {
	enginesMu.Lock()
	defer enginesMu.Unlock()

	for key, e := range engines {
		if e.datasource != nil {
			_ = e.datasource.Close()
		}
		delete(engines, key)
	}
}

// resolve validates and defaults the config, once.
func (h *Harness) resolve() (Config, error) {
	h.resolveOnce.Do(func() {
		h.resolved, h.resolveErr = h.config.resolved()
	})
	return h.resolved, h.resolveErr
}

// RequireDatabase returns a migrated database for this test, skipping when no engine is
// reachable.
//
// Skipping is the right default for a developer's machine: a framework suite that fails
// because nothing is started is a suite people learn to ignore. Use MustDatabase in CI,
// where an absent engine means the pipeline is misconfigured.
func RequireDatabase(t *testing.T) *Database {
	t.Helper()
	return defaultHarness.database(t, true)
}

// MustDatabase returns a migrated database for this test, failing when no engine is
// reachable.
func MustDatabase(t *testing.T) *Database {
	t.Helper()
	return defaultHarness.database(t, false)
}

// RequireDatabase and MustDatabase on a specific harness.
func (h *Harness) RequireDatabase(t *testing.T) *Database {
	t.Helper()
	return h.database(t, true)
}

func (h *Harness) MustDatabase(t *testing.T) *Database {
	t.Helper()
	return h.database(t, false)
}

// database prepares the first configured engine for this test.
func (h *Harness) database(t *testing.T, skipIfAbsent bool) *Database {
	t.Helper()

	config, err := h.resolve()
	if err != nil {
		// A bad config is always a failure. Skipping would hide a typo in an
		// environment variable behind "no engine available", which is the wrong
		// diagnosis and a very expensive one to chase.
		t.Fatal(err)
	}

	return h.prepare(t, config, config.Databases[0], skipIfAbsent)
}

// EachDatabase runs fn as a subtest against every configured engine.
//
//	testsupport.EachDatabase(t, func(t *testing.T, db *testsupport.Database) {
//	    // ... runs once per engine, named after it
//	})
//
// This is how one suite covers Postgres and MySQL. An engine that is not reachable skips
// its own subtest rather than the whole set, so a laptop with only Postgres running still
// gets Postgres coverage.
func EachDatabase(t *testing.T, fn func(*testing.T, *Database)) {
	t.Helper()
	defaultHarness.EachDatabase(t, fn)
}

func (h *Harness) EachDatabase(t *testing.T, fn func(*testing.T, *Database)) {
	t.Helper()

	config, err := h.resolve()
	if err != nil {
		t.Fatal(err)
	}

	for _, database := range config.Databases {
		database := database
		t.Run(database.Label(), func(t *testing.T) {
			fn(t, h.prepare(t, config, database, true))
		})
	}
}

// prepare connects, migrates, opens the test's session and registers its cleanup.
func (h *Harness) prepare(t *testing.T, config Config, database DatabaseConfig, skipIfAbsent bool) *Database {
	t.Helper()

	e := connect(config, database)
	if e.err != nil {
		if skipIfAbsent {
			t.Skipf("%v", e.err)
		}
		t.Fatalf("%v", e.err)
	}

	e.migrateOnce(h.migrations, config.MigrateDown)
	if e.err != nil {
		// A migration that fails is never a reason to skip: the engine is there and the
		// schema is wrong, which is a real failure however the test was obtained.
		t.Fatalf("%v", e.err)
	}

	db := &Database{
		config:     e.config,
		isolation:  config.Isolation,
		datasource: e.datasource,
		gorm:       e.gorm,
		tables:     e.tables,
	}

	session, err := e.datasource.NewSession()
	if err != nil {
		t.Fatalf("testsupport: opening a session on %s: %v", database.Label(), err)
	}
	db.session = session

	// Start from a clean slate as well as finishing on one. A previous run that was
	// interrupted — a panic, a ^C, a `go test -timeout` kill — leaves rows behind, and a
	// test that fails because of the *last* run's data is the worst kind to debug.
	if !config.KeepData && config.Isolation == IsolateByTruncation {
		db.Truncate(t)
	}

	if config.Isolation == IsolateByRollback {
		db.beginRollbackScope(t)
	}

	t.Cleanup(func() {
		_ = session.Close()

		if config.KeepData {
			return
		}
		if config.Isolation != IsolateByTruncation {
			return
		}
		if err := db.truncate(); err != nil {
			t.Errorf("testsupport: cleaning up after the test: %v", err)
		}
	})

	return db
}

// beginRollbackScope runs the test inside a transaction that is always rolled back.
//
// The transaction is driven from a goroutine because ISession.Transaction owns the whole
// scope: it commits when its closure returns nil and rolls back when it returns an error,
// which does not fit "hand the transaction to the test and roll back when the test ends".
// The closure therefore parks until cleanup and then returns a sentinel error, so the
// rollback happens through the session's own machinery rather than around it.
func (d *Database) beginRollbackScope(t *testing.T) {
	t.Helper()

	ready := make(chan dbCore.IDBTransaction, 1)
	release := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		done <- d.session.Transaction(context.Background(), func(tx dbCore.IDBTransaction) error {
			ready <- tx
			<-release
			return errRollbackScope
		})
	}()

	select {
	case tx := <-ready:
		d.tx = tx
	case err := <-done:
		t.Fatalf("testsupport: could not begin the test transaction: %v", err)
	}

	t.Cleanup(func() {
		close(release)

		// The rollback is the point, so its sentinel is the expected outcome. Anything
		// else means the transaction did not unwind and the next test would inherit this
		// one's rows.
		if err := <-done; err != nil && !isRollbackScope(err) {
			t.Errorf("testsupport: rolling back the test transaction: %v", err)
		}
		d.tx = nil
	})
}

// Tx is the test's transaction under IsolateByRollback, or nil under IsolateByTruncation.
//
// Most tests want Session instead. This is for code that takes an IDBTransaction directly.
func (d *Database) Tx() dbCore.IDBTransaction { return d.tx }

// errRollbackScope is returned by the rollback-isolation closure to make the session roll
// back. It is not a failure.
var errRollbackScope = fmt.Errorf("testsupport: rolling back the test transaction")

func isRollbackScope(err error) bool {
	return err != nil && err.Error() == errRollbackScope.Error()
}

// migrateOnce runs the suite's migrations against this engine, at most once.
func (e *engine) migrateOnce(migrations []core.IMigration, migrateDown bool) {
	e.migrateOnceGuard.Do(func() {
		if err := e.migrate(migrations, migrateDown); err != nil {
			e.err = err
		}
	})
}
