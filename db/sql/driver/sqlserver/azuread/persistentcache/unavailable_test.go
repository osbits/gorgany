//go:build !((darwin && cgo) || linux || windows)

package persistentcache

import (
	"path/filepath"
	"runtime"
	"testing"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The build with no credential store: macOS without cgo, which `CGO_ENABLED=0 go test` on a Mac
// runs, or a system azidentity keeps no persistent cache on. The package still builds, and the
// real openCache, which these tests do not replace, refuses with the reason.

// unavailable is what openCache says in this build.
func unavailable() string {
	if runtime.GOOS == "darwin" {
		return "this binary was built without cgo, which the macOS keychain needs; build it with " +
			"CGO_ENABLED=1 and a C compiler (the Xcode command-line tools)"
	}
	return "azidentity keeps a persistent token cache only on macOS, Linux and Windows, and this binary " +
		"is for " + runtime.GOOS
}

func TestWithoutACredentialStoreTheCacheRefusesToOpen(t *testing.T) {
	_, err := openCache("gorgany")
	require.Error(t, err)
	assert.Equal(t, unavailable(), err.Error())
}

// TestWithoutACredentialStoreTheBootSaysWhy: a datasource with auth.token_cache: persistent fails
// the boot with the reason, and the way back to one sign-in per process.
func TestWithoutACredentialStoreTheBootSaysWhy(t *testing.T) {
	for _, method := range []string{sqlserver.AuthMethodInteractive, sqlserver.AuthMethodDeviceCode} {
		_, err := sqlserver.NewDataSourceWithConfig(dsconfig.DataSource{
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
		require.Errorf(t, err, "%s", method)
		assert.Contains(t, err.Error(), "sqlserver: auth.method "+method+" for example.database.windows.net:1433/Example-db: "+
			"auth.token_cache persistent cannot keep the tokens in the operating system's credential store: "+unavailable())
		assert.Contains(t, err.Error(), "use auth.token_cache memory to sign in once per process instead")
		if runtime.GOOS == "darwin" {
			assert.Contains(t, err.Error(), "built without cgo")
		}
	}
}
