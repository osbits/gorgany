package driver

import (
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
// engine's own package, and where the import goes. It names no package that does not exist:
// every import it quotes has to compile.
func TestTheEmptyRegistryHintMentionsSQLServer(t *testing.T) {
	withCleanRegistry(t)

	_, err := New(dsconfig.DataSource{Driver: "sqlserver_gorm"})
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, "no datasource drivers are registered")
	assert.Contains(t, message, `_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"`)
	assert.Contains(t, message, "driver/builtin does not include", "builtin is not every engine")
	assert.NotContains(t, message, "azuread", "no Entra ID package ships in this release")
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
	assert.NotContains(t, message, "azuread", "no Entra ID package ships in this release")
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

// stubConstructor is a registrable constructor that builds nothing, for tests that only
// care whether a name resolves.
func stubConstructor(dsconfig.DataSource) (dbCore.IDataSource, error) {
	return &fakeDataSource{}, nil
}
