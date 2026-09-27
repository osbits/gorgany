// Package persistentcache keeps the tokens of an interactive or device-code sign-in to SQL Server
// or Azure SQL in the operating system's credential store, so that a restarted process signs in
// without asking the person again. It is for development machines.
//
// Import it for its side effects, in pkg/provider/bootstrap.go next to the azuread import, and set
// auth.token_cache: persistent on the datasource:
//
//	import (
//	    _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread"
//	    _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread/persistentcache"
//	)
//
// Without this import, auth.token_cache: persistent fails the boot, and the error quotes the
// import line. init registers azidentity/cache's New with azuread.RegisterPersistentCache, under
// the name "gorgany".
//
// # What it links
//
// It links azidentity's persistent cache (github.com/Azure/azure-sdk-for-go/sdk/azidentity/cache)
// and the MSAL extensions it stores through (github.com/AzureAD/microsoft-authentication-extensions-for-go),
// and on macOS the keychain binding github.com/keybase/go-keychain, which is cgo. No other package
// of the framework links any of them, and the split test in db/sql/driver/builtin keeps it so.
//
// # Where it works
//
//   - macOS: the login keychain, which needs cgo, so an Xcode command-line tools compiler and
//     CGO_ENABLED=1, the default where a compiler is installed. macOS may ask once to let the
//     binary use the keychain item, and again for a binary rebuilt since.
//   - Linux: a file under $XDG_CACHE_HOME or ~/.cache, encrypted with a key kept in the kernel's
//     user keyring. It needs no cgo, but the keyring must be reachable, which a container's
//     default seccomp profile does not allow, and the key lives in memory, so after a reboot the
//     person signs in once more.
//   - Windows: a file under %LOCALAPPDATA%, encrypted with DPAPI for the signed-in user.
//
// Anywhere else, and on macOS built without cgo, the package still builds, and a datasource that
// asks for the persistent cache fails the boot with the reason. The production image is built
// with CGO_ENABLED=0 for scratch, which has no credential store and no person to sign in, so a
// deployed app neither imports this package nor signs in as a person.
package persistentcache

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread"
)

func init() {
	azuread.RegisterPersistentCache(func(name string) (azidentity.Cache, error) { return openCache(name) })
}
