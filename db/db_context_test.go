package db

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

type closeCountingDataSource struct {
	dbCore.IDataSource
	closed int
	err    error
}

func (d *closeCountingDataSource) Close() error {
	d.closed++
	return d.err
}

// TestCloseClosesEveryDatasourceAndReportsEachFailure: ServerApp closes the datasources
// last on a graceful shutdown, and one that fails to close must not keep the rest open.
func TestCloseClosesEveryDatasourceAndReportsEachFailure(t *testing.T) {
	healthy := &closeCountingDataSource{}
	failing := &closeCountingDataSource{err: errors.New("connection reset")}
	dbContext := &DBContext{}
	dbContext.RegisterDataSource("default", healthy)
	dbContext.RegisterDataSource("reporting", failing)

	err := dbContext.Close()

	assert.Equal(t, 1, healthy.closed)
	assert.Equal(t, 1, failing.closed, "a failure must not stop the others closing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "datasource 'reporting': connection reset")
	assert.NotContains(t, err.Error(), "'default'")
}

func TestCloseWithNoDatasourcesSucceeds(t *testing.T) {
	assert.NoError(t, (&DBContext{}).Close())
}
