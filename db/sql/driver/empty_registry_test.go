package driver

import (
	"errors"
	"os/exec"
	"regexp"
	"testing"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F7 made the engine packages opt-in, which is compile-clean and shows up only at boot. So
// the failure message is the migration instruction, and these tests are about the message.

// TestAnEmptyRegistryExplainsTheMissingImport. Before this, an app that had not imported an
// engine got `unknown driver "postgres_gorm" (registered drivers: [])`, which names the
// symptom and not the cause.
func TestAnEmptyRegistryExplainsTheMissingImport(t *testing.T) {
	withCleanRegistry(t)

	_, err := New(dsconfig.DataSource{Driver: "postgres_gorm"})
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, "no datasource drivers are registered")
	assert.Contains(t, message, "postgres_gorm", "the driver that was asked for")
	assert.Contains(t, message, "db/sql/driver/postgres", "and the import that fixes it")
	assert.Contains(t, message, "db/sql/driver/mysql")
	assert.Contains(t, message, "db/sql/driver/builtin")
}

// TestTheEmptyRegistryHintMentionsSQLServer. driver/builtin is the import a reader reaches for
// when they want "every engine", and it does not bring SQL Server, so the hint has to name that
// engine's own package, the Entra ID package that registers it too, and where the import goes.
// Every import it quotes has to compile; TestEveryQuotedImportExists checks that.
func TestTheEmptyRegistryHintMentionsSQLServer(t *testing.T) {
	withCleanRegistry(t)

	_, err := New(dsconfig.DataSource{Driver: "sqlserver_gorm"})
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, "no datasource drivers are registered")
	assert.Contains(t, message, `_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"`)
	assert.Contains(t, message, "driver/builtin does not include", "builtin is not every engine")
	assert.Contains(t, message, `_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread" to sign in `+
		`to it with Microsoft Entra ID`)
	assert.Contains(t, message, "pkg/provider/bootstrap.go", "the file the app's driver import lives in")
}

// TestAMissingDriverKeyStillReportsItself. The two failures are independent: checking the
// registry first made a config missing its `driver:` key report an import problem instead.
func TestAMissingDriverKeyStillReportsItself(t *testing.T) {
	withCleanRegistry(t)

	_, err := New(dsconfig.DataSource{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'driver' is required")

	// And since the registry also happens to be empty, that is mentioned too — both facts
	// are true and the reader needs both.
	assert.Contains(t, err.Error(), "db/sql/driver/postgres")
}

// TestAMissingDriverKeyWithAPopulatedRegistryOmitsTheHint: the hint is noise when something
// is already registered.
func TestAMissingDriverKeyWithAPopulatedRegistryOmitsTheHint(t *testing.T) {
	withCleanRegistry(t)
	Register("postgres_gorm", stubConstructor)

	_, err := New(dsconfig.DataSource{})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "'driver' is required")
	assert.NotContains(t, err.Error(), "Import the engine you use",
		"the import hint only applies to an empty registry")
}

// TestAnUnknownDriverWithAPopulatedRegistryListsWhatExists, and still points at the
// single-engine packages in case the missing one is simply not imported.
func TestAnUnknownDriverWithAPopulatedRegistryListsWhatExists(t *testing.T) {
	withCleanRegistry(t)
	Register("postgres_gorm", stubConstructor)

	_, err := New(dsconfig.DataSource{Driver: "mysql_gorm"})
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, `unknown driver "mysql_gorm"`)
	assert.Contains(t, message, "postgres_gorm", "the names that do exist")
	assert.Contains(t, message, "db/sql/driver/mysql", "and how to add the missing one")
}

// TestAMissingSQLServerImportNamesItsPackage is the case an app on driver/builtin meets when
// it adds a SQL Server datasource: the registry is populated, and only the engine it just
// configured is missing. The one import that fixes it is the answer, not a list of engines.
func TestAMissingSQLServerImportNamesItsPackage(t *testing.T) {
	withCleanRegistry(t)
	Register("postgres_gorm", stubConstructor)
	Register("mysql_gorm", stubConstructor)

	_, err := New(dsconfig.DataSource{Driver: "sqlserver_gorm", Host: "db.internal", Database: "legacy"})
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, `unknown driver "sqlserver_gorm"`)
	assert.Contains(t, message, "mysql_gorm", "the names that do exist")
	assert.Contains(t, message, `_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"`)
	assert.Contains(t, message, "driver/builtin does not include it")
	assert.Contains(t, message, "pkg/provider/bootstrap.go, next to the other driver import")
	assert.Contains(t, message, `To sign in with Microsoft Entra ID, import `+
		`_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread" instead`)
	assert.NotContains(t, message, "Import the engine you use",
		"the empty-registry hint is for a registry with nothing in it")
}

// TestAMissingBuiltinEngineSaysBuiltinBringsIt. A Postgres-only app that adds a MySQL
// datasource can import either package, and must not be told builtin lacks it.
func TestAMissingBuiltinEngineSaysBuiltinBringsIt(t *testing.T) {
	withCleanRegistry(t)
	Register("postgres_gorm", stubConstructor)

	_, err := New(dsconfig.DataSource{Driver: "mysql_gorm"})
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, `_ "github.com/osbits/gorgany/v2/db/sql/driver/mysql"`)
	assert.Contains(t, message, "driver/builtin imports it too")
	assert.NotContains(t, message, "does not include")
	assert.NotContains(t, message, "azuread")
}

// TestAnUnknownNameListsEveryFrameworkEngine. A name the framework does not ship may be a typo
// or an engine of the app's own, so the message offers every package and the Register route.
func TestAnUnknownNameListsEveryFrameworkEngine(t *testing.T) {
	withCleanRegistry(t)
	Register("postgres_gorm", stubConstructor)

	_, err := New(dsconfig.DataSource{Driver: "oracle_gorm"})
	require.Error(t, err)

	message := err.Error()
	for _, pkg := range []string{"postgres", "mysql", "sqlserver"} {
		assert.Contains(t, message, `_ "github.com/osbits/gorgany/v2/db/sql/driver/`+pkg+`"`)
	}
	assert.Contains(t, message, "driver/builtin does not include sqlserver")
	assert.Contains(t, message, "pkg/provider/bootstrap.go")
	assert.Contains(t, message, "driver.Register")
}

// TestEveryQuotedImportExists: the hints are copied into bootstrap.go as they are, so every
// import they quote must be a package of this module, or the fix they offer fails the build. It
// asks the build tool, since a package that exists only as a string cannot be imported here.
func TestEveryQuotedImportExists(t *testing.T) {
	messages := []string{importHint(nil), missingImportHint("oracle_gorm")}
	for name := range frameworkDrivers {
		messages = append(messages, missingImportHint(name))
	}

	quoted := regexp.MustCompile(`_ "([^"]+)"`)
	paths := map[string]bool{}
	for _, message := range messages {
		for _, match := range quoted.FindAllStringSubmatch(message, -1) {
			paths[match[1]] = true
		}
	}
	require.Contains(t, paths, "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread")

	for path := range paths {
		out, err := exec.Command("go", "list", path).CombinedOutput()
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Skipf("go list unavailable: %v", err)
		}
		assert.NoErrorf(t, err, "the hints quote %s, which is not a package: %s", path, out)
	}
}

// stubConstructor is a registrable constructor that builds nothing, for tests that only
// care whether a name resolves.
func stubConstructor(dsconfig.DataSource) (dbCore.IDataSource, error) {
	return &fakeDataSource{}, nil
}
