package testsupport

import (
	"testing"
	"time"

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

	cfg, err := New(Config{Isolation: IsolateByRollback}).resolve()
	require.NoError(t, err)

	assert.Equal(t, IsolateByRollback, cfg.Isolation, "the field that was set keeps its value")
	assert.Equal(t, "db.internal", cfg.Databases[0].Host)
	assert.Equal(t, 2*time.Second, cfg.EngineWait)
	assert.True(t, cfg.KeepData)
	assert.True(t, cfg.MigrateDown)
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

	explicit := DatabaseConfig{
		Name: "primary", Driver: DriverPostgres,
		Host: "explicit", Port: 6543, User: "u", Password: "p", Database: "explicit_db",
	}

	cfg, err := New(Config{
		Databases:   []DatabaseConfig{explicit},
		Isolation:   IsolateByRollback,
		EngineWait:  7 * time.Second,
		KeepData:    true,
		MigrateDown: true,
	}).resolve()
	require.NoError(t, err)

	assert.Equal(t, []DatabaseConfig{explicit}, cfg.Databases)
	assert.Equal(t, IsolateByRollback, cfg.Isolation)
	assert.Equal(t, 7*time.Second, cfg.EngineWait)
	assert.True(t, cfg.KeepData, "a variable that is off does not turn off what the suite turned on")
	assert.True(t, cfg.MigrateDown)
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

// clearHarnessEnv unsets every variable the harness reads, so a developer's own
// GORGANY_TEST_* settings do not change what these tests assert.
func clearHarnessEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		EnvDriver, EnvHost, EnvPort, EnvUser, EnvPassword, EnvDatabase,
		EnvSSL, EnvIsolation, EnvEngineWait, EnvKeepData, EnvMigrateDown,
	} {
		t.Setenv(name, "")
		require.NoError(t, unset(name))
	}
}
