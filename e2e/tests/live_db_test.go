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
	"os"
	"testing"
	"time"

	"github.com/osbits/gorgany/app/core"
	dbCmd "github.com/osbits/gorgany/command/db"
	"github.com/osbits/gorgany/db"
	"github.com/osbits/gorgany/db/migration"
	dsconfig "github.com/osbits/gorgany/db/sql/config"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	_ "github.com/osbits/gorgany/db/sql/driver/builtin"
	mysqlv2 "github.com/osbits/gorgany/db/sql/gorm/mysql/v2"
	pgv2 "github.com/osbits/gorgany/db/sql/gorm/postgres/v2"
	"github.com/osbits/gorgany/provider"
	"github.com/osbits/gorgany/service"
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

// waitForDatasource retries until the engine accepts connections.
func waitForDatasource(t *testing.T, build func() (dbCore.IDataSource, error)) dbCore.IDataSource {
	t.Helper()

	var lastErr error
	for attempt := 0; attempt < 60; attempt++ {
		ds, err := build()
		if err == nil {
			return ds
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	t.Skipf("engine not reachable after 60s, skipping live test: %v", lastErr)
	return nil
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
func assertDatabaseName(t *testing.T, ds dbCore.IDataSource, want string) {
	t.Helper()

	var got string
	require.NoError(t, gormOf(t, ds).Raw(`SELECT current_database()`).Scan(&got).Error)
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

	// INSERT with ON DUPLICATE KEY UPDATE (translated from ON CONFLICT).
	insert := session.Query().
		Insert("dialect_probe").
		Columns("id", "region", "name", "amount").
		Values(1, "north", "ann", 10).
		OnConflict("id").
		DoUpdate(map[string]interface{}{"amount": 20})

	result := session.Executor().Exec(ctxBackground(), insert)
	require.NoError(t, result.Error, "the translated upsert must be valid MySQL")

	// The same statement again exercises the ON DUPLICATE KEY branch.
	require.NoError(t, session.Executor().Exec(ctxBackground(), insert).Error)

	var amount int
	require.NoError(t, gormDb.Raw(`SELECT amount FROM dialect_probe WHERE id = 1`).Scan(&amount).Error)
	assert.Equal(t, 20, amount, "the second insert must have taken the UPDATE branch")

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

	previousArgs := os.Args
	os.Args = []string{"cli", "db:migrate", "up", "--datasource=" + datasource}
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
		"db:migrate up --datasource=%s must succeed", datasource)
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
