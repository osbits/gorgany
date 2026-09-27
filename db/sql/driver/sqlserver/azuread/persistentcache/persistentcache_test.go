package persistentcache

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The registration, over a fake cache. The real one would write to the keychain of whoever runs
// the tests, and a CI runner or a container may have none; whether it works on a developer's
// machine is checked by hand, with one restart that does not prompt.

// marker is a type of this package, whose path the tests compare with the one azuread quotes.
type marker struct{}

// TestAzureADNamesThisPackage: the missing-import error quotes azuread.PersistentCacheImportPath,
// and an import line that names another path would fail the build.
func TestAzureADNamesThisPackage(t *testing.T) {
	assert.Equal(t, azuread.PersistentCacheImportPath, reflect.TypeOf(marker{}).PkgPath())
}

// TestImportRegistersTheFactory: init registered it, so a second registration is refused.
func TestImportRegistersTheFactory(t *testing.T) {
	assert.PanicsWithValue(t, "azuread: a persistent token cache is already registered", func() {
		azuread.RegisterPersistentCache(func(string) (azidentity.Cache, error) { return azidentity.Cache{}, nil })
	})
}

// TestADatasourceOpensTheCacheThroughTheImport: a datasource with auth.token_cache: persistent
// boots with this package imported, and opens the cache it registered, by the name every gorgany
// datasource shares. The credential is azidentity's own, built and never asked for a token.
func TestADatasourceOpensTheCacheThroughTheImport(t *testing.T) {
	var names []string
	saved := openCache
	openCache = func(name string) (azidentity.Cache, error) {
		names = append(names, name)
		return azidentity.Cache{}, nil
	}
	t.Cleanup(func() { openCache = saved })

	for _, method := range []string{sqlserver.AuthMethodInteractive, sqlserver.AuthMethodDeviceCode} {
		ds, err := sqlserver.NewDataSourceWithConfig(dsconfig.DataSource{
			Driver:      "sqlserver_gorm",
			Host:        "example.database.windows.net",
			Port:        1433,
			Database:    "Example-db",
			Username:    "user@example.com",
			LazyConnect: true,
			Auth: dsconfig.Auth{
				Method:                   method,
				TokenCache:               dsconfig.TokenCachePersistent,
				AuthenticationRecordPath: filepath.Join(t.TempDir(), "record.json"),
			},
		})
		require.NoErrorf(t, err, "%s", method)
		require.NoError(t, ds.Close())
	}
	assert.Equal(t, []string{"gorgany", "gorgany"}, names)
}
