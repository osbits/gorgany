package builder_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hardCodedPostgresBuilder matches a call to the Postgres-specific builder
// constructor.
var hardCodedPostgresBuilder = regexp.MustCompile(`\bv2\.NewBuilder\(\)`)

// packagesAllowedToNameADialect may construct a specific dialect's builder: the
// builder package itself, and each dialect's own package and tests.
var packagesAllowedToNameADialect = []string{
	"db/sql/builder/",
	"db/sql/gorm/postgres/v2/",
	"db/sql/gorm/mysql/v2/",
}

// TestNoHardCodedPostgresBuilderOutsideDialectPackages is an enforcement test, not
// a behaviour test.
//
// Thirteen call sites across db/orm and model/pagination constructed the *Postgres*
// builder unconditionally, so the whole ORM ignored the dialect its session was
// speaking. `orm.Create` against MySQL emitted `... RETURNING id` and failed with
// error 1064 — the headline v2 feature could not insert a row — and the
// many-to-many path sent raw `ON CONFLICT` to a server with no such clause,
// bypassing the dialect's own translation.
//
// Reads, updates and deletes survived only because PostgresDialect happens to emit
// bare unquoted identifiers and both engines accept `LIMIT n OFFSET m`. Every one of
// those sites was a latent break for the next dialect-specific emission.
//
// A comment asking people not to do it again would not have held. This fails the
// build instead. Obtain a builder from the session (`session.Query()`), from the ORM
// (`o.newBuilder()`), or from an existing one (`builder.NewLike(b)`).
func TestNoHardCodedPostgresBuilderOutsideDialectPackages(t *testing.T) {
	root := repoRoot(t)

	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if shouldSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// A test needs *some* dialect to build against, so test files may name one.
		// This mirrors the exclusion in the brief's own verification command.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		relSlash := filepath.ToSlash(rel)

		// The dialect packages and the e2e fixtures may name a dialect explicitly.
		if strings.HasPrefix(relSlash, "e2e/") {
			return nil
		}
		for _, allowed := range packagesAllowedToNameADialect {
			if strings.HasPrefix(relSlash, allowed) {
				return nil
			}
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		for i, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			// Prose about the old behaviour is fine; calls are not.
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if hardCodedPostgresBuilder.MatchString(line) {
				offenders = append(offenders,
					relSlash+":"+itoa(i+1)+"  "+trimmed)
			}
		}
		return nil
	})
	require.NoError(t, err)

	assert.Emptyf(t, offenders,
		"a dialect-specific builder must not be constructed outside the dialect packages.\n"+
			"Use session.Query(), o.newBuilder() or builder.NewLike(b) instead.\nOffenders:\n  %s",
		strings.Join(offenders, "\n  "))
}

// shouldSkipDir reports whether a directory is outside the module's own source.
//
// Every hidden directory is skipped, not just .git. A git *worktree* under .claude/
// holds a full checkout at some other commit, so walking into one had this test
// reporting pre-fix code in a sibling checkout as an offence in this one — a failure
// with nothing to do with the tree under test. The same applies to any tool cache or
// vendored copy that happens to live under a dot-directory.
func shouldSkipDir(name string) bool {
	if strings.HasPrefix(name, ".") && name != "." {
		return true
	}

	switch name {
	case "vendor", "node_modules", "resource":
		return true
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()

	// This test lives in db/sql/builder, three levels below the module root.
	wd, err := os.Getwd()
	require.NoError(t, err)

	root := filepath.Join(wd, "..", "..", "..")
	_, err = os.Stat(filepath.Join(root, "go.mod"))
	require.NoError(t, err, "expected the module root at %s", root)

	return root
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
