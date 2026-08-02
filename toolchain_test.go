package gorgany

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Two facts about this module that are easy to break and impossible to notice, so they are
// asserted here rather than in CI alone — a developer running `go test ./...` finds out at the
// same moment CI would.

// TestTheE2eImageAndGoModPinTheSameToolchain.
//
// The release exit criterion is that the release build and the e2e image use the same patched
// Go line. That was previously a thing somebody had to remember: go.mod said one version and
// e2e/Dockerfile said another, and nothing compared them, so the suite that is supposed to
// prove the release could have been compiled by a toolchain the release does not use.
func TestTheE2eImageAndGoModPinTheSameToolchain(t *testing.T) {
	goMod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("cannot read go.mod: %v", err)
	}
	dockerfile, err := os.ReadFile("e2e/Dockerfile")
	if err != nil {
		t.Fatalf("cannot read e2e/Dockerfile: %v", err)
	}

	toolchain := regexp.MustCompile(`(?m)^toolchain go(\d+\.\d+(?:\.\d+)?)\s*$`).
		FindStringSubmatch(string(goMod))
	if toolchain == nil {
		t.Fatal("go.mod declares no toolchain directive; the release build's Go version is " +
			"then whatever the machine happens to have")
	}

	image := regexp.MustCompile(`(?m)^FROM golang:(\d+\.\d+(?:\.\d+)?)`).
		FindStringSubmatch(string(dockerfile))
	if image == nil {
		t.Fatal("e2e/Dockerfile does not pin a golang image version")
	}

	if toolchain[1] != image[1] {
		t.Fatalf("go.mod pins toolchain go%s and e2e/Dockerfile pins golang:%s; the suite that "+
			"proves the release would be compiled by a different Go than the release",
			toolchain[1], image[1])
	}
}

// TestTheV1ChiModulePathIsNotADependency.
//
// github.com/go-chi/chi without a major-version suffix is the v1 module path, and it is dead:
// it receives no fixes, so every advisory against it needs an exception rather than an
// upgrade. The module moved to /v5; this is what stops it coming back through a transitive
// dependency or a copy-pasted import.
func TestTheV1ChiModulePathIsNotADependency(t *testing.T) {
	goMod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("cannot read go.mod: %v", err)
	}

	for _, line := range strings.Split(string(goMod), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "github.com/go-chi/chi" {
			t.Fatalf("go.mod requires the dead chi v1 module path (%s); the module is "+
				"github.com/go-chi/chi/v5", strings.TrimSpace(line))
		}
	}
}
