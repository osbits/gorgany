package v2

import (
	"context"
	"testing"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// The datasource flags added alongside the SQL Server engine: which of them MySQL refuses, and
// what it does with the rest. read_only has read_only_test.go to itself.
//
// Every test here runs against 127.0.0.1:1, where nothing listens and a connection is refused
// at once. A constructor that dials therefore fails, fast, so a constructor that succeeds
// there opened nothing, and a refusal that comes back as an UnsupportedError came back before
// anything was dialled.

var _ dbCore.PolicyReporter = (*gormMySQLDataSource)(nil)

// unreachableConfig is a config for a server nobody runs.
func unreachableConfig() dsconfig.DataSource {
	cfg := mysqlConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 1
	return cfg
}

// lazyConfig is unreachableConfig with lazy_connect, which the constructor survives only if it
// dials nothing.
func lazyConfig() dsconfig.DataSource {
	cfg := unreachableConfig()
	cfg.LazyConnect = true
	return cfg
}

// newDataSource constructs a datasource that must construct, and closes it when the test ends.
func newDataSource(t *testing.T, cfg dsconfig.DataSource) *gormMySQLDataSource {
	t.Helper()

	ds, err := NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })

	my, ok := ds.(*gormMySQLDataSource)
	require.True(t, ok)
	return my
}

// gormOf is what an app gets from GetDriver.
func gormOf(t *testing.T, ds dbCore.IDataSource) *gorm.DB {
	t.Helper()

	driver, err := ds.GetDriver()
	require.NoError(t, err)
	db, ok := driver.(*gorm.DB)
	require.True(t, ok)
	return db
}

// hintOf returns the hint of the UnsupportedError err is.
func hintOf(t *testing.T, err error) string {
	t.Helper()

	var unsupported *dbCore.UnsupportedError
	require.ErrorAs(t, err, &unsupported)
	return unsupported.Hint
}

// TestInstanceIsRefused: a named instance is SQL Server's, so a config naming one was written
// for another engine, and connecting to whatever answers on the default port would hide that.
func TestInstanceIsRefused(t *testing.T) {
	_, err := NewDataSource(map[string]any{
		"driver":   "mysql_gorm",
		"host":     "127.0.0.1",
		"port":     1,
		"db":       "Example-db",
		"instance": "legacy",
	})

	requireUnsupported(t, err, "instance")
	assert.Contains(t, hintOf(t, err), "SQL Server")
}

// TestNonSQLAuthIsRefused: MySQL signs in with the top-level username and password and nothing
// else, so an auth block asking for anything more is refused rather than dropped, and the
// refusal never repeats a value from the block.
func TestNonSQLAuthIsRefused(t *testing.T) {
	const secret = "stand-in-client-secret"

	tests := []struct {
		name      string
		auth      dsconfig.Auth
		construct string
	}{
		{
			name: "a service principal",
			auth: dsconfig.Auth{
				Method:       "service_principal",
				TenantID:     "00000000-0000-0000-0000-000000000000",
				ClientID:     "00000000-0000-0000-0000-000000000000",
				ClientSecret: secret,
			},
			construct: `auth method "service_principal"`,
		},
		{name: "an interactive sign-in", auth: dsconfig.Auth{Method: "interactive"}, construct: `auth method "interactive"`},
		{
			name:      "method sql with a tenant",
			auth:      dsconfig.Auth{Method: "sql", TenantID: "00000000-0000-0000-0000-000000000000"},
			construct: "auth settings other than method",
		},
		{
			name:      "no method but a secret",
			auth:      dsconfig.Auth{ClientSecret: secret},
			construct: "auth settings other than method",
		},
		{
			name:      "method sql with a login timeout",
			auth:      dsconfig.Auth{Method: "sql", LoginTimeout: 1},
			construct: "auth settings other than method",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := unreachableConfig()
			cfg.Auth = tt.auth

			_, err := NewDataSourceWithConfig(cfg)

			requireUnsupported(t, err, tt.construct)
			assert.NotContains(t, err.Error(), secret)
		})
	}

	t.Run("from the config map", func(t *testing.T) {
		_, err := NewDataSource(map[string]any{
			"driver": "mysql_gorm",
			"host":   "127.0.0.1",
			"port":   1,
			"db":     "Example-db",
			"auth":   map[string]any{"method": "managed_identity"},
		})

		requireUnsupported(t, err, `auth method "managed_identity"`)
	})
}

// TestSQLAuthIsAccepted: method sql, and an absent auth block, are exactly what MySQL does. A
// hand-built config is not folded by Parse, so the method is matched regardless of case and
// surrounding space here too.
func TestSQLAuthIsAccepted(t *testing.T) {
	for _, auth := range []dsconfig.Auth{{}, {Method: "sql"}, {Method: " SQL "}} {
		cfg := lazyConfig()
		cfg.Auth = auth

		newDataSource(t, cfg)
	}
}

// TestLazyConnectDisablesThePing: with lazy_connect the constructor opens nothing, so the
// first query makes the first connection. Without it the same config fails to construct,
// because the driver asks the server its version and gorm pings.
func TestLazyConnectDisablesThePing(t *testing.T) {
	ds := newDataSource(t, lazyConfig())
	assert.True(t, gormOf(t, ds).Config.DisableAutomaticPing)

	_, err := NewDataSourceWithConfig(unreachableConfig())
	require.Error(t, err, "without lazy_connect an unreachable server must still fail the constructor")
	assert.Contains(t, err.Error(), "mysql: cannot open connection")
}

// TestLazyConnectDoesNotQueryTheVersionOnMySQL: gorm.io/driver/mysql runs SELECT VERSION()
// while it initialises, before gorm's ping and whatever the ping is set to, so skipping the
// ping alone would still dial. lazy_connect has to switch the query off as well, and the
// constructor surviving 127.0.0.1:1 is the proof that it did: the query would have failed
// there.
func TestLazyConnectDoesNotQueryTheVersionOnMySQL(t *testing.T) {
	db := gormOf(t, newDataSource(t, lazyConfig()))

	dialector, ok := db.Dialector.(*mysql.Dialector)
	require.True(t, ok, "gorm.io/driver/mysql's dialector must be the one in use")
	assert.True(t, dialector.SkipInitializeWithVersion)
	assert.Empty(t, dialector.ServerVersion, "nothing was asked, so nothing was answered")
}

// TestPolicyReportsTheExternalSchemaFlag: the policy is what the commands and providers ask
// before they change a schema, so it has to say what the config said.
func TestPolicyReportsTheExternalSchemaFlag(t *testing.T) {
	external := lazyConfig()
	external.ExternalSchema = true

	ds := newDataSource(t, external)
	assert.Equal(t, dbCore.DataSourcePolicy{ExternalSchema: true}, dbCore.PolicyOf(ds))
	assert.True(t, dbCore.IsExternalSchema(ds))
	assert.False(t, dbCore.IsReadOnly(ds))

	owned := newDataSource(t, lazyConfig())
	assert.Equal(t, dbCore.DataSourcePolicy{}, dbCore.PolicyOf(owned))
}

// TestExternalSchemaInstallsTheDDLGuard: with external_schema, DDL sent on the handle an app
// gets from GetDriver is refused before it reaches the server, with gorm's Migrator as with
// Exec, and with statement logging on as with it off, since that handle is derived after the
// guard is installed. Rows may still be written, and without the flag nothing is refused.
//
// Nothing listens on the configured port, so a statement that reaches the driver fails with a
// connection error: one that fails with ErrExternalSchema was never sent.
func TestExternalSchemaInstallsTheDDLGuard(t *testing.T) {
	for name, logged := range map[string]bool{"without statement logging": false, "with statement logging": true} {
		t.Run(name, func(t *testing.T) {
			cfg := lazyConfig()
			cfg.ExternalSchema = true
			cfg.Log = logged
			db := gormOf(t, newDataSource(t, cfg))

			assert.ErrorIs(t, db.Exec("CREATE TABLE legacy (id int)").Error, dbCore.ErrExternalSchema)
			assert.ErrorIs(t, db.Exec("RENAME TABLE legacy TO legacy_old").Error, dbCore.ErrExternalSchema)
			assert.ErrorIs(t, db.Migrator().CreateTable(&legacyRow{}), dbCore.ErrExternalSchema)

			// MySQL runs the text of a /*! … */ comment, and the MySQL lexicon knows it.
			assert.ErrorIs(t, db.Exec("SELECT 1; /*! DROP TABLE legacy */").Error, dbCore.ErrExternalSchema)

			// A write is not DDL, so it goes on to the server, which is not there.
			err := db.Exec("UPDATE legacy SET id = 1").Error
			require.Error(t, err, "the write must reach the absent server")
			assert.NotErrorIs(t, err, dbCore.ErrExternalSchema)
		})
	}

	t.Run("what gorm builds and what the executor sends", func(t *testing.T) {
		cfg := lazyConfig()
		cfg.ExternalSchema = true
		ds := newDataSource(t, cfg)
		db := gormOf(t, ds)

		// gorm builds these statements after the guard's callbacks have run; the guarded
		// pool checks them as they are sent.
		var rows []legacyRow
		assert.ErrorIs(t, db.Where("1=1; DROP TABLE legacy").Find(&rows).Error, dbCore.ErrExternalSchema)
		assert.ErrorIs(t, db.Select("* INTO legacy_copy").Find(&rows).Error, dbCore.ErrExternalSchema)
		assert.ErrorIs(t, db.Table("legacy; DROP TABLE legacy --").Find(&rows).Error, dbCore.ErrExternalSchema)

		// ExecInsert sends on the pool directly, and an INSERT is no schema change: it goes on
		// to the server, which is not there.
		session, err := ds.NewSession()
		require.NoError(t, err)
		insert := session.Query().Insert("legacy").Columns("id").Values(1)
		executor, ok := session.Executor().(dbCore.LastInsertIDExecutor)
		require.True(t, ok)
		err = executor.ExecInsert(context.Background(), insert).Error
		require.Error(t, err, "the INSERT must reach the absent server")
		assert.NotErrorIs(t, err, dbCore.ErrExternalSchema)
	})

	t.Run("without the flag", func(t *testing.T) {
		db := gormOf(t, newDataSource(t, lazyConfig()))

		err := db.Exec("CREATE TABLE legacy (id int)").Error
		require.Error(t, err, "the DDL must reach the absent server")
		assert.NotErrorIs(t, err, dbCore.ErrExternalSchema, "a datasource gorgany owns must not get the guard")
	})
}

type legacyRow struct {
	ID int64
}

func (legacyRow) TableName() string { return "legacy" }
