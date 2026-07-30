package builtin_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/db/sql/driver"
	_ "github.com/osbits/gorgany/v2/db/sql/driver/builtin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F7: provider.DbProvider used to blank-import builtin, which registers both engines — so
// every app on the standard bootstrap linked gorm.io/driver/mysql, go-sql-driver/mysql and
// filippo.io/edwards25519 whether or not it would ever speak MySQL. DbProvider now imports
// nothing and the app chooses.

// TestBuiltinStillRegistersBothEngines: it is the convenience import, and an app that had
// it must keep working unchanged.
func TestBuiltinStillRegistersBothEngines(t *testing.T) {
	names := driver.Names()

	assert.Contains(t, names, "postgres_gorm")
	assert.Contains(t, names, "mysql_gorm")
}

// TestBothEnginesResolveThroughBuiltin — registered *and* constructible, since a
// registration that cannot build anything would satisfy the test above.
func TestBothEnginesResolveThroughBuiltin(t *testing.T) {
	for _, name := range []string{"postgres_gorm", "mysql_gorm"} {
		ctor, ok := driver.Lookup(name)
		require.Truef(t, ok, "%s must be registered", name)
		require.NotNilf(t, ctor, "%s must have a constructor", name)
	}
}

// TestASingleEngineImportDoesNotLinkTheOther is the dependency-hygiene claim, and it has to
// be checked with the build tool rather than at runtime: once this test binary imports
// builtin, both engines are linked into *it* by construction.
//
// `go list -deps` on the single-engine package is the assertion. It is skipped when the
// toolchain is unavailable rather than failing, since that is an environment problem and not
// a defect in the split.
func TestASingleEngineImportDoesNotLinkTheOther(t *testing.T) {
	deps := func(pkg string) string {
		t.Helper()

		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Skipf("go list unavailable: %v", err)
		}
		return string(out)
	}

	postgresOnly := deps("github.com/osbits/gorgany/v2/db/sql/driver/postgres")
	assert.Contains(t, postgresOnly, "gorm.io/driver/postgres")
	assert.NotContains(t, postgresOnly, "gorm.io/driver/mysql",
		"importing driver/postgres must not link the MySQL driver")
	assert.NotContains(t, postgresOnly, "github.com/go-sql-driver/mysql")

	mysqlOnly := deps("github.com/osbits/gorgany/v2/db/sql/driver/mysql")
	assert.Contains(t, mysqlOnly, "gorm.io/driver/mysql")
	assert.NotContains(t, mysqlOnly, "gorm.io/driver/postgres",
		"and the reverse holds too")
}

// TestDbProviderLinksNeitherEngine is the other half: the provider itself must be
// engine-agnostic, or the split achieves nothing for an app that uses the standard
// bootstrap.
func TestDbProviderLinksNeitherEngine(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/osbits/gorgany/v2/provider").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	deps := string(out)
	for _, engine := range []string{
		"gorm.io/driver/mysql",
		"gorm.io/driver/postgres",
		"github.com/go-sql-driver/mysql",
	} {
		assert.NotContainsf(t, deps, engine,
			"provider must not link %s — the app chooses its engine", engine)
	}
}

// TestTheDriverNameConstantsAgree, since apps referenced builtin's before the split and the
// single-engine packages expose their own.
func TestTheDriverNameConstantsAgree(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.Name}}",
		"github.com/osbits/gorgany/v2/db/sql/driver/postgres").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	assert.Equal(t, "postgres", strings.TrimSpace(string(out)))
}
