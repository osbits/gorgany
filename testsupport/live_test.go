//go:build livedb

// The harness's own live verification. Behind the `livedb` build tag because it needs real
// servers, like the rest of the framework's live checks.
//
//	docker run --rm -d --name gorgany-pg -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=gorgany_test -p 5433:5432 postgres:16-alpine
//
//	docker run --rm -d --name gorgany-mysql -e MYSQL_ROOT_PASSWORD=test \
//	  -e MYSQL_DATABASE=gorgany_test -p 3307:3306 mysql:8 \
//	  --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci
//
// and SQL Server, whose command is in docs/TESTING.md, then:
//
//	go test -tags=livedb ./testsupport/ -count=1 -v
//
// The engines are found where the framework's live suite in e2e/tests finds them:
// E2E_PG_HOST, E2E_PG_PORT, E2E_MYSQL_HOST, E2E_MYSQL_PORT, E2E_MSSQL_HOST and E2E_MSSQL_PORT,
// else 127.0.0.1 and the harness's default port for the driver. An engine that is not running
// skips its own subtests.
//
// A harness nobody has run against a real engine is exactly the failure mode this whole
// brief is about: the MySQL driver shipped in v2 with a green suite while being unable to
// insert anything.
package testsupport

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	// The harness links only Postgres and MySQL, so this suite registers SQL Server itself, as
	// an app's suite does. It is a test file behind a build tag, so testsupport's own imports,
	// which every app's test binary links, stay free of the engine.
	_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------- fixtures

// widgetMigration creates two tables with a foreign key between them, because the
// interesting part of truncation is ordering.
type widgetMigration struct{}

func (widgetMigration) Name() string { return "testsupport_widgets" }

// Up creates the tables if they are missing. T-SQL has no CREATE TABLE IF NOT EXISTS, so on
// SQL Server each statement asks OBJECT_ID first; the columns are the same.
func (widgetMigration) Up() core.MigrationClosure {
	return func(db *gorm.DB) error {
		widgets := `CREATE TABLE IF NOT EXISTS ts_widgets (
			id INT PRIMARY KEY,
			label VARCHAR(64) NOT NULL
		)`
		notes := `CREATE TABLE IF NOT EXISTS ts_widget_notes (
			id INT PRIMARY KEY,
			widget_id INT NOT NULL,
			body VARCHAR(64) NOT NULL,
			CONSTRAINT fk_ts_note_widget FOREIGN KEY (widget_id) REFERENCES ts_widgets(id)
		)`
		if db.Dialector.Name() == "sqlserver" {
			widgets = `IF OBJECT_ID(N'ts_widgets', N'U') IS NULL CREATE TABLE ts_widgets (
				id INT PRIMARY KEY,
				label NVARCHAR(64) NOT NULL
			)`
			notes = `IF OBJECT_ID(N'ts_widget_notes', N'U') IS NULL CREATE TABLE ts_widget_notes (
				id INT PRIMARY KEY,
				widget_id INT NOT NULL,
				body NVARCHAR(64) NOT NULL,
				CONSTRAINT fk_ts_note_widget FOREIGN KEY (widget_id) REFERENCES ts_widgets(id)
			)`
		}

		if err := db.Exec(widgets).Error; err != nil {
			return err
		}
		return db.Exec(notes).Error
	}
}

func (widgetMigration) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		if err := db.Exec(`DROP TABLE IF EXISTS ts_widget_notes`).Error; err != nil {
			return err
		}
		return db.Exec(`DROP TABLE IF EXISTS ts_widgets`).Error
	}
}

// liveDatabase is where the live suite finds one engine: hostVariable and portVariable, the
// variables e2e/tests reads for it, so that one set of variables points both live suites at the
// same servers, else 127.0.0.1 and the harness's default port. The credentials are the harness's
// defaults for the driver, the ones the containers in docs/TESTING.md are started with.
func liveDatabase(t *testing.T, name, driver, hostVariable, portVariable string) DatabaseConfig {
	t.Helper()

	host := os.Getenv(hostVariable)
	if host == "" {
		host = "127.0.0.1"
	}
	port := defaultPortFor(driver)
	if raw := os.Getenv(portVariable); raw != "" {
		parsed, err := strconv.Atoi(raw)
		require.NoError(t, err, "%s=%q is not a port", portVariable, raw)
		port = parsed
	}

	return DatabaseConfig{
		Name: name, Driver: driver,
		Host: host, Port: port,
		User: defaultUserFor(driver), Password: defaultPasswordFor(driver), Database: "gorgany_test",
	}
}

func livePostgres(t *testing.T) DatabaseConfig {
	return liveDatabase(t, "postgres", DriverPostgres, "E2E_PG_HOST", "E2E_PG_PORT")
}

func liveMySQL(t *testing.T) DatabaseConfig {
	return liveDatabase(t, "mysql", DriverMySQL, "E2E_MYSQL_HOST", "E2E_MYSQL_PORT")
}

func liveSQLServer(t *testing.T) DatabaseConfig {
	return liveDatabase(t, "sqlserver", DriverSQLServer, "E2E_MSSQL_HOST", "E2E_MSSQL_PORT")
}

// liveHarness builds a harness over every engine, so EachDatabase covers them.
func liveHarness(t *testing.T, isolation Isolation) *Harness {
	t.Helper()

	return New(Config{
		Databases:   []DatabaseConfig{livePostgres(t), liveMySQL(t), liveSQLServer(t)},
		Isolation:   isolation,
		EngineWait:  10 * time.Second,
		MigrateDown: true,
	}).AddMigration(widgetMigration{})
}

// insertWidget writes a row through the handle the harness says to use.
func insertWidget(t *testing.T, db *Database, id int, label string) {
	t.Helper()
	db.Exec(t, "INSERT INTO ts_widgets (id, label) VALUES (?, ?)", id, label)
}

// ------------------------------------------------------------------- tests

// TestTheHarnessMigratesAndConnects is the baseline: an engine comes up, the migrations
// run, and the tables the harness will truncate are the ones the migrations created.
func TestTheHarnessMigratesAndConnects(t *testing.T) {
	liveHarness(t, IsolateByTruncation).EachDatabase(t, func(t *testing.T, db *Database) {
		require.NotNil(t, db.Gorm())
		require.NotNil(t, db.Session())

		assert.ElementsMatch(t, []string{"ts_widgets", "ts_widget_notes"}, db.Tables(),
			"the harness must know exactly what the migrations created")

		// And the schema is real, not merely reported.
		insertWidget(t, db, 1, "first")
		assert.Equal(t, 1, db.CountRows(t, "ts_widgets"))
	})
}

// TestTruncationIsolatesTests is the property the whole package exists for. Two tests
// writing the same primary key would collide if either leaked.
func TestTruncationIsolatesTests(t *testing.T) {
	harness := liveHarness(t, IsolateByTruncation)

	for _, round := range []string{"first", "second", "third"} {
		t.Run(round, func(t *testing.T) {
			harness.EachDatabase(t, func(t *testing.T, db *Database) {
				require.Equal(t, 0, db.CountRows(t, "ts_widgets"),
					"the previous round's rows must be gone")

				insertWidget(t, db, 1, round)
				require.Equal(t, 1, db.CountRows(t, "ts_widgets"))
			})
		})
	}
}

// TestTruncationRespectsForeignKeys. The migrations create the parent first, so deleting
// forwards hits the constraint — this is why truncation walks the tables in reverse.
func TestTruncationRespectsForeignKeys(t *testing.T) {
	harness := liveHarness(t, IsolateByTruncation)

	t.Run("write parent and child", func(t *testing.T) {
		harness.EachDatabase(t, func(t *testing.T, db *Database) {
			insertWidget(t, db, 1, "parent")
			db.Exec(t, "INSERT INTO ts_widget_notes (id, widget_id, body) VALUES (?, ?, ?)", 1, 1, "note")

			require.Equal(t, 1, db.CountRows(t, "ts_widget_notes"))
		})
	})

	t.Run("both are gone", func(t *testing.T) {
		harness.EachDatabase(t, func(t *testing.T, db *Database) {
			assert.Equal(t, 0, db.CountRows(t, "ts_widgets"))
			assert.Equal(t, 0, db.CountRows(t, "ts_widget_notes"))
		})
	})
}

// TestForeignKeyChecksAreRestoredAfterTruncation. MySQL truncation suspends them, and
// leaving them off would make every later test in the process pass without referential
// integrity.
func TestForeignKeyChecksAreRestoredAfterTruncation(t *testing.T) {
	harness := liveHarness(t, IsolateByTruncation)

	harness.EachDatabase(t, func(t *testing.T, db *Database) {
		db.Truncate(t)

		// A child row with no parent must still be refused.
		err := db.Gorm().Exec(
			"INSERT INTO ts_widget_notes (id, widget_id, body) VALUES (?, ?, ?)", 1, 999, "orphan").Error
		require.Error(t, err, "the foreign key must still be enforced after a truncate")
	})
}

// TestRollbackIsolatesTests covers the other strategy.
func TestRollbackIsolatesTests(t *testing.T) {
	harness := liveHarness(t, IsolateByRollback)

	for _, round := range []string{"first", "second"} {
		t.Run(round, func(t *testing.T) {
			harness.EachDatabase(t, func(t *testing.T, db *Database) {
				require.NotNil(t, db.Tx(), "rollback isolation must expose the transaction")

				require.Equal(t, 0, db.CountRows(t, "ts_widgets"),
					"the previous round must have rolled back")

				insertWidget(t, db, 1, round)
				require.Equal(t, 1, db.CountRows(t, "ts_widgets"),
					"the test must see its own uncommitted rows")
			})
		})
	}
}

// TestRollbackLeavesNothingCommitted checks from outside the transaction, which is the
// assertion that actually proves the rollback happened.
func TestRollbackLeavesNothingCommitted(t *testing.T) {
	harness := liveHarness(t, IsolateByRollback)

	var outside []*gorm.DB

	t.Run("write inside a rolled-back scope", func(t *testing.T) {
		harness.EachDatabase(t, func(t *testing.T, db *Database) {
			insertWidget(t, db, 42, "doomed")
			outside = append(outside, db.Gorm())
		})
	})

	// The subtest's cleanup has run by now, so the transaction is unwound.
	for _, handle := range outside {
		var count int64
		require.NoError(t, handle.Table("ts_widgets").Where("id = ?", 42).Count(&count).Error)
		assert.Equal(t, int64(0), count, "the rolled-back row must not be visible on a fresh connection")
	}
}

// TestTheDriverIsReportedCorrectly, since dialect-specific assertions depend on it.
func TestTheDriverIsReportedCorrectly(t *testing.T) {
	liveHarness(t, IsolateByTruncation).EachDatabase(t, func(t *testing.T, db *Database) {
		switch db.Label() {
		case "postgres":
			assert.True(t, db.IsPostgres())
			assert.False(t, db.IsMySQL())
			assert.False(t, db.IsSQLServer())
			assert.Equal(t, DriverPostgres, db.Driver())
		case "mysql":
			assert.True(t, db.IsMySQL())
			assert.False(t, db.IsPostgres())
			assert.False(t, db.IsSQLServer())
			assert.Equal(t, DriverMySQL, db.Driver())
		case "sqlserver":
			assert.True(t, db.IsSQLServer())
			assert.False(t, db.IsPostgres())
			assert.False(t, db.IsMySQL())
			assert.Equal(t, DriverSQLServer, db.Driver())
		default:
			t.Fatalf("unexpected label %q", db.Label())
		}
	})
}

// TestTheSessionIsUsable end to end, since Session is what a test is told to use.
func TestTheSessionIsUsable(t *testing.T) {
	liveHarness(t, IsolateByTruncation).EachDatabase(t, func(t *testing.T, db *Database) {
		session := db.Session()
		require.NotNil(t, session)

		result := session.Executor().ExecRaw(context.Background(),
			"INSERT INTO ts_widgets (id, label) VALUES (?, ?)", 7, "via-session")
		require.NoError(t, result.Error)

		assert.Equal(t, 1, db.CountRows(t, "ts_widgets"))
	})
}

// TestAnUnreachableEngineSkipsRatherThanHangs. A wrong port must cost the wait budget and
// then skip, not fail the whole package — and the message has to name the address.
func TestAnUnreachableEngineSkipsRatherThanHangs(t *testing.T) {
	harness := New(Config{
		Databases: []DatabaseConfig{{
			Name: "nowhere", Driver: DriverPostgres,
			Host: "127.0.0.1", Port: 1, // nothing listens here
			User: "postgres", Password: "test", Database: "gorgany_test",
		}},
		EngineWait: time.Second,
	})

	// Run it as a subtest so the skip does not stop this test.
	result := t.Run("unreachable", func(t *testing.T) {
		harness.EachDatabase(t, func(t *testing.T, db *Database) {
			t.Fatal("the body must not run for an unreachable engine")
		})
	})

	assert.True(t, result, "a skip is not a failure")
}

// TestMigrateDownClearsAPreviousSchema. An interrupted run leaves tables behind, and a
// migration that is not idempotent would then fail on the next run.
func TestMigrateDownClearsAPreviousSchema(t *testing.T) {
	// Two harnesses over the same engine: the second must not trip over the first's
	// tables. connect() caches per engine, so this shares the connection and re-runs the
	// migration path.
	first := liveHarness(t, IsolateByTruncation)
	first.EachDatabase(t, func(t *testing.T, db *Database) {
		insertWidget(t, db, 1, "before")
	})

	second := liveHarness(t, IsolateByTruncation)
	second.EachDatabase(t, func(t *testing.T, db *Database) {
		assert.Equal(t, 0, db.CountRows(t, "ts_widgets"))
	})
}

// TestKeepDataLeavesRowsBehind, for debugging a failure by hand.
func TestKeepDataLeavesRowsBehind(t *testing.T) {
	config := Config{
		Databases:  []DatabaseConfig{livePostgres(t)},
		Isolation:  IsolateByTruncation,
		EngineWait: 10 * time.Second,
		KeepData:   true,
	}

	harness := New(config).AddMigration(widgetMigration{})

	t.Run("write", func(t *testing.T) {
		db := harness.RequireDatabase(t)
		insertWidget(t, db, 99, "kept")
	})

	t.Run("still there", func(t *testing.T) {
		db := harness.RequireDatabase(t)
		assert.Equal(t, 1, db.CountRows(t, "ts_widgets"))

		// Clean up by hand, since KeepData means the harness will not.
		db.Exec(t, "DELETE FROM ts_widgets WHERE id = ?", 99)
	})
}

// TestConcurrentPreparationIsSafe: `go test` runs tests in one process and a suite may use
// t.Parallel, so connect and migrateOnce have to be race-free.
func TestConcurrentPreparationIsSafe(t *testing.T) {
	harness := liveHarness(t, IsolateByTruncation)

	// Prepare once serially so the migration has run, then hammer the cached path.
	harness.EachDatabase(t, func(t *testing.T, db *Database) {})

	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprintf("parallel-%d", i), func(t *testing.T) {
			t.Parallel()
			db := harness.RequireDatabase(t)
			require.NotNil(t, db.Gorm())
		})
	}
}

// ---------------------------------------------------------------- SQL Server

// identityMigration creates what SQL Server truncation treats specially: identities, which it
// reseeds, one of them seeded away from 1, and a view, which gorm lists with the tables and
// truncation must leave alone.
type identityMigration struct{}

func (identityMigration) Name() string { return "testsupport_identities" }

func (identityMigration) Up() core.MigrationClosure {
	return func(db *gorm.DB) error {
		for _, statement := range []string{
			`IF OBJECT_ID(N'ts_counters', N'U') IS NULL CREATE TABLE ts_counters (
				id INT IDENTITY(1, 1) PRIMARY KEY,
				label NVARCHAR(64) NOT NULL
			)`,
			`IF OBJECT_ID(N'ts_tickets', N'U') IS NULL CREATE TABLE ts_tickets (
				id BIGINT IDENTITY(100, 5) PRIMARY KEY,
				label NVARCHAR(64) NOT NULL
			)`,
			// CREATE VIEW must be alone in its batch, so it cannot sit behind an IF.
			`CREATE OR ALTER VIEW ts_counter_labels AS SELECT label FROM ts_counters`,
		} {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	}
}

func (identityMigration) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		for _, statement := range []string{
			`DROP VIEW IF EXISTS ts_counter_labels`,
			`DROP TABLE IF EXISTS ts_tickets`,
			`DROP TABLE IF EXISTS ts_counters`,
		} {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	}
}

// sqlServerHarness is a harness over SQL Server alone, in a database of its own, so that its
// migrations run: connect caches one engine per database, and migrates it once, with the
// migrations of whichever harness reaches it first.
func sqlServerHarness(t *testing.T, name, database string, migrations ...core.IMigration) (*Harness, DatabaseConfig) {
	t.Helper()

	config := liveSQLServer(t)
	config.Name = name
	config.Database = database
	return New(Config{
		Databases:   []DatabaseConfig{config},
		Isolation:   IsolateByTruncation,
		EngineWait:  10 * time.Second,
		MigrateDown: true,
	}).AddMigration(migrations...), config
}

// openSQLServerMaster connects to master on database's server, the way the harness does to
// create a database.
func openSQLServerMaster(database DatabaseConfig) (*gorm.DB, func() error, error) {
	parsed, err := dsconfig.Parse(sqlServerBootstrapConfig(database).datasourceConfig())
	if err != nil {
		return nil, nil, err
	}
	master, err := driver.New(parsed)
	if err != nil {
		return nil, nil, err
	}
	gormDb, err := gormOf(master)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (and closing: %v)", err, master.Close())
	}
	return gormDb, master.Close, nil
}

// requireSQLServerMaster connects to master on database's server, skipping the test when the
// server does not answer within the live suite's engine wait.
func requireSQLServerMaster(t *testing.T, database DatabaseConfig) *gorm.DB {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for {
		master, closeMaster, err := openSQLServerMaster(database)
		if err == nil {
			if err = master.Exec("SELECT 1").Error; err == nil {
				t.Cleanup(func() { assert.NoError(t, closeMaster()) })
				return master
			}
			assert.NoError(t, closeMaster())
		}
		if time.Now().After(deadline) {
			t.Skipf("SQL Server at %s:%d is not reachable: %v", database.Host, database.Port, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// dropSQLServerDatabase closes the harness's connection to database, then drops the database
// through master, so a test that makes one leaves nothing behind on a developer's server.
func dropSQLServerDatabase(t *testing.T, database DatabaseConfig) {
	t.Helper()

	enginesMu.Lock()
	for key, e := range engines {
		if e.config.Driver == database.Driver && e.config.Host == database.Host &&
			e.config.Port == database.Port && e.config.Database == database.Database {
			if e.datasource != nil {
				assert.NoError(t, e.datasource.Close())
			}
			delete(engines, key)
		}
	}
	enginesMu.Unlock()

	master, closeMaster, err := openSQLServerMaster(database)
	if !assert.NoError(t, err, "connecting to master to drop %s", database.Database) {
		return
	}
	defer func() { assert.NoError(t, closeMaster()) }()

	name := quoteSQLServerIdentifier(database.Database)
	assert.NoError(t, master.Exec("IF DB_ID("+sqlServerStringLiteral(database.Database)+") IS NOT NULL BEGIN "+
		"ALTER DATABASE "+name+" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE "+name+"; END").Error)
}

// readsCommittedSnapshots reports whether the database db is connected to has
// READ_COMMITTED_SNAPSHOT on.
func readsCommittedSnapshots(t *testing.T, db *Database) bool {
	t.Helper()

	var on bool
	require.NoError(t, db.Gorm().Raw(
		"SELECT is_read_committed_snapshot_on FROM sys.databases WHERE name = DB_NAME()").Row().Scan(&on))
	return on
}

// TestTheHarnessCreatesAMissingSQLServerDatabase. The SQL Server image cannot create a database
// from its environment, as the Postgres and MySQL images do, so without this every run would
// start with a manual CREATE DATABASE through master.
func TestTheHarnessCreatesAMissingSQLServerDatabase(t *testing.T) {
	harness, database := sqlServerHarness(t, "sqlserver-created",
		fmt.Sprintf("ts_created_%d_test", time.Now().UnixNano()), widgetMigration{})
	// Registered before the harness registers its own cleanup, so it runs after it: the
	// database outlives the test's last truncation.
	t.Cleanup(func() {
		if !t.Skipped() {
			dropSQLServerDatabase(t, database)
		}
	})

	db := harness.RequireDatabase(t)

	var name string
	require.NoError(t, db.Gorm().Raw("SELECT DB_NAME()").Row().Scan(&name))
	assert.Equal(t, database.Database, name)
	assert.ElementsMatch(t, []string{"ts_widgets", "ts_widget_notes"}, db.Tables(),
		"the migrations ran in the database the harness created")
	assert.True(t, readsCommittedSnapshots(t, db),
		"a database the harness creates reads committed snapshots, as every Azure SQL database does")
}

// TestTheHarnessLeavesAnExistingSQLServerDatabaseAlone: how a database somebody else created
// reads is theirs to decide, and switching it needs every other connection to it gone.
func TestTheHarnessLeavesAnExistingSQLServerDatabaseAlone(t *testing.T) {
	harness, database := sqlServerHarness(t, "sqlserver-existing",
		fmt.Sprintf("ts_existing_%d_test", time.Now().UnixNano()), widgetMigration{})

	master := requireSQLServerMaster(t, database)
	require.NoError(t, master.Exec("CREATE DATABASE "+quoteSQLServerIdentifier(database.Database)).Error)
	t.Cleanup(func() { dropSQLServerDatabase(t, database) })

	db := harness.RequireDatabase(t)
	assert.False(t, readsCommittedSnapshots(t, db), "the harness must not change an existing database")
}

// TestAMissingDatabaseTheHarnessWillNotCreateIsAConfigError: the harness creates a SQL Server
// database only when its name is letters, digits and underscores. For any other name it used to
// offer the name to CREATE DATABASE, which refused it, on every attempt until the engine wait
// ran out, and then report an engine that was not reachable, so RequireDatabase skipped. A
// server that answers and a database it does not have is a mistake in the settings, and fails
// at once, saying what to do.
func TestAMissingDatabaseTheHarnessWillNotCreateIsAConfigError(t *testing.T) {
	database := liveSQLServer(t)
	requireSQLServerMaster(t, database)
	database.Name = "sqlserver-uncreatable"
	database.Database = fmt.Sprintf("ts-missing-%d-test", time.Now().UnixNano())
	parsed, err := dsconfig.Parse(database.datasourceConfig())
	require.NoError(t, err)

	const wait = 20 * time.Second
	started := time.Now()
	datasource, err := waitForEngine(database, parsed, wait)

	require.Error(t, err)
	assert.Nil(t, datasource)
	assert.True(t, isConfigError(err), "a configError fails the test rather than skipping it: %v", err)
	assert.Contains(t, err.Error(), "does not exist")
	assert.Contains(t, err.Error(), "create it yourself")
	assert.Less(t, time.Since(started), wait/2, "no wait can create it, so none is spent")
}

// TestADatabaseTheHarnessWillNotCreateIsUsedOnceItExists: such a name still works, once somebody
// has created the database.
func TestADatabaseTheHarnessWillNotCreateIsUsedOnceItExists(t *testing.T) {
	database := liveSQLServer(t)
	master := requireSQLServerMaster(t, database)
	database.Name = "sqlserver-precreated"
	database.Database = fmt.Sprintf("ts-precreated-%d-test", time.Now().UnixNano())
	require.NoError(t, master.Exec("CREATE DATABASE "+quoteSQLServerIdentifier(database.Database)).Error)
	t.Cleanup(func() { dropSQLServerDatabase(t, database) })

	harness := New(Config{Databases: []DatabaseConfig{database}, EngineWait: 10 * time.Second}).
		AddMigration(widgetMigration{})
	db := harness.MustDatabase(t)

	var name string
	require.NoError(t, db.Gorm().Raw("SELECT DB_NAME()").Row().Scan(&name))
	assert.Equal(t, database.Database, name)
}

// TestSQLServerTruncation covers what SQL Server truncation does that the shared cases cannot
// see: identities, views, a truncation that fails part-way, and the trust of the foreign keys it
// switches back on.
func TestSQLServerTruncation(t *testing.T) {
	harness, database := sqlServerHarness(t, "sqlserver-truncation", "gorgany_truncation_test",
		widgetMigration{}, identityMigration{})
	t.Cleanup(func() {
		if !t.Skipped() {
			dropSQLServerDatabase(t, database)
		}
	})
	// Gates the subtests: they skip with the parent when SQL Server is not running.
	harness.RequireDatabase(t)

	lastID := func(t *testing.T, db *Database, table string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, db.Gorm().Raw("SELECT MAX(id) FROM "+quoteSQLServerIdentifier(table)).Row().Scan(&id))
		return id
	}

	t.Run("identities restart at their seed", func(t *testing.T) {
		db := harness.RequireDatabase(t)
		for _, label := range []string{"a", "b", "c"} {
			db.Exec(t, "INSERT INTO ts_counters (label) VALUES (?)", label)
			db.Exec(t, "INSERT INTO ts_tickets (label) VALUES (?)", label)
		}
		require.Equal(t, int64(3), lastID(t, db, "ts_counters"))
		require.Equal(t, int64(110), lastID(t, db, "ts_tickets"))

		db.Truncate(t)
		db.Exec(t, "INSERT INTO ts_counters (label) VALUES (?)", "after")
		db.Exec(t, "INSERT INTO ts_tickets (label) VALUES (?)", "after")
		assert.Equal(t, int64(1), lastID(t, db, "ts_counters"), "IDENTITY(1, 1) starts again at 1")
		assert.Equal(t, int64(100), lastID(t, db, "ts_tickets"), "IDENTITY(100, 5) starts again at 100, not 5")

		// Reseeded and not written to since, the identity must still start at its seed.
		db.Truncate(t)
		db.Exec(t, "INSERT INTO ts_counters (label) VALUES (?)", "again")
		assert.Equal(t, int64(1), lastID(t, db, "ts_counters"))
	})

	t.Run("an identity that never issued a value is not reseeded", func(t *testing.T) {
		db := harness.RequireDatabase(t)
		db.Exec(t, "DROP TABLE IF EXISTS ts_fresh")
		db.Exec(t, "CREATE TABLE ts_fresh (id INT IDENTITY(1, 1) PRIMARY KEY, label NVARCHAR(64) NOT NULL)")
		t.Cleanup(func() { assert.NoError(t, db.Gorm().Exec("DROP TABLE IF EXISTS ts_fresh").Error) })

		// Reseeding it to 0 would hand the next row 0: a table nobody has written to takes the
		// reseed value itself.
		db.Truncate(t, "ts_fresh")
		db.Exec(t, "INSERT INTO ts_fresh (label) VALUES (?)", "first")
		assert.Equal(t, int64(1), lastID(t, db, "ts_fresh"))
	})

	t.Run("a view among the migrated tables is left alone", func(t *testing.T) {
		db := harness.RequireDatabase(t)
		assert.Contains(t, db.Tables(), "ts_counter_labels", "gorm lists views with the tables on SQL Server")

		db.Exec(t, "INSERT INTO ts_counters (label) VALUES (?)", "seen")
		db.Truncate(t)
		assert.Equal(t, 0, db.CountRows(t, "ts_counters"))

		err := db.truncate("ts_counter_labels")
		require.Error(t, err, "a view named explicitly is not a table to empty")
		assert.Contains(t, err.Error(), "ts_counter_labels is not a table in this database")

		err = db.truncate("ts_no_such_table")
		require.Error(t, err, "nor is a typo, which Postgres and MySQL refuse too")
		assert.Contains(t, err.Error(), "ts_no_such_table is not a table in this database")
	})

	t.Run("constraints come back on, trusted, even when a delete fails", func(t *testing.T) {
		db := harness.RequireDatabase(t)
		insertWidget(t, db, 1, "parent")
		db.Exec(t, "INSERT INTO ts_widget_notes (id, widget_id, body) VALUES (?, ?, ?)", 1, 1, "note")

		foreignKey := func(t *testing.T) (disabled, untrusted bool) {
			t.Helper()
			require.NoError(t, db.Gorm().Raw("SELECT is_disabled, is_not_trusted FROM sys.foreign_keys "+
				"WHERE name = N'fk_ts_note_widget'").Row().Scan(&disabled, &untrusted))
			return disabled, untrusted
		}

		// The trigger makes deleting from ts_widget_notes fail after both tables' constraints
		// are off.
		db.Exec(t, "CREATE OR ALTER TRIGGER ts_notes_refuse_delete ON ts_widget_notes INSTEAD OF DELETE "+
			"AS THROW 50000, 'ts_widget_notes refuses deletes', 1")
		t.Cleanup(func() {
			assert.NoError(t, db.Gorm().Exec("DROP TRIGGER IF EXISTS ts_notes_refuse_delete").Error)
		})

		// Named tables are emptied last first, so here the child goes first and fails before
		// its parent loses a row. The restore then succeeds, and has to have run.
		err := db.truncate("ts_widgets", "ts_widget_notes")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DELETE FROM [ts_widget_notes]")
		assert.NotContains(t, err.Error(), "restoring constraints", "the restore itself succeeded")

		disabled, untrusted := foreignKey(t)
		assert.False(t, disabled, "the failed truncation must switch the foreign key back on")
		assert.False(t, untrusted, "WITH CHECK marks it trusted again")
		assert.Equal(t, 1, db.CountRows(t, "ts_widgets"), "nothing after the failure ran")

		// Here the parent goes first and its row is deleted, so the child's rows are orphans and
		// its foreign key cannot come back on WITH CHECK. That failure is reported beside the
		// first one, since the key stays off until a truncation restores it.
		err = db.truncate("ts_widget_notes", "ts_widgets")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DELETE FROM [ts_widget_notes]")
		assert.Contains(t, err.Error(), "restoring constraints, which stay off until this succeeds")
		assert.Contains(t, err.Error(), "fk_ts_note_widget")
		disabled, _ = foreignKey(t)
		assert.True(t, disabled, "the key the restore could not check stays off")

		// The next truncation that gets through restores it.
		require.NoError(t, db.Gorm().Exec("DROP TRIGGER ts_notes_refuse_delete").Error)
		db.Truncate(t)
		disabled, untrusted = foreignKey(t)
		assert.False(t, disabled)
		assert.False(t, untrusted)
		assert.Equal(t, 0, db.CountRows(t, "ts_widget_notes"))
	})
}

// TestConcurrentTruncationOnSQLServerIsSafe. Two truncations at once can deadlock on SQL Server,
// where restoring a child's foreign key and deleting from its parent take the two tables in
// opposite orders, so the harness runs one truncation at a time per engine; see
// truncateSQLServer. A suite may use t.Parallel, and each test truncates when it starts and
// when it ends.
func TestConcurrentTruncationOnSQLServerIsSafe(t *testing.T) {
	harness := New(Config{
		Databases:   []DatabaseConfig{liveSQLServer(t)},
		Isolation:   IsolateByTruncation,
		EngineWait:  10 * time.Second,
		MigrateDown: true,
	}).AddMigration(widgetMigration{})
	harness.RequireDatabase(t)

	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprintf("parallel-%d", i), func(t *testing.T) {
			t.Parallel()
			db := harness.RequireDatabase(t)
			for round := 0; round < 3; round++ {
				assert.NoError(t, db.truncate(), "round %d", round)
			}
		})
	}
}
