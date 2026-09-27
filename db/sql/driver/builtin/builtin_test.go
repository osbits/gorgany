package builtin_test

import (
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	_ "github.com/osbits/gorgany/v2/db/sql/driver/builtin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuiltinDriversAreRegistered is the T1.1 structural fix: the provider used
// to carry a hard-coded switch that understood exactly one driver name, so adding
// an engine meant editing the framework.
func TestBuiltinDriversAreRegistered(t *testing.T) {
	for _, name := range []string{"postgres_gorm", "mysql_gorm"} {
		t.Run(name, func(t *testing.T) {
			ctor, ok := driver.Lookup(name)
			assert.Truef(t, ok, "driver %q must be registered", name)
			assert.NotNil(t, ctor)
		})
	}
}

// TestDriverNamesMatchCoreDbTypeConstants keeps the config-facing names and the
// core.DbType constants from drifting apart.
func TestDriverNamesMatchCoreDbTypeConstants(t *testing.T) {
	_, ok := driver.Lookup(string(core.GormPostgreSQL))
	assert.True(t, ok, "core.GormPostgreSQL must name a registered driver")

	_, ok = driver.Lookup(string(core.GormMySQL))
	assert.True(t, ok, "core.GormMySQL must name a registered driver")
}

func TestRegisteredNamesAreSorted(t *testing.T) {
	names := driver.Names()
	require.Contains(t, names, "mysql_gorm")
	require.Contains(t, names, "postgres_gorm")
	assert.IsIncreasing(t, names)
}

// TestBothEnginesAcceptReadOnlyThroughTheRegistry: Postgres and MySQL used to refuse
// read_only until they enforced it, and driver.New refuses a datasource that does not report a
// flag its config sets. Both enforce it now, so the registry hands back a datasource that
// reports it, lazily, since nothing listens on 127.0.0.1:1.
func TestBothEnginesAcceptReadOnlyThroughTheRegistry(t *testing.T) {
	for _, name := range []string{"postgres_gorm", "mysql_gorm"} {
		t.Run(name, func(t *testing.T) {
			ds, err := driver.New(dsconfig.DataSource{
				Driver:      name,
				Host:        "127.0.0.1",
				Port:        1,
				Database:    "reports",
				ReadOnly:    true,
				LazyConnect: true,
			})
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, ds.Close()) })

			assert.Equal(t, dbCore.DataSourcePolicy{ReadOnly: true}, dbCore.PolicyOf(ds))
		})
	}
}
