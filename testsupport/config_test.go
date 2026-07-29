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
