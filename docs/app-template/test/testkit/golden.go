// Package testkit holds helpers shared by tests. Only _test.go files import it.
package testkit

import (
	"flag"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files instead of comparing against them")

// Golden compares got with the file at path. Run the test with -update to rewrite
// the file, then review the diff like any other change.
func Golden(t testing.TB, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to create it)", err)
	}
	if string(want) != got {
		t.Errorf("%s is out of date. Rerun with -update and review the diff.\n--- want\n%s--- got\n%s", path, want, got)
	}
}
