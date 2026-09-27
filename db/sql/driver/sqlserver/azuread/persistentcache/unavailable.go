//go:build !((darwin && cgo) || linux || windows)

package persistentcache

import (
	"errors"
	"runtime"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

// openCache refuses: this build has no credential store azidentity's persistent cache can use,
// which on macOS means it was built without cgo, and elsewhere that azidentity supports no store
// on this system. It is a variable, as in the builds that have one, so that tests can open a fake.
var openCache = func(string) (azidentity.Cache, error) {
	if runtime.GOOS == "darwin" {
		return azidentity.Cache{}, errors.New("this binary was built without cgo, which the macOS keychain " +
			"needs; build it with CGO_ENABLED=1 and a C compiler (the Xcode command-line tools)")
	}
	return azidentity.Cache{}, errors.New("azidentity keeps a persistent token cache only on macOS, Linux " +
		"and Windows, and this binary is for " + runtime.GOOS)
}
