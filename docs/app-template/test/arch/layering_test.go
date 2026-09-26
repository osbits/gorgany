// Package arch holds repository-wide invariants. It is a plain test package, so
// `go test ./...` enforces them with no extra tooling.
package arch

import (
	"bufio"
	"errors"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const root = "../.."

// layers names the application's layers by path prefix. The longest match wins.
// A package under pkg/ that matches none of them (pkg/mail, pkg/<feature>) is a
// capability package. "*" below means every layer except test/.
var layers = map[string]string{
	"cmd/":           "cmd",
	"pkg/provider":   "provider",
	"pkg/controller": "delivery",
	"pkg/middleware": "delivery",
	"pkg/command":    "delivery",
	"pkg/job":        "delivery",
	"pkg/subscriber": "delivery",
	"pkg/auth":       "auth",
	"pkg/service":    "service",
	"pkg/model":      "model",
	"pkg/event":      "model",
	"pkg/domain":     "domain",
	"pkg/health":     "health",
	"pkg/adapter":    "adapter",
	"pkg/grgcompat":  "compat",
	"pkg/constant":   "base",
	"pkg/buildinfo":  "base",
	"db/migration":   "migration",
	"db/seeder":      "seeder",
}

// allowed is the whole import policy of docs/PROJECT_STRUCTURE.md: which layers a
// layer may import. Imports inside a package's own subtree are always allowed.
var allowed = map[string][]string{
	"cmd":        {"provider", "health"},
	"provider":   {"*"},
	"delivery":   {"delivery", "auth", "service", "capability", "model", "domain", "adapter", "compat", "base"},
	"auth":       {"service", "capability", "model", "domain", "adapter", "compat", "base"},
	"service":    {"capability", "model", "domain", "adapter", "compat", "base"},
	"capability": {"capability", "model", "domain", "adapter", "compat", "base"},
	"model":      {"model", "domain", "base"},
	"domain":     {"compat", "base"},
	"health":     {"base"},
	"adapter":    {"base"},
	"compat":     {"base"},
	"base":       {},
	"migration":  {},
	"seeder":     {"domain", "base"},
}

func TestLayering(t *testing.T) {
	module := modulePath(t)
	known := knownViolations(t)
	seen := map[string]bool{}

	for _, dir := range packageDirs(t, "cmd", "pkg", "db") {
		pkg, err := build.ImportDir(filepath.Join(root, dir), 0)
		var noGo *build.NoGoError
		if errors.As(err, &noGo) {
			continue // a test-only package
		}
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		from := layerOf(dir)
		for _, imp := range pkg.Imports { // production imports only, not test imports
			rel, ok := strings.CutPrefix(imp, module+"/")
			if !ok || rel == dir || strings.HasPrefix(rel, dir+"/") {
				continue
			}
			to := layerOf(rel)
			if (slices.Contains(allowed[from], "*") && to != "test") || slices.Contains(allowed[from], to) {
				continue
			}
			edge := dir + " -> " + rel
			if known[edge] {
				seen[edge] = true
				continue
			}
			t.Errorf("forbidden import %s (the %s layer may not import the %s layer)", edge, from, to)
		}
	}
	for edge := range known {
		if !seen[edge] {
			t.Errorf("known_violations.txt lists %q, which no longer exists: delete the line", edge)
		}
	}
}

// layerOf returns the layer of a module-relative package path. test/ has none, so
// no production package may import it.
func layerOf(rel string) string {
	best := ""
	for prefix := range layers {
		if (rel == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(rel, strings.TrimSuffix(prefix, "/")+"/")) && len(prefix) > len(best) {
			best = prefix
		}
	}
	switch {
	case best != "":
		return layers[best]
	case strings.HasPrefix(rel, "pkg/"):
		return "capability"
	default:
		return "test"
	}
}

func modulePath(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

func packageDirs(t *testing.T, tops ...string) []string {
	t.Helper()
	var dirs []string
	for _, top := range tops {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(root, path)
				dirs = append(dirs, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	return dirs
}

// knownViolations reads the edges an existing app has not fixed yet. The file may
// only shrink: a listed edge that no longer exists fails the test too.
func knownViolations(t *testing.T) map[string]bool {
	t.Helper()
	known := map[string]bool{}
	f, err := os.Open("known_violations.txt")
	if errors.Is(err, fs.ErrNotExist) {
		return known
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			known[line] = true
		}
	}
	return known
}
