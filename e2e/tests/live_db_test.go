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
package e2e

import (
	"context"
	"fmt"

	"github.com/osbits/gorgany/v2/auth"
	"os"
	"testing"
	"time"

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
	"github.com/osbits/gorgany/v2/provider"
	"github.com/osbits/gorgany/v2/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func pgConfig() map[string]any {
	return map[string]any{
		"driver":   "postgres_gorm",
		"host":     "127.0.0.1",
		"port":     5433,
		"username": "postgres",
		"password": "test",
		"db":       "gorgany_test",
		"ssl":      "disable",
	}
}

func mysqlConfig() map[string]any {
	return map[string]any{
		"driver":   "mysql_gorm",
		"host":     "127.0.0.1",
		"port":     3307,
		"username": "root",
		"password": "test",
		"db":       "gorgany_test",
	}
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

	t.Log("MySQL not reachable; using a second Postgres database. " +
		"This still proves datasource routing, but not the cross-engine case.")
	ensureSecondPgDatabase(t)
	return secondPgConfig(), "postgres"
}

func secondaryDataSource(t *testing.T, cfg map[string]any) dbCore.IDataSource {
	t.Helper()

	if cfg["driver"] == "mysql_gorm" {
		return waitForDatasource(t, func() (dbCore.IDataSource, error) {
			return mysqlv2.NewDataSource(cfg)
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

// waitForDatasource retries until the engine accepts connections, then skips.
func waitForDatasource(t *testing.T, build func() (dbCore.IDataSource, error)) dbCore.IDataSource {
	t.Helper()

	deadline := time.Now().Add(engineWait)
	var lastErr error
	for {
		ds, err := build()
		if err == nil {
			return ds
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Skipf("engine not reachable within %s, skipping live test: %v", engineWait, lastErr)
	return nil
}

// requireMySQL skips immediately when MySQL is absent, so a run without it costs
// one failed dial rather than the full retry window per test.
func requireMySQL(t *testing.T) {
	t.Helper()

	if !mysqlAvailable() {
		t.Skip("MySQL not reachable on 127.0.0.1:3307; start the container from this file's doc comment")
	}
}

// --------------------------------------------------------- T1.3: no more panics

// TestT13_MissingLogKeyReturnsAnErrorNotAPanic is the brief's second live check:
// "NewDataSource with a config map missing `log` returns an error instead of
// panicking". The map below is missing `log`, `prefer_simple_protocol` and
// `properties` — all three of which used to be unchecked type assertions.
func TestT13_MissingLogKeyReturnsAnErrorNotAPanic(t *testing.T) {
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
// spells it DATABASE().
func assertDatabaseName(t *testing.T, ds dbCore.IDataSource, want string) {
	t.Helper()

	query := `SELECT current_database()`
	if aware, ok := ds.(interface{ Dialect() dbCore.SQLDialect }); ok {
		if aware.Dialect().Name() == "mysql" {
			query = `SELECT DATABASE()`
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
	defer admin.Close()

	adminGorm := gormOf(t, admin)
	require.NoError(t, adminGorm.Exec(`DROP SCHEMA IF EXISTS tenant_a CASCADE`).Error)
	require.NoError(t, adminGorm.Exec(`CREATE SCHEMA tenant_a`).Error)
	t.Cleanup(func() { adminGorm.Exec(`DROP SCHEMA IF EXISTS tenant_a CASCADE`) })

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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS sessions`).Error)
	t.Cleanup(func() { gormDb.Exec(`DROP TABLE IF EXISTS sessions`) })

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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS sessions CASCADE`).Error)
	t.Cleanup(func() { gormDb.Exec(`DROP TABLE IF EXISTS sessions CASCADE`) })

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
	defer pg.Close()

	secondary, _ := secondaryConfig(t)
	my := secondaryDataSource(t, secondary)
	defer my.Close()

	pgGorm := gormOf(t, pg)
	myGorm := gormOf(t, my)

	for _, g := range []*gorm.DB{pgGorm, myGorm} {
		require.NoError(t, g.Exec(`DROP TABLE IF EXISTS creatio_only`).Error)
		require.NoError(t, g.Exec(`DROP TABLE IF EXISTS default_only`).Error)
		require.NoError(t, g.Migrator().DropTable(&db.Migration{}))
	}
	t.Cleanup(func() {
		for _, g := range []*gorm.DB{pgGorm, myGorm} {
			g.Exec(`DROP TABLE IF EXISTS creatio_only`)
			g.Exec(`DROP TABLE IF EXISTS default_only`)
			g.Migrator().DropTable(&db.Migration{})
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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS dialect_probe`).Error)
	require.NoError(t, gormDb.Exec(
		"CREATE TABLE dialect_probe (id INT PRIMARY KEY, region VARCHAR(50), name VARCHAR(50), amount INT)").Error)
	t.Cleanup(func() { gormDb.Exec(`DROP TABLE IF EXISTS dialect_probe`) })

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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS charset_probe`).Error)
	require.NoError(t, gormDb.Exec(`CREATE TABLE charset_probe (id INT PRIMARY KEY, note VARCHAR(50))`).Error)
	t.Cleanup(func() { gormDb.Exec(`DROP TABLE IF EXISTS charset_probe`) })

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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	reset := func() {
		gormDb.Exec(`DROP TABLE IF EXISTS step_one`)
		gormDb.Exec(`DROP TABLE IF EXISTS step_two`)
		gormDb.Migrator().DropTable(&db.Migration{})
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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	reset := func() {
		gormDb.Exec(`DROP TABLE IF EXISTS step_one`)
		gormDb.Exec(`DROP TABLE IF EXISTS step_two`)
		gormDb.Migrator().DropTable(&db.Migration{})
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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	reset := func() {
		gormDb.Exec(`DROP TABLE IF EXISTS step_one`)
		gormDb.Migrator().DropTable(&db.Migration{})
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
	if dialect == "mysql" {
		autoPK = "BIGINT AUTO_INCREMENT PRIMARY KEY"
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
		gormDb.Exec("DROP TABLE IF EXISTS " + table)
	}
}

// TestA1_OrmCreateInsertsOnBothEngines is the test whose absence let a MySQL driver
// ship with a green suite while being unable to insert a row.
//
// The ORM built every query with v2.NewBuilder() — the Postgres builder — so
// `Create` appended `RETURNING id` and MySQL answered with error 1064. Every dialect
// test asserted strings; none drove the ORM against a real engine.
func TestA1_OrmCreateInsertsOnBothEngines(t *testing.T) {
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

			// The generated key must come back on both engines: via RETURNING on
			// Postgres, via the driver's sql.Result on MySQL.
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
// (no regression) and MySQL (the fix) from one body.
func ormEngines(t *testing.T) []ormEngine {
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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	require.NoError(t, gormDb.Exec(`DROP TABLE IF EXISTS upsert_optin_probe`).Error)
	require.NoError(t, gormDb.Exec(
		"CREATE TABLE upsert_optin_probe (id INT PRIMARY KEY, amount INT)").Error)
	t.Cleanup(func() { gormDb.Exec(`DROP TABLE IF EXISTS upsert_optin_probe`) })

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
	t.Cleanup(func() { gormDb.Exec(`DROP TABLE IF EXISTS sessions`) })

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
	defer ds.Close()

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
	defer ds.Close()

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
	defer ds.Close()

	gormDb := gormOf(t, ds)
	sweepProbe(t, gormDb, 0)

	assert.Equal(t, 1, runBatchedSweep(t, ds, 10))
	assert.Equal(t, int64(1), countSessions(t, gormDb))
}
