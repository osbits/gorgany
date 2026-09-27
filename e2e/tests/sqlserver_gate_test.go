package e2e

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// These cover gateSQLServer itself, with a reach function standing in for the engine, so
// they need no server. They restore the counters the gate moves, since TestMain reads them
// to decide whether the run proved anything, and a gate exercised here proves nothing.

// keepGateState restores the gate's package state when the test ends.
func keepGateState(t *testing.T) {
	t.Helper()

	live, sqlServer, gaveUp := executedLiveCases.Load(), executedSQLServerCases.Load(), sqlServerGaveUp.Load()
	t.Cleanup(func() {
		executedLiveCases.Store(live)
		executedSQLServerCases.Store(sqlServer)
		sqlServerGaveUp.Store(gaveUp)
	})
}

// TestTheSQLServerGateAdmitsAndCounts: an admitted case is a live case and a SQL Server
// case, which are the two counts TestMain checks the switches against.
func TestTheSQLServerGateAdmitsAndCounts(t *testing.T) {
	keepGateState(t)
	live, sqlServer := executedLiveCases.Load(), executedSQLServerCases.Load()

	got := gateSQLServer(t, 0, func() (string, error) { return "datasource", nil })

	assert.Equal(t, "datasource", got, "the gate hands back what reach built")
	assert.Equal(t, live+1, executedLiveCases.Load())
	assert.Equal(t, sqlServer+1, executedSQLServerCases.Load())
}

// TestTheSQLServerGateSkipsUnderRequireLive. The compose harness always sets
// E2E_REQUIRE_LIVE and starts SQL Server only under E2E_REQUIRE_SQLSERVER, so the first must
// not make an absent SQL Server fatal, or every run without it would fail.
func TestTheSQLServerGateSkipsUnderRequireLive(t *testing.T) {
	keepGateState(t)
	t.Setenv(requireLiveEnvVar, "1")
	t.Setenv(requireSQLServerEnvVar, "")
	sqlServer := executedSQLServerCases.Load()

	passed := t.Run("unreachable", func(t *testing.T) {
		gateSQLServer(t, 0, func() (int, error) { return 0, errors.New("connection refused") })
		t.Fatal("the gate must not admit a case it could not reach")
	})

	assert.True(t, passed, "an unreachable SQL Server is a skip unless E2E_REQUIRE_SQLSERVER=1")
	assert.Equal(t, sqlServer, executedSQLServerCases.Load(), "a skipped case is not counted")
	assert.True(t, sqlServerGaveUp.Load(), "the next gate tries once instead of waiting again")
}

// TestTheSQLServerGateTriesOnceAfterGivingUp, so a run without SQL Server costs one wait,
// not one per case.
func TestTheSQLServerGateTriesOnceAfterGivingUp(t *testing.T) {
	keepGateState(t)
	t.Setenv(requireSQLServerEnvVar, "")
	sqlServerGaveUp.Store(true)

	attempts := 0
	t.Run("unreachable", func(t *testing.T) {
		gateSQLServer(t, time.Hour, func() (int, error) {
			attempts++
			return 0, errors.New("connection refused")
		})
	})

	assert.Equal(t, 1, attempts)
}
