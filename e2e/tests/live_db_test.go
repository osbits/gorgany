//go:build livedb

// Package tests contains the live-engine verification for the Tier-1 datasource
// and migration fixes. It is behind the `livedb` build tag because it needs real
// servers; everything else in the framework's suite runs without one.
//
// Start the engines the brief prescribes:
//
//	docker run --rm -d --name gorgany-mysql -e MYSQL_ROOT_PASSWORD=test \
//	  -e MYSQL_DATABASE=gorgany_test -p 3307:3306 mysql:8 \
//	  --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci
//
//	docker run --rm -d --name gorgany-pg -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=gorgany_test -p 5433:5432 postgres:16-alpine
//
// Then:
//
//	go test -tags=livedb ./e2e/tests/ -count=1 -v
//
// The SQL Server cases, and the container they need, are in live_sqlserver_test.go.
//
// e2e/run.sh runs this same suite inside the compose network instead, where the engines
// answer on their service names rather than on host-published ports; see liveEngineHost
// for the variables that redirect it. It also sets E2E_REQUIRE_LIVE=1, which forbids the
// skips below — see requireLiveEnvVar in e2e_test.go.
package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/osbits/gorgany/v2/auth"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/osbits/gorgany/v2/app/core"
	dbCmd "github.com/osbits/gorgany/v2/command/db"
	"github.com/osbits/gorgany/v2/db"
	"github.com/osbits/gorgany/v2/db/migration"
	"github.com/osbits/gorgany/v2/db/orm"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	_ "github.com/osbits/gorgany/v2/db/sql/driver/builtin"
	mysqlv2 "github.com/osbits/gorgany/v2/db/sql/gorm/mysql/v2"
	pgv2 "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	"github.com/osbits/gorgany/v2/model"
	"github.com/osbits/gorgany/v2/provider"
	"github.com/osbits/gorgany/v2/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// liveEngineHost and liveEnginePort say where an engine listens.
//
// The defaults are the host-published ports in this file's doc comment, which is how these
// tests are run by hand against two `docker run` containers. The overrides exist for the
// dockerised harness: it runs this suite from a container on the compose network, where
// 127.0.0.1 is the test runner itself and the engines are reachable only under their
// service names on their standard ports. With the addresses hardcoded, the harness could
// never dial an engine at all, so every case here skipped no matter how the compose file
// was written — which is how "the MySQL live suite passes" came to mean "the MySQL live
// suite was never started".
func liveEngineHost(envKey, fallback string) string {
	if value := os.Getenv(envKey); value != "" {
		return value
	}
	return fallback
}

// liveEnginePort panics rather than falling back on a malformed value. Silently reverting
// to the default would point the suite at the wrong machine and report the resulting skips
// as an absent engine, hiding the operator's typo behind the very outcome these gates exist
// to make impossible.
func liveEnginePort(envKey string, fallback int) int {
	value := os.Getenv(envKey)
	if value == "" {
		return fallback
	}

	port, err := strconv.Atoi(value)
	if err != nil {
		panic(fmt.Sprintf("%s must be a TCP port number, got %q", envKey, value))
	}
	return port
}

func pgConfig() map[string]any {
	return map[string]any{
		"driver":   "postgres_gorm",
		"host":     liveEngineHost("E2E_PG_HOST", "127.0.0.1"),
		"port":     liveEnginePort("E2E_PG_PORT", 5433),
		"username": "postgres",
		"password": "test",
		"db":       "gorgany_test",
		"ssl":      "disable",
	}
}

func mysqlConfig() map[string]any {
	return map[string]any{
		"driver":   "mysql_gorm",
		"host":     liveEngineHost("E2E_MYSQL_HOST", "127.0.0.1"),
		"port":     liveEnginePort("E2E_MYSQL_PORT", 3307),
		"username": "root",
		"password": "test",
		"db":       "gorgany_test",
	}
}

// mysqlAddress renders the MySQL endpoint for messages, so a skip or a failure names the
// address that was actually tried instead of the one in the doc comment.
func mysqlAddress() string {
	cfg := mysqlConfig()
	return fmt.Sprintf("%v:%v", cfg["host"], cfg["port"])
}

// secondPgDatabase is the database name used for the second Postgres datasource,
// so the two-datasource checks can run on Postgres alone.
const secondPgDatabase = "gorgany_test_two"

func secondPgConfig() map[string]any {
	cfg := pgConfig()
	cfg["db"] = secondPgDatabase
	return cfg
}

// ensureSecondPgDatabase creates the second database if it does not exist.
func ensureSecondPgDatabase(t *testing.T) {
	t.Helper()

	admin := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	defer admin.Close()

	var exists bool
	require.NoError(t, gormOf(t, admin).
		Raw(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = ?)`, secondPgDatabase).
		Scan(&exists).Error)

	if !exists {
		// CREATE DATABASE cannot run inside a transaction block.
		require.NoError(t, gormOf(t, admin).Exec(`CREATE DATABASE `+secondPgDatabase).Error)
	}
}

// mysqlAvailable reports whether the MySQL engine is reachable, so the
// cross-engine tests can skip cleanly rather than fail on an absent server.
func mysqlAvailable() bool {
	ds, err := mysqlv2.NewDataSource(mysqlConfig())
	if err != nil {
		return false
	}
	_ = ds.Close()
	return true
}

// secondaryConfig returns the config for the second datasource: MySQL when it is
// reachable — which is what proves the cross-engine half of T1.1 — and otherwise a
// second Postgres database, which still proves the routing itself.
func secondaryConfig(t *testing.T) (cfg map[string]any, dialect string) {
	t.Helper()

	if mysqlAvailable() {
		return mysqlConfig(), "mysql"
	}

	// The fallback is a convenience for a developer who has only Postgres running, and it
	// must not survive into a run that will be read as proof of cross-engine coverage: two
	// Postgres databases exercise the routing but never put a MySQL statement on a MySQL
	// connection, which is the half of this check that matters.
	if liveRunIsRequired() {
		t.Fatalf("%s is set, so the cross-engine case must run against MySQL, but MySQL is not reachable on %s",
			requireLiveEnvVar, mysqlAddress())
	}

	t.Log("MySQL not reachable; using a second Postgres database. " +
		"This still proves datasource routing, but not the cross-engine case.")
	ensureSecondPgDatabase(t)
	return secondPgConfig(), "postgres"
}

// secondaryDataSource opens the datasource cfg describes through the gate of its engine: a
// SQL Server one through gateSQLServer, whose switch is E2E_REQUIRE_SQLSERVER rather than
// E2E_REQUIRE_LIVE.
func secondaryDataSource(t *testing.T, cfg map[string]any) dbCore.IDataSource {
	t.Helper()

	switch cfg["driver"] {
	case "mysql_gorm":
		return waitForDatasource(t, func() (dbCore.IDataSource, error) {
			return mysqlv2.NewDataSource(cfg)
		})
	case "sqlserver_gorm":
		return gateSQLServer(t, engineWait, func() (dbCore.IDataSource, error) {
			return openMSSQL(cfg)
		})
	}
	return waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(cfg)
	})
}

// engineWait is how long to keep retrying a connection. It is short because an
// engine that is up answers on the first attempt; the retries exist only to absorb
// a container that is still initialising. Nothing here should sit for a minute
// discovering that a server is absent.
const engineWait = 10 * time.Second

// waitForDatasource retries until the engine accepts connections, then skips — or fails,
// when the caller demanded a real run.
//
// Close what it returns with t.Cleanup, registered before any cleanup that uses the
// connection — never with defer. Deferred calls run when the test function returns, which
// is before any cleanup, so a deferred Close shut the handle the drop-table cleanups then
// ran through: every drop failed, nothing checked, and the tables outlived the run. The
// suite still passed, because each test also drops at its start. Cleanups run last-in,
// first-out, so a Close registered first runs last.
func waitForDatasource(t *testing.T, build func() (dbCore.IDataSource, error)) dbCore.IDataSource {
	t.Helper()

	deadline := time.Now().Add(engineWait)
	var lastErr error
	for {
		ds, err := build()
		if err == nil {
			recordLiveCase()
			return ds
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	skipUnlessLiveRequired(t, "engine not reachable within %s: %v", engineWait, lastErr)
	return nil
}

// requireMySQL skips immediately when MySQL is absent, so a run without it costs
// one failed dial rather than the full retry window per test.
func requireMySQL(t *testing.T) {
	t.Helper()

	if !mysqlAvailable() {
		skipUnlessLiveRequired(t,
			"MySQL not reachable on %s; start the container from this file's doc comment", mysqlAddress())
	}
}

// --------------------------------------------------------- T1.3: no more panics

// TestT13_MissingLogKeyReturnsAnErrorNotAPanic is the brief's second live check:
// "NewDataSource with a config map missing `log` returns an error instead of
// panicking". The map below is missing `log`, `prefer_simple_protocol` and
// `properties` — all three of which used to be unchecked type assertions.
func TestT13_MissingLogKeyReturnsAnErrorNotAPanic(t *testing.T) {
	// Reachability is settled through the shared gate before anything is asserted. This
	// case used to dial the engine directly inside require.NoError, so on a machine with no
	// Postgres it did not skip like its neighbours: it failed, reporting "omitting `log`
	// must not be an error" for what was really a refused connection. That both slandered
	// working code and left the tagged suite unable to go green without engines, so the one
	// signal worth having here — an engine is up and the config was still rejected — was
	// buried under a failure that said nothing of the kind.
	waitForDatasource(t, func() (dbCore.IDataSource, error) { return pgv2.NewDataSource(pgConfig()) }).Close()

	// A valid config missing the optional keys must simply work.
	require.NotPanics(t, func() {
		ds, err := pgv2.NewDataSource(pgConfig())
		require.NoError(t, err, "omitting `log` must not be an error")
		require.NotNil(t, ds)
		_ = ds.Close()
	})

	// A *mistyped* key must be a descriptive error naming the key, still no panic.
	bad := pgConfig()
	bad["log"] = "definitely"

	var err error
	require.NotPanics(t, func() { _, err = pgv2.NewDataSource(bad) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'log'")
	assert.Contains(t, err.Error(), "boolean")
	t.Logf("mistyped `log` reported as: %v", err)
}

// ------------------------------------------------- T1.1/T1.2: two datasources

// TestT11_TwoDatasourcesBothResolveByNameOnTenBoots is the brief's first live
// check. The pre-v2 constructor returned from inside its loop over the
// `databases` map, so two configured databases registered exactly one — and
// because Go randomises map iteration order, which one changed per boot. One boot
// proves nothing, so this runs ten.
func TestT11_TwoDatasourcesBothResolveByNameOnTenBoots(t *testing.T) {
	waitForDatasource(t, func() (dbCore.IDataSource, error) { return pgv2.NewDataSource(pgConfig()) }).Close()

	secondary, secondaryDialect := secondaryConfig(t)
	secondaryDataSource(t, secondary).Close()

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{
		"default": pgConfig(),
		"creatio": secondary,
	})
	t.Cleanup(func() { viper.Set("databases", previous) })

	for boot := 0; boot < 10; boot++ {
		t.Run(fmt.Sprintf("boot-%d", boot), func(t *testing.T) {
			c := service.NewContainer()
			p := provider.NewDbProvider()
			p.Register(c)

			var dbContext core.IDBContext
			require.NoError(t, c.Make(&dbContext))

			// Both must be present, on every boot.
			defaultDs := dbContext.GetDataSource("default")
			creatioDs := dbContext.GetDataSource("creatio")
			require.NotNil(t, defaultDs, "the `default` datasource must be registered")
			require.NotNil(t, creatioDs, "the `creatio` datasource must be registered")

			// And each must be the engine it was configured as. This is the part
			// that used to go wrong silently: a Postgres statement fired at a MySQL
			// connection.
			assertDialect(t, defaultDs, "postgres")
			assertDialect(t, creatioDs, secondaryDialect)

			// The two must be distinct connections, not the same one twice.
			assert.NotSame(t, defaultDs, creatioDs)
			assertDatabaseName(t, defaultDs, "gorgany_test")
			assertDatabaseName(t, creatioDs, secondary["db"].(string))

			// T1.2: the unnamed transient must be `default`, deterministically.
			var session dbCore.ISession
			require.NoError(t, c.Make(&session))
			assert.Equal(t, "postgres", session.Query().Dialect().Name(),
				"the unnamed ISession must always resolve to `default`")

			require.NoError(t, defaultDs.Close())
			require.NoError(t, creatioDs.Close())
		})
	}
}

// assertDatabaseName confirms which database the connection actually landed in,
// which is the observable that made the pre-v2 nondeterminism dangerous.
//
// The query itself is per-engine: Postgres spells it current_database(), MySQL
// spells it DATABASE(), and SQL Server DB_NAME().
func assertDatabaseName(t *testing.T, ds dbCore.IDataSource, want string) {
	t.Helper()

	query := `SELECT current_database()`
	if aware, ok := ds.(interface{ Dialect() dbCore.SQLDialect }); ok {
		switch aware.Dialect().Name() {
		case "mysql":
			query = `SELECT DATABASE()`
		case "sqlserver":
			query = `SELECT DB_NAME()`
		}
	}

	var got string
	require.NoError(t, gormOf(t, ds).Raw(query).Scan(&got).Error)
	assert.Equal(t, want, got)
}

func assertDialect(t *testing.T, ds dbCore.IDataSource, want string) {
	t.Helper()

	aware, ok := ds.(interface{ Dialect() dbCore.SQLDialect })
	require.True(t, ok, "datasource must expose its dialect")
	assert.Equal(t, want, aware.Dialect().Name())
}

// ----------------------------------------------------- T1.4: search_path works

// TestT14_SearchPathConnectsIntoThatSchema is the brief's third live check. Before
// v2 the DSN builder had no way to express search_path, which made
// schema-per-test isolation impossible.
func TestT14_SearchPathConnectsIntoThatSchema(t *testing.T) {
	admin := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { admin.Close() })

	adminGorm := gormOf(t, admin)
	require.NoError(t, adminGorm.Exec(`DROP SCHEMA IF EXISTS tenant_a CASCADE`).Error)
	require.NoError(t, adminGorm.Exec(`CREATE SCHEMA tenant_a`).Error)
	t.Cleanup(func() { assert.NoError(t, adminGorm.Exec(`DROP SCHEMA IF EXISTS tenant_a CASCADE`).Error) })

	// A datasource pointed at that schema.
	cfg, err := dsconfig.Parse(pgConfig())
	require.NoError(t, err)
	cfg.SearchPath = "tenant_a"

	dsn, err := pgv2.BuildDSN(cfg)
	require.NoError(t, err)
	require.Contains(t, dsn, "search_path=tenant_a")
	t.Logf("DSN: %s", dsn)

	scoped, err := pgv2.NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	defer scoped.Close()

	scopedGorm := gormOf(t, scoped)

	// The connection reports the schema it landed in.
	var currentSchema string
	require.NoError(t, scopedGorm.Raw(`SELECT current_schema()`).Scan(&currentSchema).Error)
	assert.Equal(t, "tenant_a", currentSchema,
		"the connection must start inside the configured schema")

	// An unqualified CREATE lands in that schema, which is the whole point.
	require.NoError(t, scopedGorm.Exec(`CREATE TABLE isolated (id int)`).Error)

	var schemaOfTable string
	require.NoError(t, adminGorm.Raw(
		`SELECT table_schema FROM information_schema.tables WHERE table_name = 'isolated'`,
	).Scan(&schemaOfTable).Error)
	assert.Equal(t, "tenant_a", schemaOfTable)
}

// ----------------------------------------- T1.5: sessions migration on MySQL

// TestT15_SessionsMigrationOnMySQL is the brief's fourth live check: `db:migrate
// up` creates `sessions` on MySQL, both indexes are present, and a second run
// applies nothing. The pre-v2 migration used CREATE INDEX IF NOT EXISTS, which
// MySQL has no syntax for, and passed three statements to one Exec, which
// go-sql-driver/mysql rejects.
func TestT15_SessionsMigrationOnMySQL(t *testing.T) {
	requireMySQL(t)

	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(mysqlConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS sessions`).Error)
	t.Cleanup(func() { assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS sessions`).Error) })

	m := migration.NewSessionsMigration()

	// First run creates the table and both indexes.
	require.NoError(t, m.Up()(gormDb), "the sessions migration must run on MySQL")

	assert.True(t, gormDb.Migrator().HasTable("sessions"))
	for _, index := range []string{migration.SessionsExpiryIndex, migration.SessionsUserIDIndex} {
		assert.Truef(t, gormDb.Migrator().HasIndex(migration.SessionsTableModel(), index),
			"index %s must exist", index)
	}

	// Second run applies nothing and does not error.
	before := describeTable(t, gormDb, "sessions")
	require.NoError(t, m.Up()(gormDb), "a second run must be a no-op, not an error")
	assert.Equal(t, before, describeTable(t, gormDb, "sessions"),
		"a second run must not change the schema")

	// The table is actually usable, which the DDL-only assertions do not prove.
	require.NoError(t, gormDb.Exec(
		`INSERT INTO sessions (id, user_id, expiry, created_at, last_activity) VALUES (?, ?, ?, ?, ?)`,
		"s1", "u1", time.Now().Add(time.Hour), time.Now(), time.Now(),
	).Error)

	var count int64
	require.NoError(t, gormDb.Raw(`SELECT COUNT(*) FROM sessions`).Scan(&count).Error)
	assert.Equal(t, int64(1), count)

	// And Down() drops it, on MySQL.
	require.NoError(t, m.Down()(gormDb))
	assert.False(t, gormDb.Migrator().HasTable("sessions"))
}

// TestT15_SessionsMigrationOnPostgres is the same round trip on Postgres, so the
// portable rewrite is shown not to have regressed the engine it already worked on.
func TestT15_SessionsMigrationOnPostgres(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS sessions CASCADE`).Error)
	t.Cleanup(func() { assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS sessions CASCADE`).Error) })

	m := migration.NewSessionsMigration()

	require.NoError(t, m.Up()(gormDb))
	assert.True(t, gormDb.Migrator().HasTable("sessions"))
	for _, index := range []string{migration.SessionsExpiryIndex, migration.SessionsUserIDIndex} {
		assert.Truef(t, gormDb.Migrator().HasIndex(migration.SessionsTableModel(), index),
			"index %s must exist", index)
	}

	require.NoError(t, m.Up()(gormDb), "a second run must be a no-op")

	require.NoError(t, m.Down()(gormDb))
	assert.False(t, gormDb.Migrator().HasTable("sessions"))
}

// ------------------------------------------- T1.6: --datasource targets one db

// TestT16_MigrateTargetsOnlyTheSelectedDatasource is the brief's fifth live
// check: `db:migrate up --datasource=creatio` runs against the second connection
// and not the first. Both engines are real, so a migration landing on the wrong
// one is visible as a table in the wrong database.
func TestT16_MigrateTargetsOnlyTheSelectedDatasource(t *testing.T) {
	pg := waitForDatasource(t, func() (dbCore.IDataSource, error) { return pgv2.NewDataSource(pgConfig()) })
	t.Cleanup(func() { pg.Close() })

	secondary, _ := secondaryConfig(t)
	my := secondaryDataSource(t, secondary)
	t.Cleanup(func() { my.Close() })

	pgGorm := gormOf(t, pg)
	myGorm := gormOf(t, my)

	for _, g := range []*gorm.DB{pgGorm, myGorm} {
		require.NoError(t, g.Exec(`DROP TABLE IF EXISTS creatio_only`).Error)
		require.NoError(t, g.Exec(`DROP TABLE IF EXISTS default_only`).Error)
		require.NoError(t, g.Migrator().DropTable(&db.Migration{}))
	}
	t.Cleanup(func() {
		for _, g := range []*gorm.DB{pgGorm, myGorm} {
			assert.NoError(t, g.Exec(`DROP TABLE IF EXISTS creatio_only`).Error)
			assert.NoError(t, g.Exec(`DROP TABLE IF EXISTS default_only`).Error)
			assert.NoError(t, g.Migrator().DropTable(&db.Migration{}))
		}
	})

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{
		"default": pgConfig(),
		"creatio": secondary,
	})
	t.Cleanup(func() { viper.Set("databases", previous) })

	// Run `db:migrate up --datasource=creatio`.
	runMigrateUp(t, "creatio",
		scopedMigration{name: "creatio_table", target: "creatio", table: "creatio_only"},
		scopedMigration{name: "default_table", target: "default", table: "default_only"},
	)

	assert.True(t, myGorm.Migrator().HasTable("creatio_only"),
		"the creatio-scoped migration must have run on MySQL")
	assert.False(t, myGorm.Migrator().HasTable("default_only"),
		"the default-scoped migration must NOT have run on MySQL")
	assert.False(t, pgGorm.Migrator().HasTable("creatio_only"),
		"nothing must have been applied to Postgres")
	assert.False(t, pgGorm.Migrator().HasTable("default_only"),
		"the default-scoped migration must not run when creatio is selected")

	// Now the default run.
	runMigrateUp(t, "default",
		scopedMigration{name: "creatio_table", target: "creatio", table: "creatio_only"},
		scopedMigration{name: "default_table", target: "default", table: "default_only"},
	)

	assert.True(t, pgGorm.Migrator().HasTable("default_only"),
		"the default-scoped migration must have run on Postgres")
	assert.False(t, pgGorm.Migrator().HasTable("creatio_only"),
		"the creatio-scoped migration must never touch Postgres")
}

// ---------------------------------------------------------------- MySQL usable

// TestMySQLQueriesRoundTripThroughTheDialect proves the MySQL dialect's SQL is
// accepted by a real MySQL 8, not merely the string the unit tests expect.
func TestMySQLQueriesRoundTripThroughTheDialect(t *testing.T) {
	requireMySQL(t)

	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(mysqlConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS dialect_probe`).Error)
	require.NoError(t, gormDb.Exec(
		"CREATE TABLE dialect_probe (id INT PRIMARY KEY, region VARCHAR(50), name VARCHAR(50), amount INT)").Error)
	t.Cleanup(func() { assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS dialect_probe`).Error) })

	session, err := ds.NewSession()
	require.NoError(t, err)
	defer session.Close()

	// ON CONFLICT ... DO UPDATE is refused by the default dialect (item D): the
	// translation to ON DUPLICATE KEY UPDATE drops the conflict target, and MySQL keys
	// off any unique index instead. Asserted here as well as in the unit tests, because
	// a refusal that only exists as an expected string is a refusal nobody has seen.
	refused := session.Query().
		Insert("dialect_probe").
		Columns("id", "region", "name", "amount").
		Values(1, "north", "ann", 10).
		OnConflict("id").
		DoUpdate(map[string]interface{}{"amount": 20})

	_, _, refusedErr := refused.ToSQL()
	require.Error(t, refusedErr, "the default MySQL dialect must refuse DO UPDATE")
	assert.True(t, dbCore.IsUnsupported(refusedErr))

	// With the opt-in, the SQL it produces has to be accepted by a real MySQL 8 — which
	// is the half a string assertion cannot prove.
	optedIn := mysqlv2.NewBuilderWithDialect(&mysqlv2.MySQLDialect{AllowUnfaithfulUpsert: true}).
		Insert("dialect_probe").
		Columns("id", "region", "name", "amount").
		Values(1, "north", "ann", 10).
		OnConflict("id").
		DoUpdate(map[string]interface{}{"amount": 20})

	result := session.Executor().Exec(ctxBackground(), optedIn)
	require.NoError(t, result.Error, "the opted-in upsert must be valid MySQL")

	// The same statement again exercises the ON DUPLICATE KEY branch.
	require.NoError(t, session.Executor().Exec(ctxBackground(), optedIn).Error)

	var amount int
	require.NoError(t, gormDb.Raw(`SELECT amount FROM dialect_probe WHERE id = 1`).Scan(&amount).Error)
	assert.Equal(t, 20, amount, "the second insert must have taken the UPDATE branch")

	// DO NOTHING is faithful and needs no opt-in, so it must still work through the
	// session's own dialect.
	doNothing := session.Query().
		Insert("dialect_probe").
		Columns("id", "region", "name", "amount").
		Values(1, "north", "ann", 99).
		OnConflict("id").
		DoNothing()

	require.NoError(t, session.Executor().Exec(ctxBackground(), doNothing).Error,
		"ON CONFLICT DO NOTHING is unaffected by the DO UPDATE refusal")

	require.NoError(t, gormDb.Raw(`SELECT amount FROM dialect_probe WHERE id = 1`).Scan(&amount).Error)
	assert.Equal(t, 20, amount, "DO NOTHING must not have overwritten the row")

	// ROLLUP in its MySQL position, under ONLY_FULL_GROUP_BY.
	rollup := session.Query().
		Select("region", "SUM(amount) AS total").
		From("dialect_probe").
		Rollup("region")

	sql, _, err := rollup.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "WITH ROLLUP")
	t.Logf("ROLLUP SQL: %s", sql)

	var rows []struct {
		Region *string
		Total  int
	}
	require.NoError(t, gormDb.Raw(sql).Scan(&rows).Error,
		"GROUP BY ... WITH ROLLUP must be accepted by MySQL 8 under ONLY_FULL_GROUP_BY")
	assert.NotEmpty(t, rows)

	// A bare OFFSET, which MySQL rejects without a LIMIT.
	offsetOnly := session.Query().Select("id").From("dialect_probe").Offset(0)
	sql, _, err = offsetOnly.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "LIMIT 18446744073709551615")
	require.NoError(t, gormDb.Raw(sql).Scan(&[]int{}).Error,
		"the synthesised LIMIT must make a bare OFFSET legal")

	// And a construct MySQL cannot express fails before touching the server.
	_, _, err = session.Query().
		Insert("dialect_probe").Columns("id").Values(2).Returning("id").ToSQL()
	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))
	t.Logf("RETURNING refused as: %v", err)
}

// TestUtf8mb4RoundTrip proves the charset default actually holds: a 4-byte
// character survives, which it would not under MySQL's `utf8` (3-byte) alias.
func TestUtf8mb4RoundTrip(t *testing.T) {
	requireMySQL(t)

	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(mysqlConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS charset_probe`).Error)
	require.NoError(t, gormDb.Exec(`CREATE TABLE charset_probe (id INT PRIMARY KEY, note VARCHAR(50))`).Error)
	t.Cleanup(func() { assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS charset_probe`).Error) })

	const fourByte = "hello 🌍"
	require.NoError(t, gormDb.Exec(`INSERT INTO charset_probe VALUES (1, ?)`, fourByte).Error)

	var note string
	require.NoError(t, gormDb.Raw(`SELECT note FROM charset_probe WHERE id = 1`).Scan(&note).Error)
	assert.Equal(t, fourByte, note, "utf8mb4 must survive the round trip")
}

// ------------------------------------------------------------------- helpers

func gormOf(t *testing.T, ds dbCore.IDataSource) *gorm.DB {
	t.Helper()

	raw, err := ds.GetDriver()
	require.NoError(t, err)

	gormDb, ok := raw.(*gorm.DB)
	require.True(t, ok, "expected a *gorm.DB driver")
	return gormDb
}

func describeTable(t *testing.T, gormDb *gorm.DB, table string) []string {
	t.Helper()

	columns, err := gormDb.Migrator().ColumnTypes(table)
	require.NoError(t, err)

	described := make([]string, 0, len(columns))
	for _, c := range columns {
		described = append(described, fmt.Sprintf("%s:%s", c.Name(), c.DatabaseTypeName()))
	}
	return described
}

// runMigrateUp invokes the real db:migrate command with the given --datasource,
// driving it through the container exactly as ConsoleApp does.
//
// The command's dependencies are unexported struct fields tagged
// `container:"inject"`, so the container fills them; that is the same path the
// framework uses, rather than a reimplementation of the command's logic.
func runMigrateUp(t *testing.T, datasource string, migrations ...core.IMigration) {
	t.Helper()
	runMigrate(t, []string{"cli", "db:migrate", "up", "--datasource=" + datasource}, migrations...)
}

// runMigrateDown invokes `db:migrate down --datasource=... --steps=n` the same way.
func runMigrateDown(t *testing.T, datasource string, steps int, migrations ...core.IMigration) {
	t.Helper()
	runMigrate(t, []string{
		"cli", "db:migrate", "down",
		"--datasource=" + datasource,
		fmt.Sprintf("--steps=%d", steps),
	}, migrations...)
}

func runMigrate(t *testing.T, args []string, migrations ...core.IMigration) {
	t.Helper()

	previousArgs := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = previousArgs })

	c := service.NewContainer()
	dbProvider := provider.NewDbProvider()
	dbProvider.Register(c)

	var dataContext core.IDataContext
	require.NoError(t, c.Make(&dataContext))
	for _, m := range migrations {
		dataContext.AddMigration(m)
	}

	cmd := &dbCmd.MigrateCommand{}
	require.NoError(t, c.Make(cmd))

	require.NotPanics(t, func() { cmd.Execute(context.Background()) },
		"%v must succeed", args[1:])
}

func ctxBackground() context.Context { return context.Background() }

// scopedMigration is a test migration that declares its target datasource and
// creates one table, so which database it landed in is directly observable.
type scopedMigration struct {
	name   string
	target string
	table  string
}

func (m scopedMigration) Name() string           { return m.name }
func (m scopedMigration) DataSourceName() string { return m.target }

func (m scopedMigration) Up() core.MigrationClosure {
	return func(g *gorm.DB) error {
		return g.Exec(fmt.Sprintf("CREATE TABLE %s (id INT)", m.table)).Error
	}
}

func (m scopedMigration) Down() core.MigrationClosure {
	return func(g *gorm.DB) error {
		return g.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", m.table)).Error
	}
}

// ------------------------------------------------------- T1.7: down really works

// TestT17_MigrateDownActuallyRollsBack is the check the brief's own verification
// block does not list but T1.7 demands. `down` used to be
// `func (thiz MigrateCommand) down() {}` — it reported success and did nothing — so
// a test that only asserts the command exits cleanly would have passed against the
// stub. This asserts the observable effects instead: the schema reverts and the
// bookkeeping row disappears.
func TestT17_MigrateDownActuallyRollsBack(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	reset := func() {
		assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS step_one`).Error)
		assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS step_two`).Error)
		assert.NoError(t, gormDb.Migrator().DropTable(&db.Migration{}))
	}
	reset()
	t.Cleanup(reset)

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{"default": pgConfig()})
	t.Cleanup(func() { viper.Set("databases", previous) })

	one := scopedMigration{name: "step_one", target: "default", table: "step_one"}
	two := scopedMigration{name: "step_two", target: "default", table: "step_two"}

	runMigrateUp(t, "default", one, two)
	require.True(t, gormDb.Migrator().HasTable("step_one"))
	require.True(t, gormDb.Migrator().HasTable("step_two"))
	require.Equal(t, int64(2), appliedCount(t, gormDb))

	// One step rolls back only the most recently applied migration.
	runMigrateDown(t, "default", 1, one, two)

	assert.True(t, gormDb.Migrator().HasTable("step_one"),
		"a single step must not roll back more than one migration")
	assert.False(t, gormDb.Migrator().HasTable("step_two"),
		"the most recent migration's Down() must actually have run")
	assert.Equal(t, int64(1), appliedCount(t, gormDb),
		"the bookkeeping row must be gone, not just the table")
	assert.False(t, isApplied(t, gormDb, "step_two"))
	assert.True(t, isApplied(t, gormDb, "step_one"))

	// Rolling the last one back leaves nothing.
	runMigrateDown(t, "default", 1, one, two)
	assert.False(t, gormDb.Migrator().HasTable("step_one"))
	assert.Equal(t, int64(0), appliedCount(t, gormDb))

	// And `down` on an empty history is a clean no-op, not an error.
	require.NotPanics(t, func() { runMigrateDown(t, "default", 1, one, two) })

	// Re-running `up` after a rollback re-applies, proving the bookkeeping row was
	// genuinely cleared rather than the table merely dropped.
	runMigrateUp(t, "default", one, two)
	assert.True(t, gormDb.Migrator().HasTable("step_one"))
	assert.True(t, gormDb.Migrator().HasTable("step_two"))
	assert.Equal(t, int64(2), appliedCount(t, gormDb))
}

// TestT17_StepsRollsBackSeveralAtOnce covers --steps=n.
func TestT17_StepsRollsBackSeveralAtOnce(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	reset := func() {
		assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS step_one`).Error)
		assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS step_two`).Error)
		assert.NoError(t, gormDb.Migrator().DropTable(&db.Migration{}))
	}
	reset()
	t.Cleanup(reset)

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{"default": pgConfig()})
	t.Cleanup(func() { viper.Set("databases", previous) })

	one := scopedMigration{name: "step_one", target: "default", table: "step_one"}
	two := scopedMigration{name: "step_two", target: "default", table: "step_two"}

	runMigrateUp(t, "default", one, two)
	runMigrateDown(t, "default", 2, one, two)

	assert.False(t, gormDb.Migrator().HasTable("step_one"))
	assert.False(t, gormDb.Migrator().HasTable("step_two"))
	assert.Equal(t, int64(0), appliedCount(t, gormDb))
}

// TestT17_RollbackIsTransactional proves the schema change and the bookkeeping row
// move together. A migration whose Down() fails must leave both untouched, so the
// two can never disagree.
func TestT17_RollbackIsTransactional(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	reset := func() {
		assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS step_one`).Error)
		assert.NoError(t, gormDb.Migrator().DropTable(&db.Migration{}))
	}
	reset()
	t.Cleanup(reset)

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{"default": pgConfig()})
	t.Cleanup(func() { viper.Set("databases", previous) })

	good := scopedMigration{name: "step_one", target: "default", table: "step_one"}
	runMigrateUp(t, "default", good)
	require.Equal(t, int64(1), appliedCount(t, gormDb))

	// Same name, but a Down() that fails.
	broken := failingDownMigration{name: "step_one", target: "default"}

	// The command calls os.Exit(1) on a failed rollback, so drive down() through the
	// migration directly rather than the command, and assert the invariant: a failed
	// Down() leaves the bookkeeping row in place.
	tx := gormDb.Begin()
	require.Error(t, broken.Down()(tx))
	tx.Rollback()

	assert.Equal(t, int64(1), appliedCount(t, gormDb),
		"a failed Down() must leave the bookkeeping row intact")
	assert.True(t, gormDb.Migrator().HasTable("step_one"),
		"and must leave the schema intact")
}

// failingDownMigration has a Down() that always fails.
type failingDownMigration struct {
	name   string
	target string
}

func (m failingDownMigration) Name() string           { return m.name }
func (m failingDownMigration) DataSourceName() string { return m.target }
func (m failingDownMigration) Up() core.MigrationClosure {
	return func(g *gorm.DB) error { return nil }
}
func (m failingDownMigration) Down() core.MigrationClosure {
	return func(g *gorm.DB) error {
		return g.Exec(`DROP TABLE table_that_does_not_exist_anywhere`).Error
	}
}

func appliedCount(t *testing.T, gormDb *gorm.DB) int64 {
	t.Helper()

	var count int64
	require.NoError(t, gormDb.Model(&db.Migration{}).Count(&count).Error)
	return count
}

func isApplied(t *testing.T, gormDb *gorm.DB, name string) bool {
	t.Helper()

	var count int64
	require.NoError(t, gormDb.Model(&db.Migration{}).Where("name = ?", name).Count(&count).Error)
	return count > 0
}

// ------------------------------------ db:migrate up records only what it commits

// migrateUpChildEnv names the scenario TestMigrateUpChildProcess runs.
//
// `db:migrate up` ends a failed run with os.Exit(1). That exit status is what is under
// test, and it would end this test binary too, so the command runs in a re-executed copy
// of the binary and the parent reads the child's exit status and then the database.
const migrateUpChildEnv = "GORGANY_E2E_MIGRATE_UP_SCENARIO"

// migrateUpProbe is the table every scenario's migration creates, so whether the schema
// change survived is directly observable.
const migrateUpProbe = "migrate_up_probe"

type migrateUpScenario struct {
	config    func() map[string]any
	migration closureMigration
}

// migrateUpScenarios are the runs a child performs, by name.
//
// The command used to commit the migration's transaction without checking the error and
// then write the bookkeeping row on the pool, outside the transaction, without checking
// that error either. So a migration whose COMMIT failed was recorded as applied although
// its schema change had been rolled back, and one whose row could not be written stayed
// applied but unrecorded, to run again on the next deploy. Both runs exited 0.
var migrateUpScenarios = map[string]migrateUpScenario{
	// The control: without it, a child that failed for an unrelated reason would pass every
	// assertion below that expects a failure.
	"pg-succeeds": {pgConfig, closureMigration{name: "probe_succeeds", up: createProbe}},

	// The duplicate passes the INSERT and fails the deferred check at COMMIT, which is a
	// commit failure a real server produces rather than one a fake driver pretends to.
	"pg-commit-fails": {pgConfig, closureMigration{name: "probe_commit_fails", up: func(g *gorm.DB) error {
		if err := g.Exec(`CREATE TABLE ` + migrateUpProbe + ` (id INT UNIQUE DEFERRABLE INITIALLY DEFERRED)`).Error; err != nil {
			return err
		}
		return g.Exec(`INSERT INTO ` + migrateUpProbe + ` VALUES (1), (1)`).Error
	}}},

	// A migration that swallows a statement's error leaves PostgreSQL's transaction
	// aborted, so nothing after it can commit, however the closure returns.
	"pg-transaction-aborted": {pgConfig, closureMigration{name: "probe_transaction_aborted", up: func(g *gorm.DB) error {
		if err := createProbe(g); err != nil {
			return err
		}
		_ = g.Exec(`SELECT * FROM table_that_does_not_exist_anywhere`).Error
		return nil
	}}},

	// The bookkeeping row is refused by a constraint the migration itself adds.
	"pg-record-fails": {pgConfig, closureMigration{name: "probe_record_fails", up: refuseBookkeepingRows}},

	"mysql-record-fails": {mysqlConfig, closureMigration{name: "probe_record_fails", up: refuseBookkeepingRows}},

	// SQL Server has transactional DDL, as Postgres has, and no deferrable constraint to fail a
	// COMMIT with, so its failing runs are the other two. Its swallowed error dooms the
	// transaction only because every connection runs SET XACT_ABORT ON; without it SQL Server
	// fails the one statement and keeps the transaction, and the probe table would commit.
	"mssql-succeeds": {mssqlConfig, closureMigration{name: "probe_succeeds", up: createProbe}},
	"mssql-transaction-aborted": {mssqlConfig, closureMigration{name: "probe_transaction_aborted", up: func(g *gorm.DB) error {
		if err := createProbe(g); err != nil {
			return err
		}
		_ = g.Exec(`SELECT * FROM table_that_does_not_exist_anywhere`).Error
		return nil
	}}},
	"mssql-record-fails": {mssqlConfig, closureMigration{name: "probe_record_fails", up: refuseBookkeepingRows}},
}

func createProbe(g *gorm.DB) error {
	return g.Exec(`CREATE TABLE ` + migrateUpProbe + ` (id INT)`).Error
}

func refuseBookkeepingRows(g *gorm.DB) error {
	if err := createProbe(g); err != nil {
		return err
	}
	return g.Exec(`ALTER TABLE migrations ADD CONSTRAINT migrations_refuse_rows CHECK (name = '')`).Error
}

// TestMigrateUpChildProcess is the child runMigrateUpInChild starts. In any other run it
// returns at once.
func TestMigrateUpChildProcess(t *testing.T) {
	name := os.Getenv(migrateUpChildEnv)
	if name == "" {
		return
	}

	scenario, ok := migrateUpScenarios[name]
	require.True(t, ok, "unknown scenario %q", name)

	viper.Set("databases", map[string]any{"default": scenario.config()})
	runMigrateUp(t, "default", scenario.migration)
}

// runMigrateUpInChild runs the named scenario's `db:migrate up` in a child process and
// returns its exit status and output.
func runMigrateUpInChild(t *testing.T, scenario string) (int, string) {
	t.Helper()
	return runScenarioInChild(t, "TestMigrateUpChildProcess", migrateUpChildEnv, scenario)
}

// runScenarioInChild re-executes this test binary to run only the named child test, with
// envVar set to scenario, and returns the child's exit status and output.
func runScenarioInChild(t *testing.T, test, envVar, scenario string) (int, string) {
	t.Helper()

	// The timeout bounds a child that hangs, which a regression in db:seed's connection
	// handling would do, rather than leaving it to the parent's much longer one.
	child := exec.Command(os.Args[0], "-test.run=^"+test+"$", "-test.count=1", "-test.timeout=2m")

	// The child is not a verification run, so it must not demand live cases of itself. It
	// runs one scenario and admits no case through a gate, so either switch would fail its
	// TestMain.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, requireLiveEnvVar+"=") &&
			!strings.HasPrefix(entry, requireSQLServerEnvVar+"=") {
			child.Env = append(child.Env, entry)
		}
	}
	child.Env = append(child.Env, envVar+"="+scenario)

	output, err := child.CombinedOutput()
	if err == nil {
		return 0, string(output)
	}

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "the child did not run: %s", output)
	return exitErr.ExitCode(), string(output)
}

// migrateUpFailureMarker is what the command logs on its failure path. Asserting it tells
// the command's exit 1 apart from a child whose own test setup failed, which exits 1 too.
const migrateUpFailureMarker = "Migration has finished with error"

func resetMigrateUpProbe(t *testing.T, gormDb *gorm.DB) {
	t.Helper()

	reset := func() {
		assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS `+migrateUpProbe).Error)
		assert.NoError(t, gormDb.Migrator().DropTable(&db.Migration{}))
	}
	reset()
	t.Cleanup(reset)
}

func TestMigrateUpRecordsOnlyWhatCommitsOnPostgres(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	// t.Cleanup rather than defer: a deferred Close runs before the cleanups, which would
	// then reset the probe through a closed handle and leave it behind.
	t.Cleanup(func() { ds.Close() })

	assertMigrateUpRecordsOnlyWhatCommits(t, gormOf(t, ds), "pg-succeeds",
		"pg-commit-fails", "pg-transaction-aborted", "pg-record-fails")
}

// assertMigrateUpRecordsOnlyWhatCommits runs the control scenario, which must succeed and be
// recorded, and then each failing one, which must fail the run and leave neither the probe
// table nor its record: the assertions of an engine whose DDL is transactional.
func assertMigrateUpRecordsOnlyWhatCommits(t *testing.T, gormDb *gorm.DB, control string, failing ...string) {
	t.Helper()

	t.Run(control, func(t *testing.T) {
		resetMigrateUpProbe(t, gormDb)

		code, output := runMigrateUpInChild(t, control)
		require.Equal(t, 0, code, "the control scenario must succeed:\n%s", output)
		assert.True(t, gormDb.Migrator().HasTable(migrateUpProbe))
		assert.True(t, isApplied(t, gormDb, migrateUpScenarios[control].migration.name))
	})

	for _, scenario := range failing {
		t.Run(scenario, func(t *testing.T) {
			resetMigrateUpProbe(t, gormDb)

			code, output := runMigrateUpInChild(t, scenario)
			assert.Equal(t, 1, code, "a migration that did not commit must fail the run:\n%s", output)
			assert.Contains(t, output, migrateUpFailureMarker)

			assert.False(t, gormDb.Migrator().HasTable(migrateUpProbe),
				"the schema change must have been rolled back")
			assert.False(t, isApplied(t, gormDb, migrateUpScenarios[scenario].migration.name),
				"and the migration must not be recorded as applied")
		})
	}
}

// TestMigrateUpFailsWhenTheRecordCannotBeWrittenOnMySQL covers the bookkeeping write on
// the engine that cannot roll DDL back.
func TestMigrateUpFailsWhenTheRecordCannotBeWrittenOnMySQL(t *testing.T) {
	requireMySQL(t)

	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(mysqlConfig())
	})
	t.Cleanup(func() { ds.Close() })
	gormDb := gormOf(t, ds)
	resetMigrateUpProbe(t, gormDb)

	code, output := runMigrateUpInChild(t, "mysql-record-fails")
	assert.Equal(t, 1, code, "an unrecorded migration must fail the run:\n%s", output)
	assert.Contains(t, output, migrateUpFailureMarker)
	assert.False(t, isApplied(t, gormDb, "probe_record_fails"))

	// MySQL commits every DDL statement implicitly, so the table stays although the run
	// failed. That is the documented limitation, pinned so a change in it is noticed.
	assert.True(t, gormDb.Migrator().HasTable(migrateUpProbe))
}

// closureMigration is a test migration whose Up is the given closure.
type closureMigration struct {
	name string
	up   func(*gorm.DB) error
}

func (m closureMigration) Name() string              { return m.name }
func (m closureMigration) Up() core.MigrationClosure { return m.up }
func (m closureMigration) Down() core.MigrationClosure {
	return func(g *gorm.DB) error {
		return g.Exec(`DROP TABLE IF EXISTS ` + migrateUpProbe).Error
	}
}

// ------------------------------------------ db:seed commits a seeder with its row

// seedChildEnv names the scenario TestSeedChildProcess runs. Like `db:migrate up`, a failed
// `db:seed` ends with os.Exit(1), so the command runs in a child process.
const seedChildEnv = "GORGANY_E2E_SEED_SCENARIO"

// seedFailureMarker is what db:seed logs on its failure path. As with
// migrateUpFailureMarker, it tells the command's exit 1 apart from a child whose own test
// failed, which exits 1 too: the command's old panic reached the child as a failed
// require.NotPanics.
const seedFailureMarker = "Seeding has finished with error"

// seedProbe is the model every scenario's seeder saves, so which of its rows survived is
// directly observable. Its key is generated, so Save inserts it.
type seedProbe struct {
	ID    uint
	Label string `gorm:"size:64"`
}

func (seedProbe) TableName() string { return "seed_probe" }

// probeSeeder saves one seedProbe per label, in order.
type probeSeeder struct {
	name   string
	labels []string
}

func (s probeSeeder) Name() string { return s.name }
func (s probeSeeder) CollectInsertModels() []any {
	models := make([]any, 0, len(s.labels))
	for _, label := range s.labels {
		models = append(models, &seedProbe{Label: label})
	}
	return models
}

type seedScenario struct {
	config func() map[string]any
	seeder probeSeeder
	// prepare runs in the parent, after it has created the probe and seeders tables and
	// before it starts the child. A seeder can only save rows, so a constraint that makes
	// one of its writes fail is added here.
	prepare func(*gorm.DB) error
}

// seedScenarios are the runs a child performs, by name.
//
// The command used to open one transaction for the whole run and then save every model,
// and write the `seeders` row, on the pool instead. A model that failed to save left the
// models before it committed, and the run panicked (exit 2). A row that could not be
// written was not noticed: the models stayed, the seeder stayed unrecorded, and the run
// exited 0. Either way the next run saved the models again.
var seedScenarios = map[string]seedScenario{
	// The control: without it, a child that failed for an unrelated reason would pass every
	// assertion below that expects a failure.
	"pg-succeeds": {pgConfig, probeSeeder{name: "probe_seeds", labels: []string{"one", "two"}}, nil},

	// The idle transaction used to hold the pool's only connection while the lookup of the
	// seeders table waited for it, so db:seed never finished.
	"pg-one-connection": {pgOneConnectionConfig, probeSeeder{name: "probe_seeds", labels: []string{"one", "two"}}, nil},

	// The second model fails a CHECK, after the first has been saved.
	"pg-save-fails": {pgConfig, probeSeeder{name: "probe_save_fails", labels: []string{"one", "refused"}}, refuseProbeLabel},

	// The duplicate passes the INSERT and fails the deferred check at COMMIT.
	"pg-commit-fails": {pgConfig, probeSeeder{name: "probe_commit_fails", labels: []string{"same", "same"}}, deferProbeLabelUniqueness},

	// Both models save, and the bookkeeping row is refused.
	"pg-record-fails": {pgConfig, probeSeeder{name: "probe_record_fails", labels: []string{"one", "two"}}, refuseSeederRows},

	"mysql-save-fails":   {mysqlConfig, probeSeeder{name: "probe_save_fails", labels: []string{"one", "refused"}}, refuseProbeLabel},
	"mysql-record-fails": {mysqlConfig, probeSeeder{name: "probe_record_fails", labels: []string{"one", "two"}}, refuseSeederRows},

	"mssql-succeeds":     {mssqlConfig, probeSeeder{name: "probe_seeds", labels: []string{"one", "two"}}, nil},
	"mssql-save-fails":   {mssqlConfig, probeSeeder{name: "probe_save_fails", labels: []string{"one", "refused"}}, refuseProbeLabel},
	"mssql-record-fails": {mssqlConfig, probeSeeder{name: "probe_record_fails", labels: []string{"one", "two"}}, refuseSeederRows},
}

func pgOneConnectionConfig() map[string]any {
	cfg := pgConfig()
	cfg["properties"] = map[string]any{"maxOpenConnections": 1}
	return cfg
}

func refuseProbeLabel(g *gorm.DB) error {
	return g.Exec(`ALTER TABLE seed_probe ADD CONSTRAINT seed_probe_refused CHECK (label <> 'refused')`).Error
}

func deferProbeLabelUniqueness(g *gorm.DB) error {
	return g.Exec(`ALTER TABLE seed_probe ADD CONSTRAINT seed_probe_label_unique UNIQUE (label) DEFERRABLE INITIALLY DEFERRED`).Error
}

func refuseSeederRows(g *gorm.DB) error {
	return g.Exec(`ALTER TABLE seeders ADD CONSTRAINT seeders_refuse_rows CHECK (name = '')`).Error
}

// TestSeedChildProcess is the child runSeedInChild starts. In any other run it returns at
// once.
func TestSeedChildProcess(t *testing.T) {
	name := os.Getenv(seedChildEnv)
	if name == "" {
		return
	}

	scenario, ok := seedScenarios[name]
	require.True(t, ok, "unknown scenario %q", name)

	viper.Set("databases", map[string]any{"default": scenario.config()})
	runSeed(t, "default", scenario.seeder)
}

func runSeedInChild(t *testing.T, scenario string) (int, string) {
	t.Helper()
	return runScenarioInChild(t, "TestSeedChildProcess", seedChildEnv, scenario)
}

// runSeed invokes the real db:seed command with the given --datasource, through the
// container, as runMigrate does for db:migrate.
func runSeed(t *testing.T, datasource string, seeders ...core.ISeeder) {
	t.Helper()

	previousArgs := os.Args
	os.Args = []string{"cli", "db:seed", "--datasource=" + datasource}
	t.Cleanup(func() { os.Args = previousArgs })

	c := service.NewContainer()
	dbProvider := provider.NewDbProvider()
	dbProvider.Register(c)

	var dataContext core.IDataContext
	require.NoError(t, c.Make(&dataContext))
	for _, s := range seeders {
		dataContext.AddSeeder(s)
	}

	cmd := &dbCmd.SeedCommand{}
	require.NoError(t, c.Make(cmd))

	require.NotPanics(t, func() { cmd.Execute(context.Background()) }, "db:seed must succeed")
}

// prepareSeedScenario leaves the probe and seeders tables empty, with the scenario's
// constraint added, and drops both when the test ends.
func prepareSeedScenario(t *testing.T, gormDb *gorm.DB, scenario string) {
	t.Helper()

	reset := func() {
		assert.NoError(t, gormDb.Migrator().DropTable(&seedProbe{}))
		assert.NoError(t, gormDb.Migrator().DropTable(&db.Seeder{}))
	}
	reset()
	t.Cleanup(reset)

	require.NoError(t, gormDb.AutoMigrate(&seedProbe{}, &db.Seeder{}))
	if prepare := seedScenarios[scenario].prepare; prepare != nil {
		require.NoError(t, prepare(gormDb))
	}
}

func probeLabels(t *testing.T, gormDb *gorm.DB) []string {
	t.Helper()

	var labels []string
	require.NoError(t, gormDb.Model(&seedProbe{}).Order("id").Pluck("label", &labels).Error)
	return labels
}

func isSeeded(t *testing.T, gormDb *gorm.DB, name string) bool {
	t.Helper()

	var count int64
	require.NoError(t, gormDb.Model(&db.Seeder{}).Where("name = ?", name).Count(&count).Error)
	return count > 0
}

// assertSeedFailedCleanly runs a failing scenario and asserts that it failed the run and
// left neither the seeder's rows nor its record behind.
func assertSeedFailedCleanly(t *testing.T, gormDb *gorm.DB, scenario string) {
	t.Helper()

	prepareSeedScenario(t, gormDb, scenario)

	code, output := runSeedInChild(t, scenario)
	assert.Equal(t, 1, code, "a seeder that did not commit must fail the run:\n%s", output)
	assert.Contains(t, output, seedFailureMarker)

	assert.Empty(t, probeLabels(t, gormDb), "the seeder's rows must have been rolled back")
	assert.False(t, isSeeded(t, gormDb, seedScenarios[scenario].seeder.name),
		"and the seeder must not be recorded")
}

func TestSeedCommitsEachSeederWithItsRecordOnPostgres(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { ds.Close() })
	gormDb := gormOf(t, ds)

	for _, scenario := range []string{"pg-succeeds", "pg-one-connection"} {
		t.Run(scenario, func(t *testing.T) {
			prepareSeedScenario(t, gormDb, scenario)

			code, output := runSeedInChild(t, scenario)
			require.Equal(t, 0, code, "the seeder must succeed:\n%s", output)
			assert.Equal(t, []string{"one", "two"}, probeLabels(t, gormDb))
			assert.True(t, isSeeded(t, gormDb, "probe_seeds"))

			// The second run finds the seeder recorded and saves nothing.
			code, output = runSeedInChild(t, scenario)
			require.Equal(t, 0, code, "a second run must succeed:\n%s", output)
			assert.Equal(t, []string{"one", "two"}, probeLabels(t, gormDb))
		})
	}

	for _, scenario := range []string{"pg-save-fails", "pg-commit-fails", "pg-record-fails"} {
		t.Run(scenario, func(t *testing.T) {
			assertSeedFailedCleanly(t, gormDb, scenario)
		})
	}
}

// TestSeedRollsBackAFailedSeederOnMySQL: a seeder only saves rows, which InnoDB rolls back,
// so unlike a migration a failed seeder leaves nothing behind on MySQL either.
func TestSeedRollsBackAFailedSeederOnMySQL(t *testing.T) {
	requireMySQL(t)

	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(mysqlConfig())
	})
	t.Cleanup(func() { ds.Close() })
	gormDb := gormOf(t, ds)

	for _, scenario := range []string{"mysql-save-fails", "mysql-record-fails"} {
		t.Run(scenario, func(t *testing.T) {
			assertSeedFailedCleanly(t, gormDb, scenario)
		})
	}
}

// ------------------------------- db:* refuse a datasource whose schema another system owns

// ownershipChildEnv names the scenario TestOwnershipRefusalChildProcess runs.
//
// db:migrate, db:seed and db:diff refuse an external_schema datasource as a configuration
// error, which they report by panicking, so the run exits 2, as it does for a datasource that
// is not configured, and not 1, as a migration that fails does. That status is what is under
// test, and it would end this test binary too, so the command runs in a child process.
const ownershipChildEnv = "GORGANY_E2E_OWNERSHIP_SCENARIO"

// ownershipProbe stands for a table the external schema's owner created. A refused command
// must leave it as it found it, columns and rows.
const ownershipProbe = "ownership_probe"

// ownershipCreated is the table the scenarios' migration creates if it is ever run.
const ownershipCreated = "ownership_created"

// ownershipProbeRow is a row of the owner's table. It is also the domain the db:diff
// scenarios register, and it belongs to the external datasource.
type ownershipProbeRow struct {
	ID    int64  `gorm:"primaryKey;autoIncrement"`
	Label string `gorm:"size:64"`
}

func (ownershipProbeRow) TableName() string        { return ownershipProbe }
func (ownershipProbeRow) DbConnectionName() string { return "legacy" }

// legacySeeder is a seeder aimed at the external datasource.
type legacySeeder struct{}

func (legacySeeder) Name() string           { return "legacy_probe_rows" }
func (legacySeeder) DataSourceName() string { return "legacy" }
func (legacySeeder) CollectInsertModels() []any {
	return []any{&ownershipProbeRow{Label: "seeded"}}
}

// ownershipCommand is a db command a child runs, and the refusal it must end with.
type ownershipCommand struct {
	args    []string
	refusal string
}

// ownershipCommands are the runs a child performs, by name. Every child registers a
// migration and a seeder aimed at `legacy`, so the runs that select the owned `default` are
// refused too: an item aimed at an external datasource can never run, and is refused on
// every run rather than skipped on the others.
//
// The commands used to create their `migrations` or `seeders` table first, before they knew
// whether anything targeted the datasource, or whether it was gorgany's to change.
var ownershipCommands = map[string]ownershipCommand{
	"migrate-up": {
		[]string{"db:migrate", "up", "--datasource=legacy"},
		`db:migrate refuses datasource "legacy": its schema is owned outside gorgany (external_schema: true)`,
	},
	"migrate-down": {
		[]string{"db:migrate", "down", "--datasource=legacy", "--steps=1"},
		`db:migrate refuses datasource "legacy": its schema is owned outside gorgany (external_schema: true)`,
	},
	"seed": {
		[]string{"db:seed", "--datasource=legacy"},
		`db:seed refuses datasource "legacy": its schema is owned outside gorgany (external_schema: true)`,
	},
	// On MySQL this is refused for its schema, not for a dialect that commits DDL.
	"diff": {
		[]string{"db:diff", "--datasource=legacy"},
		`db:diff refuses datasource "legacy": its schema is owned outside gorgany (external_schema: true)`,
	},
	"migrate-up-default": {
		[]string{"db:migrate", "up", "--datasource=default"},
		`migration "legacy_table" targets datasource "legacy", which is external_schema: true`,
	},
	"seed-default": {
		[]string{"db:seed", "--datasource=default"},
		`seeder "legacy_probe_rows" targets datasource "legacy", which is external_schema: true`,
	},
}

// logControl is the scenario that shows the statement log the refusals are checked against
// is on: it sends one statement on `legacy` and exits 0.
const logControl = "log-control"

var ownershipEngines = map[string]func() map[string]any{"pg": pgConfig, "mysql": mysqlConfig, "mssql": mssqlConfig}

// loggedConfig is config() with every statement the datasource sends written to the child's
// output, so a child that sent one shows it.
func loggedConfig(config func() map[string]any) map[string]any {
	cfg := config()
	cfg["log"] = true
	return cfg
}

// TestOwnershipRefusalChildProcess is the child runOwnershipScenario starts. In any other run
// it returns at once.
//
// `default` and `legacy` are one database, `legacy` with external_schema: true, so that "no
// bookkeeping table was created" covers both.
func TestOwnershipRefusalChildProcess(t *testing.T) {
	name := os.Getenv(ownershipChildEnv)
	if name == "" {
		return
	}

	engine, commandName, _ := strings.Cut(name, ":")
	config, ok := ownershipEngines[engine]
	require.True(t, ok, "unknown engine in scenario %q", name)

	legacy := loggedConfig(config)
	legacy["external_schema"] = true
	viper.Set("databases", map[string]any{"default": loggedConfig(config), "legacy": legacy})

	c := service.NewContainer()
	provider.NewDbProvider().Register(c)
	require.NoError(t, c.SingletonLazy(func() core.IDomainContext { return &model.DomainContext{} }))

	if commandName == logControl {
		var dbContext core.IDBContext
		require.NoError(t, c.Make(&dbContext))
		legacyGorm, err := dbCmd.ResolveGorm(dbContext, "legacy")
		require.NoError(t, err)
		require.NoError(t, legacyGorm.Exec(`SELECT 1`).Error)
		return
	}

	command, ok := ownershipCommands[commandName]
	require.True(t, ok, "unknown command in scenario %q", name)

	previousArgs := os.Args
	os.Args = append([]string{"cli"}, command.args...)
	t.Cleanup(func() { os.Args = previousArgs })

	var dataContext core.IDataContext
	require.NoError(t, c.Make(&dataContext))
	dataContext.AddMigration(scopedMigration{name: "legacy_table", target: "legacy", table: ownershipCreated})
	dataContext.AddSeeder(legacySeeder{})

	var domainContext core.IDomainContext
	require.NoError(t, c.Make(&domainContext))
	domainContext.RegisterDomain("tests.ownershipProbeRow", ownershipProbeRow{})

	var cmd interface{ Execute(context.Context) }
	switch command.args[0] {
	case "db:migrate":
		cmd = &dbCmd.MigrateCommand{}
	case "db:seed":
		cmd = &dbCmd.SeedCommand{}
	case "db:diff":
		cmd = &dbCmd.DiffCommand{}
	}
	require.NoError(t, c.Make(cmd))

	// Not under require.NotPanics: the refusal is the panic, and the exit 2 it ends this
	// child with is what the parent asserts.
	cmd.Execute(context.Background())
	t.Fatalf("%v returned instead of refusing", command.args)
}

// runOwnershipScenario runs one command of ownershipCommands, or logControl, on engine in a
// child process and returns its exit status and output.
func runOwnershipScenario(t *testing.T, engine, command string) (int, string) {
	t.Helper()
	return runScenarioInChild(t, "TestOwnershipRefusalChildProcess", ownershipChildEnv, engine+":"+command)
}

// resetOwnershipProbe leaves the owner's table with one row, and no table the commands
// create, and drops them all when the test ends.
func resetOwnershipProbe(t *testing.T, owner *gorm.DB) {
	t.Helper()

	reset := func() {
		assert.NoError(t, owner.Migrator().DropTable(ownershipProbe, ownershipCreated, &db.Migration{}, &db.Seeder{}))
	}
	reset()
	t.Cleanup(reset)

	require.NoError(t, owner.AutoMigrate(&ownershipProbeRow{}))
	require.NoError(t, owner.Create(&ownershipProbeRow{Label: "owner"}).Error)
}

// tableInInformationSchema reports whether the connected database has the table, asking
// information_schema rather than gorm's Migrator, which the commands under test use.
func tableInInformationSchema(t *testing.T, g *gorm.DB, table string) bool {
	t.Helper()

	schema := "current_schema()"
	switch g.Dialector.Name() {
	case "mysql":
		schema = "DATABASE()"
	case "sqlserver":
		schema = "SCHEMA_NAME()"
	}

	var count int64
	require.NoError(t, g.Raw(
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = `+schema+` AND table_name = ?`,
		table,
	).Scan(&count).Error)
	return count > 0
}

func ownershipProbeLabels(t *testing.T, g *gorm.DB) []string {
	t.Helper()

	var labels []string
	require.NoError(t, g.Model(&ownershipProbeRow{}).Order("id").Pluck("label", &labels).Error)
	return labels
}

// assertDbCommandsRefuseAnExternalSchema runs every command of ownershipCommands on engine
// and asserts that each exits 2 with its refusal, having sent no statement: no bookkeeping
// table, no table of the migration's, and the owner's table as it was.
func assertDbCommandsRefuseAnExternalSchema(t *testing.T, engine string, owner *gorm.DB) {
	t.Helper()

	// Without it, "no statement was logged" below would pass for a log that is off.
	t.Run(logControl, func(t *testing.T) {
		code, output := runOwnershipScenario(t, engine, logControl)
		require.Equal(t, 0, code, "the control must succeed:\n%s", output)
		require.Contains(t, output, "[rows:", "the child's datasources must log the statements they send")
	})

	names := make([]string, 0, len(ownershipCommands))
	for name := range ownershipCommands {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			resetOwnershipProbe(t, owner)
			columns := describeTable(t, owner, ownershipProbe)

			code, output := runOwnershipScenario(t, engine, name)

			assert.Equal(t, 2, code, "a refusal is a configuration error, which exits 2:\n%s", output)
			assert.Contains(t, output, ownershipCommands[name].refusal)
			assert.NotContains(t, output, "statement refused",
				"the command must refuse before the connection's DDL guard has a statement to refuse")
			assert.NotContains(t, output, "commits DDL immediately")
			assert.NotContains(t, output, "[rows:", "no statement may be sent, on either datasource")

			for _, table := range []string{"migrations", "seeders", ownershipCreated} {
				assert.Falsef(t, tableInInformationSchema(t, owner, table), "no %s table may be created", table)
			}
			assert.Equal(t, columns, describeTable(t, owner, ownershipProbe), "the owner's table keeps its columns")
			assert.Equal(t, []string{"owner"}, ownershipProbeLabels(t, owner), "and its rows")
		})
	}
}

func TestDbCommandsRefuseAnExternalSchemaOnPostgres(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	assertDbCommandsRefuseAnExternalSchema(t, "pg", gormOf(t, ds))
}

func TestDbCommandsRefuseAnExternalSchemaOnMySQL(t *testing.T) {
	requireMySQL(t)

	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(mysqlConfig())
	})
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	assertDbCommandsRefuseAnExternalSchema(t, "mysql", gormOf(t, ds))
}

// ============================================================ A1: ORM on MySQL

// ormWidget has an AUTO_INCREMENT primary key, which is the case the brief calls
// out: reading a generated key back is exactly what needs RETURNING on Postgres and
// something else entirely on MySQL.
type ormWidget struct {
	orm.BaseEntity
	ID    int64  `gorm:"primaryKey;autoIncrement;column:id"`
	Name  string `gorm:"column:name;not null"`
	Label string `gorm:"column:label"`
}

func (ormWidget) TableName() string { return "orm_widgets" }

// ormTag and the join table cover the many-to-many path, which built raw
// `ON CONFLICT` regardless of engine.
type ormTag struct {
	orm.BaseEntity
	ID   int64  `gorm:"primaryKey;autoIncrement;column:id"`
	Name string `gorm:"column:name;not null"`
}

func (ormTag) TableName() string { return "orm_tags" }

// createOrmSchema builds the probe tables with engine-appropriate DDL.
func createOrmSchema(t *testing.T, gormDb *gorm.DB, dialect string) {
	t.Helper()

	dropOrmSchema(t, gormDb)

	autoPK := "BIGSERIAL PRIMARY KEY"
	switch dialect {
	case "mysql":
		autoPK = "BIGINT AUTO_INCREMENT PRIMARY KEY"
	case "sqlserver":
		autoPK = "BIGINT IDENTITY(1,1) PRIMARY KEY"
	}

	stmts := []string{
		fmt.Sprintf("CREATE TABLE orm_widgets (id %s, name VARCHAR(100) NOT NULL, label VARCHAR(100))", autoPK),
		fmt.Sprintf("CREATE TABLE orm_tags (id %s, name VARCHAR(100) NOT NULL)", autoPK),
		"CREATE TABLE orm_widget_tags (orm_widget_id BIGINT NOT NULL, orm_tag_id BIGINT NOT NULL, " +
			"PRIMARY KEY (orm_widget_id, orm_tag_id))",
	}
	for _, s := range stmts {
		require.NoError(t, gormDb.Exec(s).Error, "DDL: %s", s)
	}
}

func dropOrmSchema(t *testing.T, gormDb *gorm.DB) {
	t.Helper()
	for _, table := range []string{"orm_widget_tags", "orm_widgets", "orm_tags"} {
		assert.NoError(t, gormDb.Exec("DROP TABLE IF EXISTS "+table).Error)
	}
}

// TestA1_OrmCreateInsertsOnEveryEngine is the test whose absence let a MySQL driver
// ship with a green suite while being unable to insert a row.
//
// The ORM built every query with v2.NewBuilder() — the Postgres builder — so
// `Create` appended `RETURNING id` and MySQL answered with error 1064. Every dialect
// test asserted strings; none drove the ORM against a real engine.
func TestA1_OrmCreateInsertsOnEveryEngine(t *testing.T) {
	for _, engine := range ormEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			gormDb := gormOf(t, engine.ds)
			createOrmSchema(t, gormDb, engine.name)
			t.Cleanup(func() { dropOrmSchema(t, gormDb) })

			session, err := engine.ds.NewSession()
			require.NoError(t, err)
			defer session.Close()

			require.Equal(t, engine.name, session.Query().Dialect().Name(),
				"the session must speak the engine's own dialect")

			widget := &ormWidget{Name: "gadget", Label: "shiny"}
			require.NoError(t, orm.New[*ormWidget](session).Create(widget),
				"orm.Create must succeed on %s", engine.name)

			// The generated key must come back on every engine: via RETURNING on
			// Postgres, OUTPUT on SQL Server, and the driver's sql.Result on MySQL.
			assert.NotZero(t, widget.ID,
				"the auto-increment primary key must be populated after Create")

			// And the row must really be there, with the values we sent.
			var stored ormWidget
			require.NoError(t, gormDb.Raw(
				"SELECT id, name, label FROM orm_widgets WHERE id = ?", widget.ID,
			).Scan(&stored).Error)
			assert.Equal(t, widget.ID, stored.ID)
			assert.Equal(t, "gadget", stored.Name)
			assert.Equal(t, "shiny", stored.Label)

			// A second insert must get a distinct key, which proves the read-back is
			// per-statement and not a stale cached value.
			second := &ormWidget{Name: "other", Label: "dull"}
			require.NoError(t, orm.New[*ormWidget](session).Create(second))
			assert.NotZero(t, second.ID)
			assert.NotEqual(t, widget.ID, second.ID,
				"each Create must read back its own generated key")

			var count int64
			require.NoError(t, gormDb.Raw("SELECT COUNT(*) FROM orm_widgets").Scan(&count).Error)
			assert.Equal(t, int64(2), count)
		})
	}
}

// TestA1_ManyToManySaveWritesTheJoinRow covers the other half of A1: the m2m path
// built `ON CONFLICT (a, b) DO NOTHING` with the Postgres builder, bypassing the
// MySQL dialect's own translation to ON DUPLICATE KEY UPDATE and sending a clause
// MySQL has no syntax for.
func TestA1_ManyToManySaveWritesTheJoinRow(t *testing.T) {
	for _, engine := range ormEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			gormDb := gormOf(t, engine.ds)
			createOrmSchema(t, gormDb, engine.name)
			t.Cleanup(func() { dropOrmSchema(t, gormDb) })

			session, err := engine.ds.NewSession()
			require.NoError(t, err)
			defer session.Close()

			widget := &ormWidget{Name: "gadget"}
			require.NoError(t, orm.New[*ormWidget](session).Create(widget))
			tag := &ormTag{Name: "red"}
			require.NoError(t, orm.New[*ormTag](session).Create(tag))
			require.NotZero(t, widget.ID)
			require.NotZero(t, tag.ID)

			// Write the association through the builder the ORM now uses, exercising
			// the dialect's own upsert translation rather than raw ON CONFLICT.
			insert := session.Query().
				Insert("orm_widget_tags").
				Columns("orm_widget_id", "orm_tag_id").
				Values(widget.ID, tag.ID).
				OnConflict("orm_widget_id", "orm_tag_id").
				DoNothing()

			sql, _, err := insert.ToSQL()
			require.NoError(t, err, "the join insert must render for %s", engine.name)
			t.Logf("%s join upsert: %s", engine.name, sql)

			res := session.Executor().Exec(context.Background(), insert)
			require.NoError(t, res.Error, "the join insert must execute on %s", engine.name)

			var joined int64
			require.NoError(t, gormDb.Raw(
				"SELECT COUNT(*) FROM orm_widget_tags WHERE orm_widget_id = ? AND orm_tag_id = ?",
				widget.ID, tag.ID,
			).Scan(&joined).Error)
			assert.Equal(t, int64(1), joined, "the join row must exist")

			// Re-running the same upsert must be a no-op, not a duplicate-key error —
			// that is the whole point of DO NOTHING, and the translation must preserve it.
			res = session.Executor().Exec(context.Background(), insert)
			require.NoError(t, res.Error, "a repeated DO NOTHING upsert must not error")

			require.NoError(t, gormDb.Raw(
				"SELECT COUNT(*) FROM orm_widget_tags").Scan(&joined).Error)
			assert.Equal(t, int64(1), joined, "DO NOTHING must not insert a duplicate")
		})
	}
}

// ormEngine pairs a datasource with the dialect name it speaks.
type ormEngine struct {
	name string
	ds   dbCore.IDataSource
}

// ormEngines returns every reachable engine, so the ORM tests assert on Postgres
// (no regression), MySQL (the fix) and SQL Server from one body. SQL Server is left out with
// a log line when it is not reachable, as MySQL is, unless E2E_REQUIRE_SQLSERVER=1 demands it
// (see sqlServerForSharedCase).
func ormEngines(t *testing.T) []ormEngine {
	t.Helper()

	engines := postgresAndMySQLEngines(t)
	if ds := sqlServerForSharedCase(t); ds != nil {
		engines = append(engines, ormEngine{name: "sqlserver", ds: ds})
	}
	return engines
}

// postgresAndMySQLEngines is ormEngines without SQL Server, for a case whose SQL Server
// counterpart is a case of its own.
func postgresAndMySQLEngines(t *testing.T) []ormEngine {
	t.Helper()

	engines := []ormEngine{}

	pg := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { _ = pg.Close() })
	engines = append(engines, ormEngine{name: "postgres", ds: pg})

	if mysqlAvailable() {
		my, err := mysqlv2.NewDataSource(mysqlConfig())
		require.NoError(t, err)
		t.Cleanup(func() { _ = my.Close() })
		engines = append(engines, ormEngine{name: "mysql", ds: my})
	} else {
		t.Log("MySQL not reachable; the ORM fix is only asserted on Postgres in this run")
	}

	return engines
}

// TestCountByQueryShapesOnBothEngines runs the queries CountByQuery wraps against real
// engines, which is the only place the wrapped SQL is judged. CountByQuery used to append
// COUNT(*) to the caller's select list and keep its ORDER BY and LIMIT, so a grouped,
// DISTINCT or UNION query was counted as something else, and a page's total was capped at
// the page size. It now counts a derived table, and a derived table is where the engines
// disagree: MySQL refuses one with a repeated column name (error 1060), so the ORM must
// either name every column once or refuse before sending — both are asserted here. The
// grouped shapes pin which columns the derived table keeps: a star under GROUP BY is
// refused by Postgres (42803) and by MySQL's ONLY_FULL_GROUP_BY (1055), so it is replaced,
// while a GROUP BY that names a select-list alias or position needs that list kept.
func TestCountByQueryShapesOnBothEngines(t *testing.T) {
	for _, engine := range postgresAndMySQLEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			assertCountByQueryShapes(t, engine)
		})
	}
}

// countByQueryShape is one query CountByQuery wraps, and the total it must report.
type countByQueryShape struct {
	name  string
	build func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder
	want  int64

	// sqlServerError is the error number SQL Server answers the shape with, for a spelling it
	// does not have, or 0 when it runs there too.
	sqlServerError int32

	// sqlServerRefusal is the construct the SQL Server dialect refuses the shape as, before
	// anything is sent, or "" when it renders there.
	sqlServerRefusal string
}

// countByQueryShapes are the shapes TestCountByQueryShapesOnBothEngines and
// TestSQLServerCountByQueryShapes count, over the rows assertCountByQueryShapes inserts.
func countByQueryShapes(session dbCore.ISession) []countByQueryShape {
	return []countByQueryShape{
		{
			// The total of the second page of one, not the page itself.
			name: "paged",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Neq("name", "c").OrderBy("name", "ASC").Limit(1).Offset(1)
			},
			want: 4,
		},
		{
			name: "group by",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("name", "COUNT(*)").GroupBy("name").OrderBy("name", "ASC")
			},
			want: 3,
		},
		{
			name: "group by with having",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("name", "COUNT(*) AS n").GroupBy("name").
					Having(&dbCore.RawCondition{SQL: "COUNT(*) > ?", Args: []any{1}})
			},
			want: 2,
		},
		{
			name: "group by with having and no select list",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.GroupBy("name").Having(&dbCore.RawCondition{SQL: "COUNT(*) > ?", Args: []any{1}})
			},
			want: 2,
		},
		{
			name: "group by with having and a star",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("*").GroupBy("name").
					Having(&dbCore.RawCondition{SQL: "COUNT(*) > ?", Args: []any{1}})
			},
			want: 2,
		},
		{
			// T-SQL cannot GROUP BY a select-list alias (Msg 207, invalid column name).
			name: "group by a select-list alias",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("UPPER(name) AS uname", "COUNT(*) AS n").GroupBy("uname")
			},
			want:           3,
			sqlServerError: 207,
		},
		{
			// Nor by a position, which it reads as the constant 1 (Msg 164). The dialect says
			// so itself, before the server does.
			name: "group by a select-list position",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("name").GroupBy("1")
			},
			want:             3,
			sqlServerRefusal: "a GROUP BY position",
		},
		{
			name: "distinct",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("DISTINCT name, label").Limit(2)
			},
			want: 4,
		},
		{
			name: "distinct written with parentheses",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("DISTINCT(name)")
			},
			want: 3,
		},
		{
			name: "distinct written with parentheses beside another column",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("DISTINCT(name), label")
			},
			want: 4,
		},
		{
			// Both columns are called name; aliasing one is what keeps MySQL from
			// answering 1060 for the derived table.
			name: "distinct across a join",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				return q.Select("DISTINCT orm_widgets.name", "orm_tags.name AS tag_name").
					CrossJoin("orm_tags")
			},
			want: 6,
		},
		{
			name: "union",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				tags := session.Query().Select("name").From("orm_tags").Build()
				return q.Select("name").Union(tags).Limit(1)
			},
			want: 4,
		},
		{
			name: "union all",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				tags := session.Query().Select("name").From("orm_tags").Build()
				return q.Select("name").UnionAll(tags)
			},
			want: 7,
		},
		{
			name: "common table expression",
			build: func(q dbCore.IQueryBuilder) dbCore.IQueryBuilder {
				labelled := session.Query().Select("name").From("orm_widgets").IsNotNull("label").Build()
				return q.WithCTE("labelled", labelled).From("labelled").Select("DISTINCT name")
			},
			want: 2,
		},
	}
}

// assertCountByQueryShapes counts every shape of countByQueryShapes on engine.
func assertCountByQueryShapes(t *testing.T, engine ormEngine) {
	t.Helper()

	gormDb := gormOf(t, engine.ds)
	createOrmSchema(t, gormDb, engine.name)
	t.Cleanup(func() { dropOrmSchema(t, gormDb) })

	for _, row := range [][2]any{{"a", "x"}, {"a", "y"}, {"b", "x"}, {"b", "x"}, {"c", nil}} {
		require.NoError(t, gormDb.Exec(
			"INSERT INTO orm_widgets (name, label) VALUES (?, ?)", row[0], row[1]).Error)
	}
	for _, name := range []string{"a", "z"} {
		require.NoError(t, gormDb.Exec("INSERT INTO orm_tags (name) VALUES (?)", name).Error)
	}

	session, err := engine.ds.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Close()) })
	widgets := orm.New[*ormWidget](session)

	for _, shape := range countByQueryShapes(session) {
		t.Run(shape.name, func(t *testing.T) {
			total, err := widgets.CountByQuery(shape.build(session.Query()))
			if engine.name == "sqlserver" && shape.sqlServerError != 0 {
				require.Error(t, err, "SQL Server has no such spelling")
				assert.Equal(t, shape.sqlServerError, sqlServerErrorNumber(err), "%v", err)
				return
			}
			if engine.name == "sqlserver" && shape.sqlServerRefusal != "" {
				requireUnsupported(t, err, shape.sqlServerRefusal)
				return
			}
			require.NoError(t, err, "CountByQuery must run on %s", engine.name)
			assert.Equal(t, shape.want, total)
		})
	}

	// Unaliased, the join's two name columns would reach MySQL as a derived table with
	// a repeated column. The ORM refuses that itself, on every engine, before sending.
	_, err = widgets.CountByQuery(session.Query().
		Select("DISTINCT orm_widgets.name", "orm_tags.name").CrossJoin("orm_tags"))
	require.Error(t, err)
	assert.Equal(t, "orm: CountByQuery cannot wrap a DISTINCT/UNION query with unnamed or duplicate "+
		"columns (name is not unique); alias them", err.Error())
}

// TestTheUpsertOptInWorksThroughTheConfig is H3 against a real MySQL 8.
//
// TestMySQLQueriesRoundTripThroughTheDialect above proves the opted-in SQL is accepted, but
// it takes the opt-in with NewBuilderWithDialect — a builder constructed by hand, bypassing
// the ORM. That was the only route there was, and it is why the flag shipped unreachable:
// gormMySQLDataSource.Dialect() returned a hard-coded &MySQLDialect{}, so session.Query()
// could never honour it however the app was configured.
//
// This test takes the opt-in the way an app does — a key in the datasource config — and then
// uses session.Query(), so what is verified is the path an app actually has.
func TestTheUpsertOptInWorksThroughTheConfig(t *testing.T) {
	requireMySQL(t)

	config := mysqlConfig()
	config["allow_unfaithful_upsert"] = true

	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(config)
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS upsert_optin_probe`).Error)
	require.NoError(t, gormDb.Exec(
		"CREATE TABLE upsert_optin_probe (id INT PRIMARY KEY, amount INT)").Error)
	t.Cleanup(func() { assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS upsert_optin_probe`).Error) })

	session, err := ds.NewSession()
	require.NoError(t, err)
	defer session.Close()

	upsert := func(amount int) dbCore.IQueryBuilder {
		return session.Query().
			Insert("upsert_optin_probe").
			Columns("id", "amount").
			Values(1, amount).
			OnConflict("id").
			DoUpdate(map[string]interface{}{"amount": amount})
	}

	// session.Query() — not a hand-built builder. This is the assertion that failed before
	// H3: the session's own dialect had the flag off no matter what the config said.
	_, _, err = upsert(10).ToSQL()
	require.NoError(t, err, "the config opt-in must reach session.Query()'s dialect")

	require.NoError(t, session.Executor().Exec(ctxBackground(), upsert(10)).Error)
	require.NoError(t, session.Executor().Exec(ctxBackground(), upsert(20)).Error,
		"the second statement must take the ON DUPLICATE KEY branch")

	var amount int
	require.NoError(t, gormDb.Raw(`SELECT amount FROM upsert_optin_probe WHERE id = 1`).Scan(&amount).Error)
	assert.Equal(t, 20, amount)

	// And a transaction, which carries its own dialect field.
	require.NoError(t, session.Transaction(ctxBackground(), func(tx dbCore.IDBTransaction) error {
		_, _, txErr := tx.Query().
			Insert("upsert_optin_probe").
			Columns("id", "amount").
			Values(2, 5).
			OnConflict("id").
			DoUpdate(map[string]interface{}{"amount": 5}).
			ToSQL()
		return txErr
	}), "a transaction's builder must honour the opt-in too")
}

// ------------------------------------- I3: the sweep must batch, on both engines

// I3. DbSessionRepository.DeleteExpired used to be one statement:
//
//	DELETE FROM sessions WHERE expiry < NOW()
//
// Harmless while nothing called it, which was the case until H4 — the job meant to call it was
// registered by nothing. H4 gives it a caller, so the first sweep on an app running for months
// would delete everything accumulated since deployment in a single statement: a long lock on
// the matched tuples, a WAL burst proportional to the whole backlog, and bloat needing VACUUM.
// The fix for the leak would have hit hardest exactly the apps that had leaked most.
//
// The batched statement wraps its subquery in a derived table because MySQL rejects a subquery
// on the DELETE's own target with error 1093, while `DELETE ... LIMIT n` — the obvious form —
// exists on MySQL and not on Postgres. So the SQL is the same on both and neither engine's
// acceptance can be assumed: this runs it on both.

// sweepProbe creates a sessions table and fills it with expired rows.
func sweepProbe(t *testing.T, gormDb *gorm.DB, expired int) {
	t.Helper()

	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS sessions`).Error)
	require.NoError(t, migration.NewSessionsMigration().Up()(gormDb))
	t.Cleanup(func() { assert.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS sessions`).Error) })

	for i := 0; i < expired; i++ {
		require.NoError(t, gormDb.Exec(
			`INSERT INTO sessions (id, user_id, expiry, created_at, last_activity) `+
				`VALUES (?, '', ?, ?, ?)`,
			fmt.Sprintf("expired-%d", i),
			time.Now().Add(-time.Hour), time.Now().Add(-time.Hour), time.Now().Add(-time.Hour),
		).Error)
	}

	// One live row, to prove the sweep is selective and not a truncate.
	require.NoError(t, gormDb.Exec(
		`INSERT INTO sessions (id, user_id, expiry, created_at, last_activity) `+
			`VALUES ('live', '', ?, ?, ?)`,
		time.Now().Add(time.Hour), time.Now(), time.Now(),
	).Error)
}

func countSessions(t *testing.T, gormDb *gorm.DB) int64 {
	t.Helper()

	var n int64
	require.NoError(t, gormDb.Raw(`SELECT count(*) FROM sessions`).Scan(&n).Error)
	return n
}

// runBatchedSweep executes the framework's own batched statement the way DeleteExpired does,
// through the session executor, and returns how many batches it took.
//
// It drives the real SQL and the real loop condition rather than calling DeleteExpired, whose
// container-injected dbContext is not available here — so what is verified is that the
// statement both engines have to accept does batch, and terminates.
func runBatchedSweep(t *testing.T, ds dbCore.IDataSource, batch int) int {
	t.Helper()

	session, err := ds.NewSession()
	require.NoError(t, err)
	defer session.Close()

	batches := 0
	for {
		result := session.Executor().ExecRaw(ctxBackground(), auth.BatchedExpiredDeleteSQL, batch)
		require.NoError(t, result.Error, "the batched delete must be valid on this engine")
		batches++

		if result.RowsAffected < int64(batch) {
			return batches
		}
		require.Less(t, batches, 100, "the sweep must terminate")
	}
}

func TestTheSessionSweepBatchesOnPostgres(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	sweepProbe(t, gormDb, 25)

	batches := runBatchedSweep(t, ds, 10)

	assert.Equal(t, 3, batches, "25 expired rows at 10 per batch is three statements")
	assert.Equal(t, int64(1), countSessions(t, gormDb), "the unexpired session must survive")
}

func TestTheSessionSweepBatchesOnMySQL(t *testing.T) {
	requireMySQL(t)

	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(mysqlConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	sweepProbe(t, gormDb, 25)

	// The derived-table wrapper is here for MySQL specifically: without it this statement is
	// error 1093, "You can't specify target table for update in FROM clause".
	batches := runBatchedSweep(t, ds, 10)

	assert.Equal(t, 3, batches)
	assert.Equal(t, int64(1), countSessions(t, gormDb))
}

// TestASweepWithNothingExpiredIsOneStatement — the common case on a healthy app, and it must
// not cost a batch per tick beyond the one that finds nothing.
func TestASweepWithNothingExpiredIsOneStatement(t *testing.T) {
	ds := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { ds.Close() })

	gormDb := gormOf(t, ds)
	sweepProbe(t, gormDb, 0)

	assert.Equal(t, 1, runBatchedSweep(t, ds, 10))
	assert.Equal(t, int64(1), countSessions(t, gormDb))
}

// ============================================================ read_only on Postgres and MySQL

// readOnlyProbe is the table the read_only cases read, and try to write, through a read_only
// datasource on the same database as the one that owns it. It is the name gorm's naming
// strategy gives readOnlyProbeRow as well as the one its TableName returns, since the ORM's
// All and Count name the table from a nil entity, on which TableName cannot be called.
const readOnlyProbe = "read_only_probe_rows"

type readOnlyProbeRow struct {
	orm.BaseEntity
	ID    int64  `gorm:"primaryKey;autoIncrement;column:id"`
	Label string `gorm:"column:label;size:64"`
}

func (readOnlyProbeRow) TableName() string { return readOnlyProbe }

// readOnlyConfig is config() with read_only: true.
func readOnlyConfig(config func() map[string]any) map[string]any {
	cfg := config()
	cfg["read_only"] = true
	return cfg
}

// assertReadOnlyRefusesWrites writes to the probe every way an app can through readOnly —
// the builder, raw SQL, the gorm handle GetDriver returns, the ORM, and a transaction — and
// asserts that each is refused with core.ErrReadOnly and that the owner's row is all the
// table holds afterwards. Then it reads the probe every way an app can, which must work.
func assertReadOnlyRefusesWrites(t *testing.T, owner, readOnly dbCore.IDataSource) {
	t.Helper()

	ownerGorm := gormOf(t, owner)
	reset := func() { assert.NoError(t, ownerGorm.Migrator().DropTable(readOnlyProbe)) }
	reset()
	t.Cleanup(reset)
	require.NoError(t, ownerGorm.AutoMigrate(&readOnlyProbeRow{}))
	require.NoError(t, ownerGorm.Create(&readOnlyProbeRow{Label: "owner"}).Error)

	require.True(t, dbCore.IsReadOnly(readOnly))
	session, err := readOnly.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Close()) })
	ctx := context.Background()
	driverGorm := gormOf(t, readOnly)

	writes := map[string]func() error{
		"builder Exec": func() error {
			return session.Executor().Exec(ctx, session.Query().Insert(readOnlyProbe).Columns("label").Values("builder")).Error
		},
		"builder Exec of an UPDATE": func() error {
			return session.Executor().Exec(ctx, session.Query().Update(readOnlyProbe).Set("label", "builder")).Error
		},
		"ExecRaw": func() error {
			return session.Executor().ExecRaw(ctx, "UPDATE "+readOnlyProbe+" SET label = ?", "raw").Error
		},
		"GetDriver().Exec": func() error {
			return driverGorm.Exec("DELETE FROM " + readOnlyProbe).Error
		},
		"gorm Create": func() error {
			return driverGorm.Create(&readOnlyProbeRow{Label: "gorm"}).Error
		},
		"ORM Create": func() error {
			return orm.New[*readOnlyProbeRow](session).Create(&readOnlyProbeRow{Label: "orm"})
		},
		"a transaction": func() error {
			return session.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
				return tx.ExecRaw(ctx, "INSERT INTO "+readOnlyProbe+" (label) VALUES ('tx')").Error
			})
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, write(), dbCore.ErrReadOnly)
		})
	}

	var labels []string
	require.NoError(t, ownerGorm.Model(&readOnlyProbeRow{}).Order("id").Pluck("label", &labels).Error)
	assert.Equal(t, []string{"owner"}, labels, "no write may reach the table")

	t.Run("reads", func(t *testing.T) {
		var found []readOnlyProbeRow
		res := session.Executor().Find(ctx, session.Query().Select("id", "label").From(readOnlyProbe), &found)
		require.NoError(t, res.Error)
		require.Len(t, found, 1)
		assert.Equal(t, "owner", found[0].Label)

		var raw []readOnlyProbeRow
		require.NoError(t, session.Executor().FindRaw(ctx, &raw, "SELECT id, label FROM "+readOnlyProbe+" WHERE label = ?", "owner").Error)
		assert.Len(t, raw, 1)

		count, err := session.Executor().Count(ctx, session.Query().Select("COUNT(*)").From(readOnlyProbe))
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)

		all, err := orm.New[*readOnlyProbeRow](session).All()
		require.NoError(t, err)
		require.Len(t, all, 1)
		assert.Equal(t, "owner", all[0].Label)
		ormCount, err := orm.New[*readOnlyProbeRow](session).Count()
		require.NoError(t, err)
		assert.Equal(t, int64(1), ormCount)

		require.NoError(t, session.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
			var inTx []readOnlyProbeRow
			if err := tx.Find(ctx, tx.Query().Select("id", "label").From(readOnlyProbe), &inTx).Error; err != nil {
				return err
			}
			assert.Len(t, inTx, 1)
			return nil
		}), "a read-only transaction commits")
	})
}

// TestReadOnlyRefusesWritesOnPostgres: the dialect, the guard and the server each refuse a
// write on a read_only Postgres datasource. The server's refusal is the one layer that sees a
// write sent on the *sql.DB that DB() returns, which the guard does not.
func TestReadOnlyRefusesWritesOnPostgres(t *testing.T) {
	owner := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return pgv2.NewDataSource(pgConfig())
	})
	t.Cleanup(func() { assert.NoError(t, owner.Close()) })
	readOnly, err := pgv2.NewDataSource(readOnlyConfig(pgConfig))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, readOnly.Close()) })

	assertReadOnlyRefusesWrites(t, owner, readOnly)

	t.Run("the server makes every transaction read-only", func(t *testing.T) {
		session, err := readOnly.NewSession()
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, session.Close()) })

		var setting []struct{ Setting string }
		require.NoError(t, session.Executor().FindRaw(context.Background(), &setting,
			"SELECT current_setting('default_transaction_read_only') AS setting").Error)
		require.Len(t, setting, 1)
		assert.Equal(t, "on", setting[0].Setting)

		sqlDB, err := gormOf(t, readOnly).DB()
		require.NoError(t, err)
		_, err = sqlDB.ExecContext(context.Background(), "INSERT INTO "+readOnlyProbe+" (label) VALUES ('bypass')")
		requireServerReadOnlyRefusal(t, err, "the guard does not see the *sql.DB")
	})

	// set_config is a SELECT that would switch the server's check off for good on the pooled
	// connection it ran on, and let every write sent there afterwards through: a writing
	// function through the guard, an INSERT on DB(). With one connection in the pool, each
	// statement below runs on the connection it would have switched.
	t.Run("set_config cannot switch the server's check off", func(t *testing.T) {
		cfg := readOnlyConfig(pgConfig)
		cfg["properties"] = map[string]any{"maxOpenConnections": 1}
		single, err := pgv2.NewDataSource(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, single.Close()) })
		session, err := single.NewSession()
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, session.Close()) })
		ctx := context.Background()

		const off = "set_config('default_transaction_read_only', 'off', false)"
		var rows []map[string]any
		assert.ErrorIs(t, session.Executor().FindRaw(ctx, &rows, "SELECT "+off).Error, dbCore.ErrReadOnly)
		assert.ErrorIs(t, session.Executor().Find(ctx, session.Query().Select(off), &rows).Error, dbCore.ErrReadOnly)
		assert.ErrorIs(t, gormOf(t, single).Raw("SELECT "+off).Scan(&rows).Error, dbCore.ErrReadOnly)

		var setting []struct{ Setting string }
		require.NoError(t, session.Executor().FindRaw(ctx, &setting,
			"SELECT current_setting('default_transaction_read_only') AS setting").Error)
		require.Len(t, setting, 1)
		assert.Equal(t, "on", setting[0].Setting)

		// A function that writes is no word the guard refuses; the server refuses what it does.
		err = session.Executor().FindRaw(ctx, &rows, "SELECT nextval(pg_get_serial_sequence('"+readOnlyProbe+"', 'id'))").Error
		requireServerReadOnlyRefusal(t, err, "the guard lets a function call through")
		sqlDB, err := gormOf(t, single).DB()
		require.NoError(t, err)
		_, err = sqlDB.ExecContext(ctx, "INSERT INTO "+readOnlyProbe+" (label) VALUES ('bypass')")
		requireServerReadOnlyRefusal(t, err, "the guard does not see the *sql.DB")
	})

	// An operator's own setting in options wins, spelt any way Postgres reads it, and an empty
	// key of its own sends nothing, as behind a PgBouncer that refuses the parameter. The
	// server then keeps its default, off, and the dialect and the guard refuse writes alone.
	t.Run("options decide what the server is asked", func(t *testing.T) {
		for name, options := range map[string]map[string]any{
			"an empty key, to send nothing": {"default_transaction_read_only": ""},
			"a -c switch spelt with dashes": {"options": "-c default-transaction-read-only=off"},
		} {
			t.Run(name, func(t *testing.T) {
				cfg := readOnlyConfig(pgConfig)
				cfg["options"] = options
				ds, err := pgv2.NewDataSource(cfg)
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, ds.Close()) })
				session, err := ds.NewSession()
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, session.Close()) })
				ctx := context.Background()

				var setting []struct{ Setting string }
				require.NoError(t, session.Executor().FindRaw(ctx, &setting,
					"SELECT current_setting('default_transaction_read_only') AS setting").Error)
				require.Len(t, setting, 1)
				assert.Equal(t, "off", setting[0].Setting)
				assert.ErrorIs(t, session.Executor().ExecRaw(ctx, "INSERT INTO "+readOnlyProbe+" (label) VALUES ('opt-out')").Error, dbCore.ErrReadOnly)
			})
		}
	})
}

// requireServerReadOnlyRefusal asserts err is Postgres's own refusal of a write in a read-only
// transaction, SQLSTATE 25006, which arrives as the driver's error: only gorgany's own
// refusals wrap core.ErrReadOnly.
func requireServerReadOnlyRefusal(t *testing.T, err error, why string) {
	t.Helper()

	require.Error(t, err, "past the guard, the server must refuse the write")
	assert.NotErrorIs(t, err, dbCore.ErrReadOnly, why)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	assert.Equal(t, "25006", pgErr.Code, "read_only_sql_transaction")
}

// TestReadOnlyRefusesWritesOnMySQL: the dialect and the guard refuse a write on a read_only
// MySQL datasource. The server is not asked to make the session read-only (see the MySQL
// BuildDSN), which this pins: the session variable stays off.
func TestReadOnlyRefusesWritesOnMySQL(t *testing.T) {
	requireMySQL(t)

	owner := waitForDatasource(t, func() (dbCore.IDataSource, error) {
		return mysqlv2.NewDataSource(mysqlConfig())
	})
	t.Cleanup(func() { assert.NoError(t, owner.Close()) })
	readOnly, err := mysqlv2.NewDataSource(readOnlyConfig(mysqlConfig))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, readOnly.Close()) })

	assertReadOnlyRefusesWrites(t, owner, readOnly)

	var readOnlySession []struct{ Value int64 }
	require.NoError(t, gormOf(t, readOnly).Raw("SELECT @@SESSION.transaction_read_only AS value").Scan(&readOnlySession).Error)
	require.Len(t, readOnlySession, 1)
	assert.Equal(t, int64(0), readOnlySession[0].Value)
}
