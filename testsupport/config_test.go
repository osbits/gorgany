package testsupport

import (
	"errors"
	"fmt"
	"strings"
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
	assert.Equal(t, "test", cfg.Databases[0].Password)
	assert.Equal(t, "gorgany_test", cfg.Databases[0].Database)
	assert.Equal(t, "disable", cfg.Databases[0].SSL)
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
	assert.Equal(t, "test", cfg.Databases[0].Password)
	assert.Equal(t, "disable", cfg.Databases[0].SSL, "MySQL ignores it, and it is what MySQL was given before")
}

// TestTheSQLServerDefaultsFollowTheDriver to the container in docs/TESTING.md, so that
// GORGANY_TEST_DRIVER is the only variable a developer sets to switch to it.
func TestTheSQLServerDefaultsFollowTheDriver(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvDriver, DriverSQLServer)

	cfg := FromEnv()
	require.Len(t, cfg.Databases, 1)
	database := cfg.Databases[0]

	assert.Equal(t, DriverSQLServer, database.Driver)
	assert.Equal(t, "127.0.0.1", database.Host)
	assert.Equal(t, 14330, database.Port)
	assert.Equal(t, "sa", database.User)
	assert.Equal(t, "Gorgany-Test-1", database.Password, "SQL Server refuses the others' \"test\"")
	assert.Equal(t, "gorgany_test", database.Database)
	assert.Equal(t, "true", database.SSL, "Postgres's disable would turn SQL Server's encryption off")
	assert.Empty(t, database.TrustServerCertificate, "empty means the default, which follows the host")
}

// TestAnExplicitPasswordStillWinsForSQLServer, the empty one included: the SQL Server default
// is one more default an explicitly empty variable must not be replaced by.
func TestAnExplicitPasswordStillWinsForSQLServer(t *testing.T) {
	clearHarnessEnv(t)
	t.Setenv(EnvDriver, DriverSQLServer)

	t.Setenv(EnvPassword, "")
	assert.Equal(t, "", FromEnv().Databases[0].Password)

	t.Setenv(EnvPassword, "An0ther-Secret")
	assert.Equal(t, "An0ther-Secret", FromEnv().Databases[0].Password)

	t.Setenv(EnvSSL, "")
	assert.Equal(t, "", FromEnv().Databases[0].SSL, "an explicitly empty SSL is the engine's own default")
	assert.NotContains(t, FromEnv().Databases[0].datasourceConfig(), "ssl")
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
	t.Setenv(EnvTrustServerCert, "false")

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
	assert.Equal(t, "false", cfg.Databases[0].TrustServerCertificate)
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
		{EnvTrustServerCert, "false", func(t *testing.T, cfg Config) {
			assert.Equal(t, "false", cfg.Databases[0].TrustServerCertificate)
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

// TestTheDatasourceConfigIsUnchangedForPostgresAndMySQL. SQL Server's keys are added for SQL
// Server alone: a Postgres or MySQL suite's connection must not change because the harness
// learned another engine, and neither server accepts trust_server_certificate.
func TestTheDatasourceConfigIsUnchangedForPostgresAndMySQL(t *testing.T) {
	for _, trust := range []string{"", "true", "false"} {
		pg := DatabaseConfig{
			Driver: DriverPostgres, Host: "h", Port: 5432, User: "u", Password: "p", Database: "d",
			TrustServerCertificate: trust,
		}
		assert.Equal(t, map[string]any{
			"driver": DriverPostgres, "host": "h", "port": 5432,
			"username": "u", "password": "p", "db": "d", "ssl": "disable",
		}, pg.datasourceConfig())

		mysql := DatabaseConfig{
			Driver: DriverMySQL, Host: "h", Port: 3306, User: "u", Password: "p", Database: "d",
			SSL: "disable", TrustServerCertificate: trust,
		}
		assert.Equal(t, map[string]any{
			"driver": DriverMySQL, "host": "h", "port": 3306,
			"username": "u", "password": "p", "db": "d",
		}, mysql.datasourceConfig())
	}
}

// TestTheSQLServerDatasourceConfigTrustsALocalContainer, whose certificate is self-signed, and
// leaves the encryption the engine's default rather than Postgres's disable.
func TestTheSQLServerDatasourceConfigTrustsALocalContainer(t *testing.T) {
	local := localTarget(DriverSQLServer, "gorgany_test")
	cfg := local.datasourceConfig()

	assert.Equal(t, map[string]any{
		"driver": DriverSQLServer, "host": "127.0.0.1", "port": 14330,
		"username": "u", "password": "", "db": "gorgany_test",
		"options": map[string]any{"trust_server_certificate": "true"},
	}, cfg, "an empty SSL is left out, so the connection is encrypted")

	parsed, err := dsconfig.Parse(cfg)
	require.NoError(t, err, "the registry must accept what the harness renders")
	assert.Equal(t, map[string]string{"trust_server_certificate": "true"}, parsed.Options)
	assert.Empty(t, parsed.SSL)

	local.SSL = "disable"
	assert.Equal(t, "disable", local.datasourceConfig()["ssl"], "an SSL the caller set is the caller's")
}

// TestTrustServerCertIsNotAddedForAzureHosts. The engine refuses a trusted certificate from an
// Azure SQL host, whose certificate always verifies, and with ssl strict, which verifies it by
// definition, so the default must not put one there. A value the caller set is rendered as set,
// for the engine to refuse where it must.
func TestTrustServerCertIsNotAddedForAzureHosts(t *testing.T) {
	trust := func(database DatabaseConfig) any {
		options, _ := database.datasourceConfig()["options"].(map[string]any)
		if options == nil {
			return nil
		}
		return options["trust_server_certificate"]
	}

	for _, host := range []string{"example.database.windows.net", "tcp:example.database.windows.net,1433"} {
		azure := localTarget(DriverSQLServer, "gorgany_test")
		azure.Host = host
		assert.Nil(t, trust(azure), host)
	}

	strict := localTarget(DriverSQLServer, "gorgany_test")
	strict.SSL = "Strict"
	assert.Nil(t, trust(strict), "ssl strict verifies the certificate")

	for value, rendered := range map[string]string{"false": "false", "0": "false", "FALSE": "false", "true": "true", "1": "true"} {
		explicit := localTarget(DriverSQLServer, "gorgany_test")
		explicit.TrustServerCertificate = value
		assert.Equal(t, rendered, trust(explicit), value)
	}

	explicitOnAzure := localTarget(DriverSQLServer, "gorgany_test")
	explicitOnAzure.Host = "example.database.windows.net"
	explicitOnAzure.TrustServerCertificate = "true"
	assert.Equal(t, "true", trust(explicitOnAzure), "the caller's value is not second-guessed")

	t.Run("from the environment", func(t *testing.T) {
		clearHarnessEnv(t)
		t.Setenv(EnvDriver, DriverSQLServer)
		t.Setenv(EnvTrustServerCert, "false")

		assert.Equal(t, "false", trust(FromEnv().Databases[0]), "the variable turns the default off")
	})
}

// TestATrustServerCertificateThatIsNotABooleanIsAConfigError. The engine refuses it too, but on
// every attempt to connect, so the harness would retry it for the whole engine wait and then
// skip, reporting a typo as an engine that is not running.
func TestATrustServerCertificateThatIsNotABooleanIsAConfigError(t *testing.T) {
	database := localTarget(DriverSQLServer, "gorgany_test")
	database.TrustServerCertificate = "maybe"

	err := database.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `TrustServerCertificate "maybe" is not true or false`)
	assert.Contains(t, err.Error(), EnvTrustServerCert)

	pg := localTarget(DriverPostgres, "gorgany_test")
	pg.TrustServerCertificate = "maybe"
	assert.NoError(t, pg.Validate(), "Postgres ignores the setting, so it does not check it")
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

			for _, driver := range []string{DriverPostgres, DriverMySQL, DriverSQLServer} {
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
	for _, supported := range []string{DriverPostgres, DriverMySQL, DriverSQLServer} {
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
		_, err := Config{Databases: []DatabaseConfig{localTarget(DriverSQLServer, name)}}.resolved()
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), `refusing SQL Server database "`+name+`"`)
		assert.Contains(t, err.Error(), `"test" in its name as a word of its own`)
		assert.Contains(t, err.Error(), EnvAllowAnyTarget)
	}

	// This binary does not register the SQL Server driver, which a resolved config would also
	// need, so the names that pass are checked against the name rule alone.
	for _, name := range []string{"gorgany_test", "TestDb", "AppTests", "APITests", "e2e-testing", "test1", "orders.test", "TEST_orders"} {
		assert.NoError(t, guardSQLServerDatabase(DriverSQLServer, name), name)
	}

	for _, name := range []string{"master", "MODEL", "msdb", "tempdb"} {
		_, err := Config{Databases: []DatabaseConfig{localTarget(DriverSQLServer, name)}}.resolved()
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

	hint := registrationHint(DriverSQLServer)
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
			cfg.Databases = []DatabaseConfig{localTarget(DriverSQLServer, "Example-db")}
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
// though it were MySQL. No statement is sent here: the refusal comes before the handle, which
// these fixtures leave nil, would be used.
func TestTruncateRefusesAnUnknownDriver(t *testing.T) {
	unknown := &Database{config: DatabaseConfig{Driver: "app_engine"}, tables: []string{"widgets"}}
	err := unknown.truncate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot empty tables on driver "app_engine"`)
	for _, implemented := range []string{DriverPostgres, DriverMySQL, DriverSQLServer} {
		assert.Contains(t, err.Error(), implemented, "and names the engines it can empty")
	}
	assert.Contains(t, err.Error(), "IsolateByRollback", "and names the isolation that works")

	nothing := &Database{config: DatabaseConfig{Driver: "app_engine"}}
	assert.NoError(t, nothing.truncate(), "with no tables there is nothing to refuse")
}

// ------------------------------------------------------------------ SQL Server

// TestQuoteIdentifierUsesBracketsOnSQLServer, where a double quote is an identifier only under
// QUOTED_IDENTIFIER ON, and a bracket always is. A closing bracket is the one character inside
// that needs escaping.
func TestQuoteIdentifierUsesBracketsOnSQLServer(t *testing.T) {
	sqlServer := &Database{config: DatabaseConfig{Driver: DriverSQLServer}}
	assert.Equal(t, "[ts_widgets]", sqlServer.quoteIdentifier("ts_widgets"))
	assert.Equal(t, "[2024Orders]", sqlServer.quoteIdentifier("2024Orders"))
	assert.Equal(t, "[odd]]name]", sqlServer.quoteIdentifier("odd]name"))
	assert.Equal(t, "[it's [x]", sqlServer.quoteIdentifier("it's [x"), "an opening bracket needs no escape")

	postgres := &Database{config: DatabaseConfig{Driver: DriverPostgres}}
	assert.Equal(t, `"odd""name"`, postgres.quoteIdentifier(`odd"name`), "the other engines keep theirs")
	mysql := &Database{config: DatabaseConfig{Driver: DriverMySQL}}
	assert.Equal(t, "`odd``name`", mysql.quoteIdentifier("odd`name"))
}

// TestSQLServerTruncateStatements pins the sequence: constraints off, rows deleted in the order
// given, identities reseeded where they have issued a value, constraints back on WITH CHECK.
func TestSQLServerTruncateStatements(t *testing.T) {
	plan, err := sqlServerTruncateStatements([]sqlServerTable{
		{name: "ts_widget_notes"},
		{name: "ts_counters", reseedTo: "0"},
		{name: "it's]odd", reseedTo: "95"},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"ALTER TABLE [ts_widget_notes] NOCHECK CONSTRAINT ALL",
		"ALTER TABLE [ts_counters] NOCHECK CONSTRAINT ALL",
		"ALTER TABLE [it's]]odd] NOCHECK CONSTRAINT ALL",
	}, plan.disable)
	assert.Equal(t, []string{
		"DELETE FROM [ts_widget_notes]",
		"DELETE FROM [ts_counters]",
		"DELETE FROM [it's]]odd]",
		"DBCC CHECKIDENT (N'[ts_counters]', RESEED, 0) WITH NO_INFOMSGS",
		"DBCC CHECKIDENT (N'[it''s]]odd]', RESEED, 95) WITH NO_INFOMSGS",
	}, plan.empty, "a table without a used identity is not reseeded: Msg 7997, or a next id of 0")
	assert.Equal(t, []string{
		"ALTER TABLE [ts_widget_notes] WITH CHECK CHECK CONSTRAINT ALL",
		"ALTER TABLE [ts_counters] WITH CHECK CHECK CONSTRAINT ALL",
		"ALTER TABLE [it's]]odd] WITH CHECK CHECK CONSTRAINT ALL",
	}, plan.restore, "one restore per disable, so a partial disable restores exactly what it switched off")

	empty, err := sqlServerTruncateStatements(nil)
	require.NoError(t, err)
	assert.Empty(t, empty.disable)
	assert.Empty(t, empty.empty)
	assert.Empty(t, empty.restore)
}

// TestSQLServerTruncateStatementsRefuseAReseedThatIsNotAnInteger. The value comes from the
// server's catalog, but it is written into SQL, so it is checked like anything else that is.
func TestSQLServerTruncateStatementsRefuseAReseedThatIsNotAnInteger(t *testing.T) {
	for _, value := range []string{"0; DROP TABLE x", "1.5", "0x10", " 0", "-"} {
		_, err := sqlServerTruncateStatements([]sqlServerTable{{name: "ts_counters", reseedTo: value}})
		require.Error(t, err, value)
		assert.Contains(t, err.Error(), "not an integer", value)
	}

	plan, err := sqlServerTruncateStatements([]sqlServerTable{{name: "t", reseedTo: "-2"}})
	require.NoError(t, err, "IDENTITY(-1, 1) reseeds to -2")
	assert.Contains(t, plan.empty, "DBCC CHECKIDENT (N'[t]', RESEED, -2) WITH NO_INFOMSGS")
}

// TestTheHarnessCreatesItsDatabaseOnlyOnALocalSQLServer. The Postgres and MySQL images create
// theirs from their environment, and creating one on Azure SQL provisions a billable database.
func TestTheHarnessCreatesItsDatabaseOnlyOnALocalSQLServer(t *testing.T) {
	assert.True(t, createsItsDatabase(localTarget(DriverSQLServer, "gorgany_test")))

	azure := localTarget(DriverSQLServer, "gorgany_test")
	azure.Host = "example.database.windows.net"
	assert.False(t, createsItsDatabase(azure))

	assert.False(t, createsItsDatabase(localTarget(DriverPostgres, "gorgany_test")))
	assert.False(t, createsItsDatabase(localTarget(DriverMySQL, "gorgany_test")))
}

// TestTheCreateDatabaseBatchSetsSnapshotReadsOnlyOnWhatItCreates: an existing database's read
// behaviour is its owner's, and switching it needs every other connection gone.
func TestTheCreateDatabaseBatchSetsSnapshotReadsOnlyOnWhatItCreates(t *testing.T) {
	statement, err := sqlServerCreateDatabaseSQL("gorgany_test")
	require.NoError(t, err)
	assert.Equal(t, "IF DB_ID(N'gorgany_test') IS NULL BEGIN CREATE DATABASE [gorgany_test]; "+
		"ALTER DATABASE [gorgany_test] SET READ_COMMITTED_SNAPSHOT ON; END", statement)

	statement, err = sqlServerCreateDatabaseSQL("2024_test")
	require.NoError(t, err)
	assert.Contains(t, statement, "CREATE DATABASE [2024_test]", "a name starting with a digit is bracketed")
}

// TestEnsureDatabaseRejectsUnsafeNames before it connects anywhere: CREATE DATABASE cannot take
// the name as a parameter, so it is written into the SQL, and only a name that cannot be
// anything but a name is.
func TestEnsureDatabaseRejectsUnsafeNames(t *testing.T) {
	refuseToOpen := func(dsconfig.DataSource) (dbCore.IDataSource, error) {
		t.Fatal("an unsafe name must be refused before connecting to master")
		return nil, nil
	}

	for _, name := range []string{
		"e2e-testing",
		"gorgany test",
		"gorgany_test]; DROP DATABASE [orders",
		"gorgany_test'); DROP DATABASE orders; --",
		"tést",
		"",
		strings.Repeat("t", 129),
	} {
		_, err := sqlServerCreateDatabaseSQL(name)
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "letters, digits and underscores", name)
		assert.Contains(t, err.Error(), "create it yourself", "an existing database of that name still works")

		assert.Error(t, ensureSQLServerDatabase(localTarget(DriverSQLServer, name), refuseToOpen), name)
	}

	_, err := sqlServerCreateDatabaseSQL(strings.Repeat("t", 128))
	assert.NoError(t, err, "128 characters is SQL Server's limit, and allowed")
}

// TestLookingForADatabaseTakesAnyNameThroughMaster: whether a database exists is asked of
// master with the name bound as an argument, so a name CREATE DATABASE is never given, such as
// one with a hyphen, can still be looked for, and a failure to reach master is reported as
// that, for waitForEngine to retry.
func TestLookingForADatabaseTakesAnyNameThroughMaster(t *testing.T) {
	errStub := errors.New("stub: no server")
	var opened []dsconfig.DataSource
	open := func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		opened = append(opened, cfg)
		return nil, errStub
	}

	exists, err := sqlServerDatabaseExists(localTarget(DriverSQLServer, "e2e-testing"), open)

	require.ErrorIs(t, err, errStub)
	assert.False(t, exists)
	assert.Contains(t, err.Error(), "connecting to master to look for database e2e-testing")
	require.Len(t, opened, 1, "the name is not refused before connecting")
	assert.Equal(t, "master", opened[0].Database)
}

// TestEnsureDatabaseUsesMasterWithoutTrippingTheTargetGuard. The database is created through
// master, which the guard refuses as a system database; the connection is a copy of the target
// the guard already accepted, pointed at master, and never passes through the guard itself.
func TestEnsureDatabaseUsesMasterWithoutTrippingTheTargetGuard(t *testing.T) {
	target := DatabaseConfig{
		Name: "primary", Driver: DriverSQLServer, Host: "db.internal", Port: 14331,
		User: "sa", Password: "Gorgany-Test-1", Database: "gorgany_test", SSL: "true",
	}
	require.Error(t, guardSQLServerDatabase(target.Label(), "master"),
		"the guard refuses master, which is why the bootstrap must not go through it")

	errStub := errors.New("stub: no server")
	var opened []dsconfig.DataSource
	open := func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		opened = append(opened, cfg)
		return nil, errStub
	}

	err := ensureSQLServerDatabase(target, open)
	require.ErrorIs(t, err, errStub, "the error is the connection's, not the guard's")
	assert.NotContains(t, err.Error(), "refusing")
	assert.Contains(t, err.Error(), "connecting to master to create database gorgany_test")

	require.Len(t, opened, 1)
	master := opened[0]
	assert.Equal(t, "master", master.Database)
	assert.Equal(t, DriverSQLServer, master.Driver)
	assert.Equal(t, "db.internal", master.Host)
	assert.Equal(t, 14331, master.Port)
	assert.Equal(t, "sa", master.Username)
	assert.Equal(t, "Gorgany-Test-1", master.Password)
	assert.Equal(t, "true", master.SSL)
	assert.Equal(t, map[string]string{"trust_server_certificate": "true"}, master.Options,
		"master is reached with the target's TLS settings")

	assert.Equal(t, "gorgany_test", target.Database, "the target itself is not changed")
	bootstrap := sqlServerBootstrapConfig(target)
	bootstrap.Database = target.Database
	assert.Equal(t, target, bootstrap, "the bootstrap differs from the target in its database alone")
}

// TestAConnectionErrorInEnsureDatabaseKeepsTheDriversRefusal, so waitForEngine can return a
// refused setting at once, as it does for the target.
func TestAConnectionErrorInEnsureDatabaseKeepsTheDriversRefusal(t *testing.T) {
	refusal := dbCore.Unsupported("sqlserver", "search_path", "no search path")
	err := ensureSQLServerDatabase(localTarget(DriverSQLServer, "gorgany_test"),
		func(dsconfig.DataSource) (dbCore.IDataSource, error) { return nil, refusal })

	require.Error(t, err)
	assert.True(t, isRefusal(err))
}

// clearHarnessEnv unsets every variable the harness reads, so a developer's own
// GORGANY_TEST_* settings do not change what these tests assert.
func clearHarnessEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		EnvDriver, EnvHost, EnvPort, EnvUser, EnvPassword, EnvDatabase,
		EnvSSL, EnvIsolation, EnvEngineWait, EnvKeepData, EnvMigrateDown, EnvAllowAnyTarget,
		EnvTrustServerCert,
	} {
		t.Setenv(name, "")
		require.NoError(t, unset(name))
	}
}
