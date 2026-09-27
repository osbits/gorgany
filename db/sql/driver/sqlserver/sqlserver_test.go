package sqlserver_test

import (
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	"github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheDriverRegistersUnderItsName(t *testing.T) {
	ctor, ok := driver.Lookup(sqlserver.Name)
	require.True(t, ok, "importing the package registers %s", sqlserver.Name)
	assert.NotNil(t, ctor)
	assert.Equal(t, []string{"sqlserver_gorm"}, driver.Names(), "and nothing else: not Postgres, not MySQL")
}

func TestNameMatchesCoreConstant(t *testing.T) {
	assert.Equal(t, string(core.GormSQLServer), sqlserver.Name)
}

// TestDriverNewBuildsWithLazyConnect: through the registry, as the provider builds every
// datasource, and with both policy flags, which driver.New refuses unless the datasource
// reports them.
func TestDriverNewBuildsWithLazyConnect(t *testing.T) {
	ds, err := driver.New(dsconfig.DataSource{
		Driver:         sqlserver.Name,
		Host:           "127.0.0.1",
		Port:           1,
		Database:       "Example-db",
		Username:       "sa",
		Password:       "Gorgany-Test-1",
		LazyConnect:    true,
		ReadOnly:       true,
		ExternalSchema: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	assert.Equal(t, dbCore.DataSourcePolicy{ExternalSchema: true, ReadOnly: true}, dbCore.PolicyOf(ds))
	session, err := ds.NewSession()
	require.NoError(t, err)
	assert.Equal(t, "sqlserver", session.Query().Dialect().Name())
}
