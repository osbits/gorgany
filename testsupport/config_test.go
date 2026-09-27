package testsupport

import (
	"fmt"
	"testing"
	"time"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These run without any engine: they cover the configuration layer, which is where a
// harness most often goes wrong silently.

func TestFromEnvDefaultsToThePostgresContainer(t *testing.T) {
	clearHarnessEnv(t)

	cfg := FromEnv()
	require.Len(t, cfg.Databases, 1)

	assert.Equal(t, DriverPostgres, cfg.Databases[0].Driver)
	assert.Equal(t, "127.0.0.1", cfg.Databases[0].Host)
	assert.Equal(t, 5433, cfg.Databases[0].Port)
	assert.Equal(t, "postgres", cfg.Databases[0].User)
	assert.Equal(t, "gorgany_test", cfg.Databases[0].Database)
	assert.Equal(t, IsolateByTruncation, cfg.Isolation, "truncation is the safe default")
}

// TestTheMySqlDefaultsFollowTheDriver: switching one variable must not require setting
// the port and user too.
func TestTheMySqlDefaultsFollowTheDriver(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvDriver, DriverMySQL)

	cfg := FromEnv()
	assert.Equal(t, 3307, cfg.Databases[0].Port)
	assert.Equal(t, "root", cfg.Databases[0].User)
}

func TestEveryVariableIsHonoured(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvDriver, DriverMySQL)
	t.Setenv(EnvHost, "db.internal")
	t.Setenv(EnvPort, "13306")
	t.Setenv(EnvUser, "tester")
	t.Setenv(EnvPassword, "s3cret")
	t.Setenv(EnvDatabase, "app_test")
	t.Setenv(EnvIsolation, string(IsolateByRollback))
	t.Setenv(EnvEngineWait, "5s")
	t.Setenv(EnvKeepData, "true")
	t.Setenv(EnvMigrateDown, "1")
	t.Setenv(EnvAllowAnyTarget, "true")

	cfg := FromEnv()
	assert.Equal(t, DriverMySQL, cfg.Databases[0].Driver)
	assert.Equal(t, "db.internal", cfg.Databases[0].Host)
	assert.Equal(t, 13306, cfg.Databases[0].Port)
	assert.Equal(t, "tester", cfg.Databases[0].User)
	assert.Equal(t, "s3cret", cfg.Databases[0].Password)
	assert.Equal(t, "app_test", cfg.Databases[0].Database)
	assert.Equal(t, IsolateByRollback, cfg.Isolation)
	assert.Equal(t, 5*time.Second, cfg.EngineWait)
	assert.True(t, cfg.KeepData)
	assert.True(t, cfg.MigrateDown)
	assert.True(t, cfg.AllowAnyTarget)
}

// TestAnExplicitlyEmptyPasswordIsARealValue. Substituting the default for it would
// silently connect as something else — the same class of bug as A5's os.Getenv.
func TestAnExplicitlyEmptyPasswordIsARealValue(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvPassword, "")

	assert.Equal(t, "", FromEnv().Databases[0].Password)
}

// TestAnUnparseablePortFallsBackRatherThanConnectingToZero.
func TestAnUnparseablePortFallsBackRatherThanConnectingToZero(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvPort, "not-a-number")

	assert.Equal(t, 5433, FromEnv().Databases[0].Port)
}

func TestAnUnknownIsolationIsRejected(t *testing.T) {
	_, err := Config{
		Databases: []DatabaseConfig{{Driver: DriverPostgres, Host: "h", Port: 1, Database: "d"}},
		Isolation: "wishful",
	}.resolved()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown isolation")
	assert.Contains(t, err.Error(), string(IsolateByTruncation))
	assert.Contains(t, err.Error(), string(IsolateByRollback))
}

// TestAnIncompleteDatabaseConfigNamesWhatIsMissing. A harness that connects to nothing and
// says "not reachable" sends people looking at Docker instead of at their config.
func TestAnIncompleteDatabaseConfigNamesWhatIsMissing(t *testing.T) {
	_, err := Config{Databases: []DatabaseConfig{{Driver: DriverPostgres}}}.resolved()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Host")
	assert.Contains(t, err.Error(), "Port")
	assert.Contains(t, err.Error(), "Database")
}

func TestResolvedFillsTheDefaults(t *testing.T) {
	clearHarnessEnv(t)

	cfg, err := Config{}.resolved()
	require.NoError(t, err)

	assert.Len(t, cfg.Databases, 1, "an empty Databases means one entry from the environment")
	assert.Equal(t, IsolateByTruncation, cfg.Isolation)
	assert.Equal(t, DefaultEngineWait, cfg.EngineWait)
	assert.False(t, cfg.KeepData)
	assert.False(t, cfg.MigrateDown)
	assert.False(t, cfg.AllowAnyTarget, "the target guard is on unless something switches it off")
}

// TestTheDefaultHarnessHonoursEveryVariable walks the path Main, RequireDatabase and
// MustDatabase take. The default harness is New(Config{}), and FromEnv reading a variable
// is no use if resolving that harness throws the value away — which it did for everything
// but the connection settings, so GORGANY_TEST_ENGINE_WAIT=2s still waited 30s.
//
// Each variable is set on its own, to a value that is not its default, so a pass cannot
// come from the default happening to match.
func TestTheDefaultHarnessHonoursEveryVariable(t *testing.T) {
	cases := []struct {
		variable string
		value    string
		check    func(t *testing.T, cfg Config)
	}{
		{EnvDriver, DriverMySQL, func(t *testing.T, cfg Config) {
			assert.Equal(t, DriverMySQL, cfg.Databases[0].Driver)
			assert.Equal(t, 3307, cfg.Databases[0].Port, "the port default follows the driver")
			assert.Equal(t, "root", cfg.Databases[0].User, "the user default follows the driver")
		}},
		{EnvHost, "db.internal", func(t *testing.T, cfg Config) {
			assert.Equal(t, "db.internal", cfg.Databases[0].Host)
		}},
		{EnvPort, "15432", func(t *testing.T, cfg Config) {
			assert.Equal(t, 15432, cfg.Databases[0].Port)
		}},
		{EnvUser, "tester", func(t *testing.T, cfg Config) {
			assert.Equal(t, "tester", cfg.Databases[0].User)
		}},
		{EnvPassword, "s3cret", func(t *testing.T, cfg Config) {
			assert.Equal(t, "s3cret", cfg.Databases[0].Password)
		}},
		{EnvDatabase, "app_test", func(t *testing.T, cfg Config) {
			assert.Equal(t, "app_test", cfg.Databases[0].Database)
		}},
		{EnvSSL, "require", func(t *testing.T, cfg Config) {
			assert.Equal(t, "require", cfg.Databases[0].SSL)
		}},
		{EnvIsolation, string(IsolateByRollback), func(t *testing.T, cfg Config) {
			assert.Equal(t, IsolateByRollback, cfg.Isolation)
		}},
		{EnvEngineWait, "2s", func(t *testing.T, cfg Config) {
			assert.Equal(t, 2*time.Second, cfg.EngineWait)
		}},
		{EnvKeepData, "true", func(t *testing.T, cfg Config) {
			assert.True(t, cfg.KeepData)
		}},
		{EnvMigrateDown, "1", func(t *testing.T, cfg Config) {
			assert.True(t, cfg.MigrateDown)
		}},
		{EnvAllowAnyTarget, "1", func(t *testing.T, cfg Config) {
			assert.True(t, cfg.AllowAnyTarget)
		}},
	}

	for _, c := range cases {
		t.Run(c.variable, func(t *testing.T) {
			clearHarnessEnv(t)
			t.Setenv(c.variable, c.value)

			cfg, err := New(Config{}).resolve()
			require.NoError(t, err)
			require.Len(t, cfg.Databases, 1)
			c.check(t, cfg)
		})
	}
}

// TestAnEmptyVariableMeansTheDefault. A CI matrix that leaves a cell blank exports the
// variable as "", and for these that must mean "unset", not an invalid value.
func TestAnEmptyVariableMeansTheDefault(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvIsolation, "")
	t.Setenv(EnvEngineWait, "")

	cfg, err := New(Config{}).resolve()
	require.NoError(t, err)
	assert.Equal(t, IsolateByTruncation, cfg.Isolation)
	assert.Equal(t, DefaultEngineWait, cfg.EngineWait)
}

// TestABadIsolationVariableFailsTheDefaultHarness. It used to be ignored on this path, so a
// typo silently ran the suite under truncation; a bad config must be reported as one.
func TestABadIsolationVariableFailsTheDefaultHarness(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvIsolation, "rolback")

	_, err := New(Config{}).resolve()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown isolation "rolback"`)
}

// TestAPartialConfigTakesTheRestFromTheEnvironment. Configure(Config{Isolation: ...}) is
// the documented way to change isolation, and it must not switch every other variable off.
func TestAPartialConfigTakesTheRestFromTheEnvironment(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvHost, "db.internal")
	t.Setenv(EnvIsolation, string(IsolateByTruncation))
	t.Setenv(EnvEngineWait, "2s")
	t.Setenv(EnvKeepData, "1")
	t.Setenv(EnvMigrateDown, "1")
	t.Setenv(EnvAllowAnyTarget, "1")

	cfg, err := New(Config{Isolation: IsolateByRollback}).resolve()
	require.NoError(t, err)

	assert.Equal(t, IsolateByRollback, cfg.Isolation, "the field that was set keeps its value")
	assert.Equal(t, "db.internal", cfg.Databases[0].Host)
	assert.Equal(t, 2*time.Second, cfg.EngineWait)
	assert.True(t, cfg.KeepData)
	assert.True(t, cfg.MigrateDown)
	assert.True(t, cfg.AllowAnyTarget)
}

// TestAnExplicitConfigKeepsItsValues. The environment fills what a suite left unset; it
// does not override what the suite chose.
func TestAnExplicitConfigKeepsItsValues(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvDriver, DriverMySQL)
	t.Setenv(EnvHost, "from-env")
	t.Setenv(EnvPort, "13306")
	t.Setenv(EnvDatabase, "env_db")
	t.Setenv(EnvIsolation, string(IsolateByTruncation))
	t.Setenv(EnvEngineWait, "2s")
	t.Setenv(EnvKeepData, "false")
	t.Setenv(EnvMigrateDown, "0")
	t.Setenv(EnvAllowAnyTarget, "0")

	explicit := DatabaseConfig{
		Name: "primary", Driver: DriverPostgres,
		Host: "explicit", Port: 6543, User: "u", Password: "p", Database: "explicit_db",
	}

	cfg, err := New(Config{
		Databases:      []DatabaseConfig{explicit},
		Isolation:      IsolateByRollback,
		EngineWait:     7 * time.Second,
		KeepData:       true,
		MigrateDown:    true,
		AllowAnyTarget: true,
	}).resolve()
	require.NoError(t, err)

	assert.Equal(t, []DatabaseConfig{explicit}, cfg.Databases)
	assert.Equal(t, IsolateByRollback, cfg.Isolation)
	assert.Equal(t, 7*time.Second, cfg.EngineWait)
	assert.True(t, cfg.KeepData, "a variable that is off does not turn off what the suite turned on")
	assert.True(t, cfg.MigrateDown)
	assert.True(t, cfg.AllowAnyTarget)
}

func TestTheDatasourceConfigMatchesWhatTheRegistryExpects(t *testing.T) {
	pg := DatabaseConfig{
		Driver: DriverPostgres, Host: "h", Port: 5432,
		User: "u", Password: "p", Database: "d",
	}.datasourceConfig()

	assert.Equal(t, DriverPostgres, pg["driver"])
	assert.Equal(t, "d", pg["db"])
	assert.Equal(t, "u", pg["username"])
	assert.Equal(t, "disable", pg["ssl"], "an unset sslmode must not become empty")

	mysql := DatabaseConfig{
		Driver: DriverMySQL, Host: "h", Port: 3306, Database: "d",
	}.datasourceConfig()

	assert.NotContains(t, mysql, "ssl", "MySQL has no sslmode; sending one would be a config error")
}

func TestALabelFallsBackToTheDriver(t *testing.T) {
	assert.Equal(t, DriverMySQL, DatabaseConfig{Driver: DriverMySQL}.Label())
	assert.Equal(t, "primary", DatabaseConfig{Name: "primary", Driver: DriverMySQL}.Label())
}

// ------------------------------------------------------------------ target guard

// localTarget is a database the guard accepts: a local engine the harness supports, under a
// name any driver allows.
func localTarget(driver, database string) DatabaseConfig {
	return DatabaseConfig{Driver: driver, Host: "127.0.0.1", Port: 14330, User: "u", Database: database}
}

// TestTheHarnessRefusesAnAzureSQLHost. The harness empties every table its migrations
// create, so a production variable left in the shell must not be enough to point it at a
// cloud database, whichever engine the suite configured and however it was configured.
func TestTheHarnessRefusesAnAzureSQLHost(t *testing.T) {
	for _, host := range []string{
		"example.database.windows.net",
		"EXAMPLE.database.windows.net.",
		"tcp:example.database.windows.net,1433",
		"example.database.usgovcloudapi.net",
	} {
		t.Run(host, func(t *testing.T) {
			clearHarnessEnv(t)

			for _, driver := range []string{DriverPostgres, DriverMySQL, driverSQLServer} {
				target := localTarget(driver, "gorgany_test")
				target.Host = host

				_, err := Config{Databases: []DatabaseConfig{target}}.resolved()
				require.Error(t, err, "%s on %s", host, driver)
				assert.Contains(t, err.Error(), "an Azure SQL host")
				assert.Contains(t, err.Error(), EnvAllowAnyTarget, "the refusal names the way out")
			}
		})
	}

	t.Run("from the environment", func(t *testing.T) {
		clearHarnessEnv(t)
		t.Setenv(EnvHost, "example.database.windows.net")

		_, err := New(Config{}).resolve()
		require.Error(t, err, "the default harness is guarded too")
		assert.Contains(t, err.Error(), "an Azure SQL host")
	})
}

// TestTheHarnessRefusesAnAzureDatabaseForPostgresOrMySQLHost: IsAzureSQLHost answers for Azure
// SQL alone, hosts a Postgres or MySQL suite cannot reach; the Azure hosts those suites can
// reach are the managed Postgres and MySQL ones, and a cloud database is no more disposable
// for being Postgres.
func TestTheHarnessRefusesAnAzureDatabaseForPostgresOrMySQLHost(t *testing.T) {
	clearHarnessEnv(t)

	for driver, hosts := range map[string][]string{
		DriverPostgres: {"example.postgres.database.azure.com", "EXAMPLE.postgres.database.azure.com.", "example.postgres.database.azure.com:5432"},
		DriverMySQL:    {"example.mysql.database.azure.com", "db.internal,example.mysql.database.azure.com"},
	} {
		for _, host := range hosts {
			target := localTarget(driver, "gorgany_test")
			target.Host = host

			_, err := Config{Databases: []DatabaseConfig{target}}.resolved()
			require.Error(t, err, "%s on %s", host, driver)
			assert.Contains(t, err.Error(), "an Azure Database for PostgreSQL or MySQL host")
			assert.Contains(t, err.Error(), EnvAllowAnyTarget)
		}
	}

	for _, host := range []string{"postgres.database.azure.com", "example.postgres.database.azure.com.example.org"} {
		target := localTarget(DriverPostgres, "gorgany_test")
		target.Host = host
		_, err := Config{Databases: []DatabaseConfig{target}}.resolved()
		assert.NoError(t, err, "%s names no Azure server", host)
	}
}

// TestAHostThatOnlyResemblesAzureIsAccepted, since the guard is a refusal and a false
// positive would stop a suite that did nothing wrong.
func TestAHostThatOnlyResemblesAzureIsAccepted(t *testing.T) {
	clearHarnessEnv(t)

	for _, host := range []string{"database.windows.net.example.com", "db.internal", "localhost"} {
		target := localTarget(DriverPostgres, "gorgany_test")
		target.Host = host

		_, err := Config{Databases: []DatabaseConfig{target}}.resolved()
		assert.NoError(t, err, host)
	}
}

// TestTheHarnessRefusesAnUnsupportedDriver. Isolation has to know how to empty an engine's
// tables, and before the guard an unknown driver was emptied as though it were MySQL.
func TestTheHarnessRefusesAnUnsupportedDriver(t *testing.T) {
	clearHarnessEnv(t)

	_, err := Config{Databases: []DatabaseConfig{localTarget("oracle_gorm", "gorgany_test")}}.resolved()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `driver "oracle_gorm" is not one the harness supports`)
	for _, supported := range []string{DriverPostgres, DriverMySQL, driverSQLServer} {
		assert.Contains(t, err.Error(), supported)
	}
	assert.Contains(t, err.Error(), EnvAllowAnyTarget)

	t.Run("from the environment", func(t *testing.T) {
		clearHarnessEnv(t)
		t.Setenv(EnvDriver, "oracle_gorm")

		_, err := New(Config{}).resolve()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not one the harness supports")
	})
}

// TestTheHarnessRefusesASQLServerDatabaseNotNamedForTests. One SQL Server instance commonly
// hosts databases that are not disposable, so there the name must say this one is.
func TestTheHarnessRefusesASQLServerDatabaseNotNamedForTests(t *testing.T) {
	clearHarnessEnv(t)

	// "test" inside another word is not a name for tests: each of these could be a production
	// database on a shared instance.
	for _, name := range []string{"Example-db", "legacy", "orders", "Attestations", "LatestOrders", "Contests", "Protest", "testdb"} {
		_, err := Config{Databases: []DatabaseConfig{localTarget(driverSQLServer, name)}}.resolved()
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), `refusing SQL Server database "`+name+`"`)
		assert.Contains(t, err.Error(), `"test" in its name as a word of its own`)
		assert.Contains(t, err.Error(), EnvAllowAnyTarget)
	}

	// This binary does not register the SQL Server driver, which a resolved config would also
	// need, so the names that pass are checked against the name rule alone.
	for _, name := range []string{"gorgany_test", "TestDb", "AppTests", "APITests", "e2e-testing", "test1", "orders.test", "TEST_orders"} {
		assert.NoError(t, guardSQLServerDatabase(driverSQLServer, name), name)
	}

	for _, name := range []string{"master", "MODEL", "msdb", "tempdb"} {
		_, err := Config{Databases: []DatabaseConfig{localTarget(driverSQLServer, name)}}.resolved()
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "a SQL Server system database", name)
	}
}

// TestTheSQLServerNameRuleLeavesTheOtherEnginesAlone. Postgres and MySQL suites predate the
// rule, and a database called "app" there is not a reason to fail them.
func TestTheSQLServerNameRuleLeavesTheOtherEnginesAlone(t *testing.T) {
	clearHarnessEnv(t)

	for _, driver := range []string{DriverPostgres, DriverMySQL} {
		_, err := Config{Databases: []DatabaseConfig{localTarget(driver, "legacy")}}.resolved()
		assert.NoError(t, err, driver)
	}
}

// TestAnUnregisteredDriverNamesItsImport. A driver that passes every safety rule still cannot
// connect when nothing in the test binary registers it. It used to be retried for the whole
// engine wait and then skipped, as though the engine were down; the fix is an import, and the
// error says which.
//
// The engine is one no binary registers, so the answer does not depend on what this test
// binary links; the SQL Server wording, which is what an app meets first, is checked on the
// hint itself for the same reason.
func TestAnUnregisteredDriverNamesItsImport(t *testing.T) {
	clearHarnessEnv(t)

	_, err := Config{
		Databases:      []DatabaseConfig{localTarget("app_engine", "gorgany_test")},
		AllowAnyTarget: true,
	}.resolved()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `driver "app_engine" is not registered in this test binary`)
	assert.Contains(t, err.Error(), DriverPostgres, "and says what is registered")
	assert.Contains(t, err.Error(), "call driver.Register")

	hint := registrationHint(driverSQLServer)
	assert.Contains(t, hint, `_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"`)
	assert.Contains(t, hint, "driver/builtin", "why testsupport's own import does not cover it")
}

// TestAllowAnyTargetOptsOut of every safety rule, set in code or by its variable, and of
// nothing else: a driver that is not registered cannot connect whatever the guard allows.
func TestAllowAnyTargetOptsOut(t *testing.T) {
	azure := localTarget(DriverPostgres, "legacy")
	azure.Host = "example.database.windows.net"

	for _, how := range []string{"field", "variable"} {
		t.Run(how, func(t *testing.T) {
			clearHarnessEnv(t)

			cfg := Config{Databases: []DatabaseConfig{azure}}
			if how == "field" {
				cfg.AllowAnyTarget = true
			} else {
				t.Setenv(EnvAllowAnyTarget, "1")
			}

			resolved, err := cfg.resolved()
			require.NoError(t, err, "an Azure host is allowed once the guard is off")
			assert.True(t, resolved.AllowAnyTarget)

			// Whether this binary registers SQL Server decides if this is an error at all;
			// what it must not be, either way, is the name rule's refusal.
			cfg.Databases = []DatabaseConfig{localTarget(driverSQLServer, "Example-db")}
			_, err = cfg.resolved()
			assert.NotContains(t, fmt.Sprint(err), "refusing", "the name rule is off")

			cfg.Databases = []DatabaseConfig{localTarget("app_engine", "gorgany_test")}
			_, err = cfg.resolved()
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "not one the harness supports", "the driver rule is off")
			assert.Contains(t, err.Error(), "is not registered", "the registration rule is not")
		})
	}
}

// TestAnUnregisteredDriverIsNotRetried. waitForEngine used to retry every error for the whole
// engine wait, including a lookup that fails the same way every time, and prepare then
// skipped. It now returns at once, as a config error that prepare does not skip.
func TestAnUnregisteredDriverIsNotRetried(t *testing.T) {
	target := localTarget("app_engine", "gorgany_test")

	started := time.Now()
	_, err := waitForEngine(target, dsconfig.DataSource{Driver: target.Driver, Host: target.Host,
		Port: target.Port, Database: target.Database}, time.Minute)
	require.Error(t, err)

	assert.Less(t, time.Since(started), 5*time.Second, "a registry miss must not use the wait")
	assert.True(t, isConfigError(err), "a missing registration is a config error, not an absent engine")
	assert.Contains(t, err.Error(), "is not registered")
}

// TestARefusedSettingIsNotRetried. A driver's UnsupportedError is its answer to the config,
// the same on every attempt, so it too must not use the wait. Postgres refuses `instance`
// before it dials, which makes it a refusal that needs no engine.
func TestARefusedSettingIsNotRetried(t *testing.T) {
	target := localTarget(DriverPostgres, "gorgany_test")
	parsed, err := dsconfig.Parse(target.datasourceConfig())
	require.NoError(t, err)
	parsed.Instance = "SQLEXPRESS"

	started := time.Now()
	_, err = waitForEngine(target, parsed, time.Minute)
	require.Error(t, err)

	assert.Less(t, time.Since(started), 5*time.Second)
	assert.True(t, isConfigError(err))
	var unsupported *dbCore.UnsupportedError
	assert.ErrorAs(t, err, &unsupported, "the driver's refusal is kept")
}

// TestAConfigErrorFromConnectIsKept, so prepare can tell it from an absent engine. connect is
// what prepare reads, and it caches per engine, so the entry is removed afterwards.
func TestAConfigErrorFromConnectIsKept(t *testing.T) {
	target := localTarget("app_engine", "gorgany_config_error_test")
	t.Cleanup(func() {
		enginesMu.Lock()
		defer enginesMu.Unlock()
		for key, e := range engines {
			if e.config == target {
				delete(engines, key)
			}
		}
	})

	e := connect(Config{EngineWait: time.Minute}, target)
	require.Error(t, e.err)
	assert.True(t, isConfigError(e.err))
	assert.Nil(t, e.datasource)
}

// TestTruncateRefusesAnUnknownDriver. It used to empty anything that was not Postgres as
// though it were MySQL. Neither statement is sent here: the refusal comes before the handle,
// which these fixtures leave nil, would be used.
func TestTruncateRefusesAnUnknownDriver(t *testing.T) {
	unknown := &Database{config: DatabaseConfig{Driver: "app_engine"}, tables: []string{"widgets"}}
	err := unknown.truncate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot empty tables on driver "app_engine"`)
	assert.Contains(t, err.Error(), "IsolateByRollback", "and names the isolation that works")

	sqlServer := &Database{config: DatabaseConfig{Driver: driverSQLServer}, tables: []string{"widgets"}}
	err = sqlServer.truncate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncation is not implemented for SQL Server yet")

	nothing := &Database{config: DatabaseConfig{Driver: "app_engine"}}
	assert.NoError(t, nothing.truncate(), "with no tables there is nothing to refuse")
}

// clearHarnessEnv unsets every variable the harness reads, so a developer's own
// GORGANY_TEST_* settings do not change what these tests assert.
func clearHarnessEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		EnvDriver, EnvHost, EnvPort, EnvUser, EnvPassword, EnvDatabase,
		EnvSSL, EnvIsolation, EnvEngineWait, EnvKeepData, EnvMigrateDown, EnvAllowAnyTarget,
	} {
		t.Setenv(name, "")
		require.NoError(t, unset(name))
	}
}
