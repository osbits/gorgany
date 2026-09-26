// Package buildinfo carries what the build stamped into the binary.
package buildinfo

// Version is set at build time:
//
//	go build -ldflags "-X myapp/pkg/buildinfo.Version=v1.4.0" ./cmd/app
var Version = "dev"
