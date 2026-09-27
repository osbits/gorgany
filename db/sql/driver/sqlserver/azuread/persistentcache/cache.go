//go:build (darwin && cgo) || linux || windows

package persistentcache

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity/cache"
)

// openCache opens the persistent cache called name in the operating system's credential store. It
// is a variable only so that tests can open a fake instead, and never touch the keychain.
var openCache = func(name string) (azidentity.Cache, error) {
	return cache.New(&cache.Options{Name: name})
}
