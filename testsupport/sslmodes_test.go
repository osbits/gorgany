package testsupport

import (
	"strings"
	"testing"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	// A test may link the SQL Server engine, which testsupport itself must not: this is how
	// the harness's copy of the engine's SSL modes is compared with the engine.
	sqlserverv2 "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheHarnessAcceptsTheSSLModesTheEngineDoes keeps sqlServerSSLModes, which Validate checks
// SQL Server's SSL against, the same as the engine's own list: every value the harness accepts
// the engine builds a connection string for, and each value the harness refuses, the engine
// refuses too. A value the harness refused and the engine took would fail a suite that should
// run; one the harness took and the engine refused would be retried for the whole engine wait
// and then skipped, which is what Validate is there to prevent.
func TestTheHarnessAcceptsTheSSLModesTheEngineDoes(t *testing.T) {
	buildable := func(ssl string) error {
		_, err := sqlserverv2.BuildDSN(dsconfig.DataSource{
			Driver: DriverSQLServer, Host: "127.0.0.1", Port: 14330,
			Username: "sa", Password: "Gorgany-Test-1", Database: "gorgany_test", SSL: ssl,
		})
		return err
	}

	for mode := range sqlServerSSLModes {
		for _, spelling := range []string{mode, strings.ToUpper(mode), " " + mode + " "} {
			database := localTarget(DriverSQLServer, "gorgany_test")
			database.SSL = spelling
			assert.NoErrorf(t, database.Validate(), "the harness must accept %q", spelling)
			assert.NoErrorf(t, buildable(spelling), "the engine must accept %q", spelling)
		}
	}

	for _, refused := range []string{"require", "verify-full", "verify-ca", "prefer", "allow", "on", "off", "skip-verify", "preferred"} {
		database := localTarget(DriverSQLServer, "gorgany_test")
		database.SSL = refused
		assert.Errorf(t, database.Validate(), "the harness must refuse %q", refused)
		assert.Errorf(t, buildable(refused), "the engine refuses %q", refused)
	}
}

// TestAnSSLModeSQLServerDoesNotKnowIsAConfigError. The engine refuses it on every attempt to
// connect, so the harness would retry it for the whole engine wait and then skip, reporting a
// Postgres sslmode left in GORGANY_TEST_SSL as an engine that is not running.
func TestAnSSLModeSQLServerDoesNotKnowIsAConfigError(t *testing.T) {
	database := localTarget(DriverSQLServer, "gorgany_test")
	database.SSL = "require"

	err := database.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `SSL "require" is not a SQL Server encryption mode`)
	assert.Contains(t, err.Error(), EnvSSL)

	_, err = Config{Databases: []DatabaseConfig{database}}.resolved()
	require.Error(t, err, "resolved fails on it, so RequireDatabase fails the test instead of skipping it")

	t.Run("from the environment", func(t *testing.T) {
		clearHarnessEnv(t)
		t.Setenv(EnvDriver, DriverSQLServer)
		t.Setenv(EnvSSL, "verify-full")

		_, err := Config{}.resolved()
		require.Error(t, err)
		assert.Contains(t, err.Error(), EnvSSL)
	})

	pg := localTarget(DriverPostgres, "gorgany_test")
	pg.SSL = "require"
	assert.NoError(t, pg.Validate(), "require is a Postgres sslmode")
}
