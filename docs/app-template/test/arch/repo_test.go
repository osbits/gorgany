package arch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestToolchainPin keeps both Dockerfiles and the CI image on the go.mod toolchain.
// The official golang image sets GOTOOLCHAIN=local, so an image older than the
// toolchain line compiles silently with the older compiler; after a bump, CI would
// test and scan with one compiler while production ships another.
func TestToolchainPin(t *testing.T) {
	toolchain := regexp.MustCompile(`(?m)^toolchain go(\S+)$`).FindStringSubmatch(read(t, "go.mod"))
	if toolchain == nil {
		t.Fatal("go.mod has no toolchain directive")
	}
	for _, dockerfile := range []string{"Dockerfile", "test/e2e/runner.Dockerfile"} {
		image := regexp.MustCompile(`(?m)^ARG GO_VERSION=(\S+)$`).FindStringSubmatch(read(t, dockerfile))
		if image == nil {
			t.Fatalf("%s has no ARG GO_VERSION=", dockerfile)
		}
		if toolchain[1] != image[1] {
			t.Errorf("go.mod pins go%s but %s builds with golang:%s", toolchain[1], dockerfile, image[1])
		}
	}
	for _, m := range regexp.MustCompile(`(?m)^\s*image: golang:(\S+)`).FindAllStringSubmatch(read(t, ".gitlab-ci.yml"), -1) {
		if m[1] != toolchain[1] {
			t.Errorf("go.mod pins go%s but .gitlab-ci.yml uses golang:%s", toolchain[1], m[1])
		}
	}
}

// TestEnvSampleIsComplete fails when config/config.yml reads a ${VAR} that
// .env.sample does not list: the next developer, and the next deploy, would not
// know to set it, and an unresolved placeholder becomes an empty string.
func TestEnvSampleIsComplete(t *testing.T) {
	config := read(t, "config/config.yml")
	sample := read(t, ".env.sample")

	listed := map[string]bool{}
	for _, line := range strings.Split(sample, "\n") {
		if name, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok && !strings.HasPrefix(name, "#") {
			listed[name] = true
		}
	}
	for _, m := range regexp.MustCompile(`\$\{(\w+)\}`).FindAllStringSubmatch(config, -1) {
		if !listed[m[1]] {
			t.Errorf("config/config.yml reads ${%s}, which .env.sample does not list", m[1])
		}
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
