package driver

import (
	"testing"

	dsconfig "github.com/osbits/gorgany/db/sql/config"
	dbCore "github.com/osbits/gorgany/db/sql/core"
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

// stubConstructor is a registrable constructor that builds nothing, for tests that only
// care whether a name resolves.
func stubConstructor(dsconfig.DataSource) (dbCore.IDataSource, error) {
	return &fakeDataSource{}, nil
}
