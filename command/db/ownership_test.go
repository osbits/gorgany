package db

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	grglog "github.com/osbits/gorgany/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The recording driver these tests run on is in migrate_test.go. A refusal must leave its
// log empty: no CREATE TABLE "migrations" or "seeders", no BEGIN, not even a lookup.

var (
	externalSchema = dbCore.DataSourcePolicy{ExternalSchema: true}
	readOnly       = dbCore.DataSourcePolicy{ReadOnly: true}
)

// unownedPolicies are the two configurations every schema-changing command refuses, each
// with the sentinel its refusal wraps and the setting its message names.
var unownedPolicies = []struct {
	name     string
	policy   dbCore.DataSourcePolicy
	sentinel error
	setting  string
	// targetRefusal is what a migration or seeder aimed at such a datasource is told, after
	// its description.
	targetRefusal string
}{
	{"external_schema", externalSchema, dbCore.ErrExternalSchema, "external_schema: true",
		` targets datasource "legacy", which is external_schema: true; gorgany runs no migration or ` +
			`seeder on a datasource whose schema it does not own, and keeps no migrations/seeders table ` +
			`there — delete it or retarget it at a datasource gorgany owns`},
	{"read_only", readOnly, dbCore.ErrReadOnly, "read_only: true",
		` targets datasource "legacy", which is read_only: true; gorgany runs no migration or seeder ` +
			`on a datasource it may not write to, and keeps no migrations/seeders table there — delete it ` +
			`or retarget it at a writable datasource`},
}

// recordedContext returns a context whose `default` is owned and writable and whose
// `legacy` has the given policy. Both sit on r, so a statement either one sends is logged.
func recordedContext(t *testing.T, r *recording, legacy dbCore.DataSourcePolicy) *fakeDBContext {
	t.Helper()

	gormDb := recordingGorm(t, r)
	return &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": &fakeDataSource{driver: gormDb},
		"legacy":  &fakeDataSource{driver: gormDb, policy: legacy},
	}}
}

// panicError runs fn and returns the error it panicked with. A refusal is a configuration
// error, which the db commands report by panicking (exit 2), as they do for a datasource
// that is not configured.
func panicError(t *testing.T, fn func()) (err error) {
	t.Helper()

	defer func() {
		recovered := recover()
		require.NotNil(t, recovered, "expected a panic")
		var ok bool
		err, ok = recovered.(error)
		require.Truef(t, ok, "expected the panic to carry an error, got %v", recovered)
	}()
	fn()
	return nil
}

// policylessDataSource is a datasource an app wrote before PolicyReporter existed.
type policylessDataSource struct{ driver any }

func (p *policylessDataSource) NewSession() (dbCore.ISession, error) { return nil, nil }
func (p *policylessDataSource) GetDriver() (any, error)              { return p.driver, nil }
func (p *policylessDataSource) Close() error                         { return nil }

// scopedSeeder is a seeder that declares which datasource it targets.
type scopedSeeder struct {
	modelSeeder
	target string
}

func (s scopedSeeder) DataSourceName() string { return s.target }

// ------------------------------------------------ RequireOwnedDatasource / ResolveOwnedGorm

func TestResolveOwnedGormRefusesAnExternalSchema(t *testing.T) {
	r := &recording{}

	gormDb, err := ResolveOwnedGorm(recordedContext(t, r, externalSchema), "legacy", "db:migrate")

	assert.Nil(t, gormDb)
	require.ErrorIs(t, err, dbCore.ErrExternalSchema)
	assert.NotErrorIs(t, err, dbCore.ErrReadOnly)
	assert.EqualError(t, err, `db:migrate refuses datasource "legacy": its schema is owned outside gorgany `+
		`(external_schema: true) — gorgany never creates, alters or records anything there, not even its own `+
		`migrations/seeders tables; select a datasource gorgany owns with --datasource, or remove `+
		`external_schema from databases.legacy if gorgany owns that schema after all`)
	assert.Empty(t, r.log)
}

func TestResolveOwnedGormRefusesAReadOnlyDatasource(t *testing.T) {
	r := &recording{}

	gormDb, err := ResolveOwnedGorm(recordedContext(t, r, readOnly), "legacy", "db:seed")

	assert.Nil(t, gormDb)
	require.ErrorIs(t, err, dbCore.ErrReadOnly)
	assert.NotErrorIs(t, err, dbCore.ErrExternalSchema)
	assert.EqualError(t, err, `db:seed refuses datasource "legacy": it is read-only (read_only: true) — `+
		`gorgany writes nothing there, not even its own migrations/seeders tables; select a writable `+
		`datasource with --datasource, or remove read_only from databases.legacy if it should accept writes`)
	assert.Empty(t, r.log)
}

// TestResolveOwnedGormNamesTheSchemaWhenBothFlagsAreSet: external_schema is the stronger
// statement, and removing read_only alone would not make the datasource usable.
func TestResolveOwnedGormNamesTheSchemaWhenBothFlagsAreSet(t *testing.T) {
	r := &recording{}
	both := dbCore.DataSourcePolicy{ExternalSchema: true, ReadOnly: true}

	_, err := ResolveOwnedGorm(recordedContext(t, r, both), "legacy", "db:diff")

	require.ErrorIs(t, err, dbCore.ErrExternalSchema)
	assert.NotErrorIs(t, err, dbCore.ErrReadOnly)
	assert.EqualError(t, err, `db:diff refuses datasource "legacy": its schema is owned outside gorgany `+
		`(external_schema: true) — gorgany never creates, alters or records anything there, not even its own `+
		`migrations/seeders tables; select a datasource gorgany owns with --datasource, or remove `+
		`external_schema from databases.legacy if gorgany owns that schema after all`)
	assert.Empty(t, r.log)
}

// TestResolveOwnedGormKeepsTheUnknownDatasourceMessage: a typo in --datasource reads the
// same whether a command resolves through ResolveGorm or ResolveOwnedGorm.
func TestResolveOwnedGormKeepsTheUnknownDatasourceMessage(t *testing.T) {
	ctx := &fakeDBContext{}

	_, want := ResolveGorm(ctx, "legacy")
	require.EqualError(t, want,
		"datasource \"legacy\" is not configured; check the `databases` config key and the --datasource flag")

	_, err := ResolveOwnedGorm(ctx, "legacy", "db:migrate")
	assert.EqualError(t, err, want.Error())
	assert.EqualError(t, RequireOwnedDatasource(ctx, "legacy", "db:seed"), want.Error())

	_, want = ResolveGorm(nil, "default")
	require.EqualError(t, want, "no database context available")
	_, err = ResolveOwnedGorm(nil, "default", "db:diff")
	assert.EqualError(t, err, want.Error())
}

func TestResolveOwnedGormReturnsAnOwnedDatasource(t *testing.T) {
	owned := &fakeDataSource{driver: gormWithDialector(postgres.New(postgres.Config{}))}
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": owned,
		"reports": &policylessDataSource{driver: owned.driver},
	}}

	got, err := ResolveOwnedGorm(ctx, "default", "db:migrate")
	require.NoError(t, err)
	assert.Same(t, owned.driver, got)

	// A datasource that does not report a policy is owned and writable, which is what
	// gorgany assumed of every datasource before the flags existed.
	got, err = ResolveOwnedGorm(ctx, "reports", "db:migrate")
	require.NoError(t, err)
	assert.Same(t, owned.driver, got)
	assert.NoError(t, RequireOwnedDatasource(ctx, "reports", "db:migrate"))
}

// TestResolveGormIsUnchangedForPolicyDatasources: ResolveGorm resolves what it always
// did. Asking about ownership is ResolveOwnedGorm's job, so a caller that only reads, such
// as a health check, is not refused.
func TestResolveGormIsUnchangedForPolicyDatasources(t *testing.T) {
	for _, p := range unownedPolicies {
		t.Run(p.name, func(t *testing.T) {
			r := &recording{}
			ctx := recordedContext(t, r, p.policy)

			got, err := ResolveGorm(ctx, "legacy")

			require.NoError(t, err)
			assert.NotNil(t, got)
			assert.Empty(t, r.log)
		})
	}
}

// TestRequireOwnedDatasourceDecidesOnTheConfigurationAlone: the refusal does not depend
// on the driver, so a datasource whose driver cannot even be had is still told why.
func TestRequireOwnedDatasourceDecidesOnTheConfigurationAlone(t *testing.T) {
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"legacy":  &fakeDataSource{err: assert.AnError, policy: externalSchema},
		"reports": &fakeDataSource{driver: "not a gorm handle", policy: readOnly},
	}}

	_, err := ResolveOwnedGorm(ctx, "legacy", "db:migrate")
	require.ErrorIs(t, err, dbCore.ErrExternalSchema)
	assert.NotErrorIs(t, err, assert.AnError)

	_, err = ResolveOwnedGorm(ctx, "reports", "db:migrate")
	require.ErrorIs(t, err, dbCore.ErrReadOnly)
	assert.NotContains(t, err.Error(), "not GORM-backed")
}

// TestEveryDbCommandKeepsTheUnknownDatasourceMessage: resolving through ResolveOwnedGorm
// changed nothing about a datasource that is not configured.
func TestEveryDbCommandKeepsTheUnknownDatasourceMessage(t *testing.T) {
	const want = "datasource \"legacy\" is not configured; check the `databases` config key and the --datasource flag"

	commands := []struct {
		args []string
		run  func()
	}{
		{[]string{"db:migrate", "up", "--datasource=legacy"}, func() {
			MigrateCommand{dataContext: &stubDataContext{}, dbContext: &fakeDBContext{}}.Execute(nil)
		}},
		{[]string{"db:migrate", "down", "--datasource=legacy"}, func() {
			MigrateCommand{dataContext: &stubDataContext{}, dbContext: &fakeDBContext{}}.Execute(nil)
		}},
		{[]string{"db:seed", "--datasource=legacy"}, func() {
			SeedCommand{dataContext: &stubDataContext{}, dbContext: &fakeDBContext{}}.Execute(nil)
		}},
		{[]string{"db:diff", "--datasource=legacy"}, func() {
			DiffCommand{dbContext: &fakeDBContext{}}.Execute(nil)
		}},
	}

	for _, c := range commands {
		t.Run(strings.Join(c.args[:len(c.args)-1], " "), func(t *testing.T) {
			withArgs(t, c.args...)
			assert.PanicsWithError(t, want, c.run)
		})
	}
}

// ----------------------------------------------------------------------------- db:migrate

// TestMigrateRefusesAnUnownedDatasourceBeforeAnySQL: up used to create `migrations` in
// whatever datasource was selected before anything else, and down did the same before
// reading it. On a datasource gorgany does not own, that first statement was the damage.
func TestMigrateRefusesAnUnownedDatasourceBeforeAnySQL(t *testing.T) {
	for _, p := range unownedPolicies {
		for _, direction := range []string{"up", "down"} {
			t.Run(p.name+" "+direction, func(t *testing.T) {
				withArgs(t, "db:migrate", direction, "--datasource=legacy")
				r := &recording{}
				cmd := MigrateCommand{
					dataContext: &stubDataContext{migrations: []core.IMigration{
						execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"},
					}},
					dbContext: recordedContext(t, r, p.policy),
				}

				err := panicError(t, func() { cmd.Execute(nil) })

				require.ErrorIs(t, err, p.sentinel)
				assert.Contains(t, err.Error(), `db:migrate refuses datasource "legacy"`)
				assert.Contains(t, err.Error(), p.setting)
				assert.Empty(t, r.log, "nothing may reach the database")
			})
		}
	}
}

// TestAMigrationTargetingAnUnownedDatasourceIsAnError: every run on its datasource is
// refused, so it could never be applied. Skipping it on the other datasources' runs would
// leave it registered, never applied and never mentioned.
func TestAMigrationTargetingAnUnownedDatasourceIsAnError(t *testing.T) {
	for _, p := range unownedPolicies {
		for _, direction := range []string{"up", "down"} {
			t.Run(p.name+" "+direction, func(t *testing.T) {
				withArgs(t, "db:migrate", direction)
				r := &recording{}
				cmd := MigrateCommand{
					dataContext: &stubDataContext{migrations: []core.IMigration{
						execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"},
						scopedMigration{name: "legacy_orders", target: "legacy"},
					}},
					dbContext: recordedContext(t, r, p.policy),
				}

				err := panicError(t, func() { cmd.Execute(nil) })

				require.ErrorIs(t, err, p.sentinel)
				assert.EqualError(t, err, `migration "legacy_orders"`+p.targetRefusal)
				assert.Empty(t, r.log, "the migration for default must not run either")
			})
		}
	}
}

// TestMigrateUpWithNothingToRunTouchesNothing: the `migrations` table used to be created
// on every run, so a datasource no migration targets got one anyway.
func TestMigrateUpWithNothingToRunTouchesNothing(t *testing.T) {
	for name, migrations := range map[string][]core.IMigration{
		"none registered":       nil,
		"all for another owned": {scopedMigration{name: "reports_only", target: "reports"}},
	} {
		t.Run(name, func(t *testing.T) {
			withArgs(t, "db:migrate", "up")
			r := &recording{}
			gormDb := recordingGorm(t, r)
			cmd := MigrateCommand{
				dataContext: &stubDataContext{migrations: migrations},
				dbContext: &fakeDBContext{sources: map[string]dbCore.IDataSource{
					"default": &fakeDataSource{driver: gormDb},
					"reports": &fakeDataSource{driver: gormDb},
				}},
			}

			logged := captureCommandLog(t)
			require.NotPanics(t, func() { cmd.Execute(nil) })
			assert.Empty(t, r.log)
			assert.Contains(t, logged.lines(), `No migrations target datasource "default"`)
			assert.NotContains(t, logged.text(), "Migrating datasource")
		})
	}
}

// TestMigrateUpCreatesTheTableWhenAMigrationMatches: the table is still created, before
// the first migration that needs it.
func TestMigrateUpCreatesTheTableWhenAMigrationMatches(t *testing.T) {
	withArgs(t, "db:migrate", "up")
	r := &recording{}
	cmd := MigrateCommand{
		dataContext: &stubDataContext{migrations: []core.IMigration{
			execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"},
		}},
		dbContext: recordedContext(t, r, dbCore.DataSourcePolicy{}),
	}

	require.NotPanics(t, func() { cmd.Execute(nil) })

	writes := withoutReads(r.log)
	require.NotEmpty(t, writes)
	assert.True(t, strings.HasPrefix(writes[0], `pool: CREATE TABLE "migrations"`),
		"the bookkeeping table comes first, got %q", writes[0])
	assert.Equal(t, []string{"BEGIN", "tx: CREATE TABLE widgets (id int)", recordSQL, "COMMIT"},
		writes[len(writes)-4:])
}

func TestMigrateDownWithNothingMatchingTouchesNothing(t *testing.T) {
	withArgs(t, "db:migrate", "down")
	r := &recording{}
	gormDb := recordingGorm(t, r)
	cmd := MigrateCommand{
		dataContext: &stubDataContext{migrations: []core.IMigration{
			scopedMigration{name: "reports_only", target: "reports"},
		}},
		dbContext: &fakeDBContext{sources: map[string]dbCore.IDataSource{
			"default": &fakeDataSource{driver: gormDb},
			"reports": &fakeDataSource{driver: gormDb},
		}},
	}

	logged := captureCommandLog(t)
	require.NotPanics(t, func() { cmd.Execute(nil) })
	assert.Empty(t, r.log)
	assert.Contains(t, logged.lines(), `Nothing to roll back on datasource "default": no migrations target it`)
}

// TestMigrateDownWithoutTheTableIsANoOp: down used to create the table before reading
// it. Now it only asks whether the table is there, since a failed read of it is an
// error and there is nothing to roll back in a table that does not exist.
func TestMigrateDownWithoutTheTableIsANoOp(t *testing.T) {
	withArgs(t, "db:migrate", "down")
	r := &recording{} // answers every lookup with no rows, so HasTable is false
	cmd := MigrateCommand{
		dataContext: &stubDataContext{migrations: []core.IMigration{
			execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"},
		}},
		dbContext: recordedContext(t, r, dbCore.DataSourcePolicy{}),
	}

	logged := captureCommandLog(t)
	require.NotPanics(t, func() { cmd.Execute(nil) })
	require.Len(t, r.log, 1, "only the table lookup may run: %v", r.log)
	assert.Contains(t, r.log[0], "information_schema.tables")
	assert.Contains(t, logged.lines(), "Nothing to roll back on datasource \"default\": it has no `migrations` table")
}

// TestMigrateDownReportsAFailedTableLookup: gorm's HasTable answers false when its lookup
// fails, so a rollback on a database it never reached — an unreachable server, a bad
// credential under lazy_connect — logged that there was no table and exited 0. The lookup's
// error is a configuration-time failure like the read of the table below it: exit 2.
func TestMigrateDownReportsAFailedTableLookup(t *testing.T) {
	withArgs(t, "db:migrate", "down")
	r := &recording{failQuery: "information_schema"}
	cmd := MigrateCommand{
		dataContext: &stubDataContext{migrations: []core.IMigration{
			execMigration{name: "m1", sql: "CREATE TABLE widgets (id int)"},
		}},
		dbContext: recordedContext(t, r, dbCore.DataSourcePolicy{}),
	}
	logged := captureCommandLog(t)

	err := panicError(t, func() { cmd.Execute(nil) })

	require.ErrorIs(t, err, errInjected)
	assert.EqualError(t, err, "cannot tell whether datasource \"default\" has a `migrations` table: "+errInjected.Error())
	assert.NotContains(t, logged.text(), "Nothing to roll back")
	require.Len(t, r.log, 1, "nothing may run after the failed lookup: %v", r.log)
}

// ---------------------------------------------------------------------- OnOwnedDefault

// TestOnOwnedDefaultIsLeftOutWhereDefaultCannotTakeIt: the sessions migrations reach every
// app through DbProvider.Boot. Where `default` is missing, external_schema or read_only when
// db:migrate runs, they are left out with a line saying why, rather than failing the run as
// a migration aimed at such a datasource does, and the run goes on with the rest.
func TestOnOwnedDefaultIsLeftOutWhereDefaultCannotTakeIt(t *testing.T) {
	for name, c := range map[string]struct {
		sources map[string]dbCore.IDataSource
		reason  string
	}{
		"no default": {
			map[string]dbCore.IDataSource{"reports": nil},
			`no "default" datasource is configured, and it runs only there`,
		},
		"external_schema default": {
			map[string]dbCore.IDataSource{"default": &fakeDataSource{policy: externalSchema}, "reports": nil},
			`datasource "default" refuses it: ` + dbCore.ErrExternalSchema.Error(),
		},
		"read_only default": {
			map[string]dbCore.IDataSource{"default": &fakeDataSource{policy: readOnly}, "reports": nil},
			`datasource "default" refuses it: ` + dbCore.ErrReadOnly.Error(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			withArgs(t, "db:migrate", "up", "--datasource=reports")
			r := &recording{}
			c.sources["reports"] = &fakeDataSource{driver: recordingGorm(t, r)}
			cmd := MigrateCommand{
				dataContext: &stubDataContext{migrations: []core.IMigration{
					OnOwnedDefault(execMigration{name: "create_sessions_table", sql: "CREATE TABLE sessions (id int)"}),
					scopedMigration{name: "reports_table", target: "reports"},
				}},
				dbContext: &fakeDBContext{sources: c.sources},
			}
			logged := captureCommandLog(t)

			require.NotPanics(t, func() { cmd.Execute(nil) })

			assert.Contains(t, logged.lines(), "Skipping migration create_sessions_table: "+c.reason)
			assert.Contains(t, logged.lines(), `Migrating datasource "reports"`, "the rest still run")
			assert.NotContains(t, strings.Join(r.log, "\n"), "sessions")
		})
	}
}

// TestOnOwnedDefaultRunsOnAnOwnedDefault: on a `default` gorgany owns it is an ordinary
// migration, and one that default has at the time db:migrate runs counts, however it was
// registered.
func TestOnOwnedDefaultRunsOnAnOwnedDefault(t *testing.T) {
	withArgs(t, "db:migrate", "up")
	r := &recording{}
	cmd := MigrateCommand{
		dataContext: &stubDataContext{migrations: []core.IMigration{
			OnOwnedDefault(execMigration{name: "create_sessions_table", sql: "CREATE TABLE sessions (id int)"}),
		}},
		dbContext: recordedContext(t, r, dbCore.DataSourcePolicy{}),
	}
	logged := captureCommandLog(t)

	require.NotPanics(t, func() { cmd.Execute(nil) })

	assert.Equal(t, []string{"BEGIN", "tx: CREATE TABLE sessions (id int)", recordSQL, "COMMIT"},
		withoutReads(r.log)[len(withoutReads(r.log))-4:])
	assert.NotContains(t, logged.text(), "Skipping migration create_sessions_table")

	// And on another datasource's run it is skipped as any migration for `default` is.
	withArgs(t, "db:migrate", "up", "--datasource=legacy")
	logged = captureCommandLog(t)
	cmd.dbContext = recordedContext(t, &recording{}, dbCore.DataSourcePolicy{})
	require.NotPanics(t, func() { cmd.Execute(nil) })
	assert.Contains(t, logged.lines(),
		`Skipping migration create_sessions_table: it targets datasource "default", not "legacy"`)
}

// -------------------------------------------------------------------------------- db:seed

func TestSeedRefusesAnUnownedDatasourceBeforeAnySQL(t *testing.T) {
	for _, p := range unownedPolicies {
		t.Run(p.name, func(t *testing.T) {
			withArgs(t, "db:seed", "--datasource=legacy")
			r := &recording{}
			cmd := SeedCommand{
				dataContext: &stubDataContext{seeders: []core.ISeeder{
					scopedSeeder{modelSeeder: twoModels("s1"), target: "legacy"},
				}},
				dbContext: recordedContext(t, r, p.policy),
			}

			err := panicError(t, func() { cmd.Execute(nil) })

			require.ErrorIs(t, err, p.sentinel)
			assert.Contains(t, err.Error(), `db:seed refuses datasource "legacy"`)
			assert.Empty(t, r.log, "nothing may reach the database")
		})
	}
}

func TestASeederTargetingAnUnownedDatasourceIsAnError(t *testing.T) {
	for _, p := range unownedPolicies {
		t.Run(p.name, func(t *testing.T) {
			withArgs(t, "db:seed")
			r := &recording{}
			cmd := SeedCommand{
				dataContext: &stubDataContext{seeders: []core.ISeeder{
					twoModels("s1"),
					scopedSeeder{modelSeeder: twoModels("legacy_rows"), target: "legacy"},
				}},
				dbContext: recordedContext(t, r, p.policy),
			}

			err := panicError(t, func() { cmd.Execute(nil) })

			require.ErrorIs(t, err, p.sentinel)
			assert.EqualError(t, err, `seeder "legacy_rows"`+p.targetRefusal)
			assert.Empty(t, r.log, "the seeder for default must not run either")
		})
	}
}

// TestSeedWithNothingToRunTouchesNothing: the `seeders` table used to be created before
// the seeders were even partitioned.
func TestSeedWithNothingToRunTouchesNothing(t *testing.T) {
	withArgs(t, "db:seed")
	r := &recording{}
	gormDb := recordingGorm(t, r)
	cmd := SeedCommand{
		dataContext: &stubDataContext{seeders: []core.ISeeder{
			scopedSeeder{modelSeeder: twoModels("reports_rows"), target: "reports"},
		}},
		dbContext: &fakeDBContext{sources: map[string]dbCore.IDataSource{
			"default": &fakeDataSource{driver: gormDb},
			"reports": &fakeDataSource{driver: gormDb},
		}},
	}

	logged := captureCommandLog(t)
	require.NotPanics(t, func() { cmd.Execute(nil) })
	assert.Empty(t, r.log)
	assert.Contains(t, logged.lines(), `No seeders target datasource "default"`)
	assert.NotContains(t, logged.text(), "Seeding datasource")
}

func TestSeedCreatesTheTableWhenASeederMatches(t *testing.T) {
	withArgs(t, "db:seed")
	r := &recording{}
	cmd := SeedCommand{
		dataContext: &stubDataContext{seeders: []core.ISeeder{twoModels("s1")}},
		dbContext:   recordedContext(t, r, dbCore.DataSourcePolicy{}),
	}

	require.NotPanics(t, func() { cmd.Execute(nil) })

	writes := withoutReads(r.log)
	require.NotEmpty(t, writes)
	assert.True(t, strings.HasPrefix(writes[0], `pool: CREATE TABLE "seeders"`),
		"the bookkeeping table comes first, got %q", writes[0])
	assert.Equal(t, []string{"BEGIN", rowInsertSQL, tagInsertSQL, seedRecordSQL, "COMMIT"},
		writes[len(writes)-5:])
}

// -------------------------------------------------------------------------------- db:diff

// TestDiffRefusesAnUnownedDatasourceBeforeAnySQL: the diff runs real CREATE TABLE and
// ALTER TABLE in a transaction, so a read_only datasource is refused as well as an
// external one. The domain context is nil: the refusal must come before the domains are
// even asked for.
func TestDiffRefusesAnUnownedDatasourceBeforeAnySQL(t *testing.T) {
	for _, p := range unownedPolicies {
		t.Run(p.name, func(t *testing.T) {
			withArgs(t, "db:diff", "--datasource=legacy")
			r := &recording{}
			cmd := DiffCommand{dbContext: recordedContext(t, r, p.policy)}

			err := panicError(t, func() { cmd.Execute(nil) })

			require.ErrorIs(t, err, p.sentinel)
			assert.Contains(t, err.Error(), `db:diff refuses datasource "legacy"`)
			assert.Empty(t, r.log, "no BEGIN, no DDL")
		})
	}
}

// TestDiffsPolicyRefusalWinsOverTheTransactionalDDLRefusal: on a dialect db:diff does
// not support, an unowned datasource is told that it is unowned. "Its dialect commits DDL
// immediately" would suggest that another dialect would do.
func TestDiffsPolicyRefusalWinsOverTheTransactionalDDLRefusal(t *testing.T) {
	for _, p := range unownedPolicies {
		t.Run(p.name, func(t *testing.T) {
			withArgs(t, "db:diff", "--datasource=legacy")
			cmd := DiffCommand{dbContext: &fakeDBContext{sources: map[string]dbCore.IDataSource{
				"legacy": &fakeDataSource{driver: gormWithDialector(mysql.New(mysql.Config{})), policy: p.policy},
			}}}

			err := panicError(t, func() { cmd.Execute(nil) })

			require.ErrorIs(t, err, p.sentinel)
			assert.NotContains(t, err.Error(), "commits DDL immediately")
		})
	}
}

type diffWidget struct {
	ID    uint
	Label string
}

type legacyOrder struct {
	ID    uint
	Total int
}

func (legacyOrder) DbConnectionName() string { return "legacy" }

// TestDiffDiffsOnlyTheSelectedDatasourcesDomains: every registered domain used to be
// diffed, so a table another system owns was drafted as a new table for `default`.
func TestDiffDiffsOnlyTheSelectedDatasourcesDomains(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pkg", "domain"), 0o755))
	t.Chdir(dir)

	withArgs(t, "db:diff")
	r := &recording{}
	domains := &stubDomainContext{domains: map[string]any{
		"example.com/app/pkg/domain.Widget":      diffWidget{},
		"example.com/app/pkg/domain.LegacyOrder": legacyOrder{},
	}}
	cmd := DiffCommand{domainContext: domains, dbContext: recordedContext(t, r, externalSchema)}

	output := printed(t, func() { require.NotPanics(t, func() { cmd.Execute(nil) }) })

	joined := strings.Join(r.log, "\n")
	assert.Contains(t, joined, `CREATE TABLE "diff_widgets"`)
	assert.NotContains(t, joined, "legacy_orders", "the external datasource's domain must not be diffed")
	assert.Contains(t, strings.Split(output, "\n"),
		`Skipping domain example.com/app/pkg/domain.LegacyOrder: it belongs to datasource "legacy", not "default"`)
}

// inDiffProject makes a temporary directory the working directory of an app whose module is
// example.com/app, with an empty ./pkg/domain, which is all db:diff reads of a project. The
// migration it drafts is written under it.
func inDiffProject(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pkg", "domain"), 0o755))
	t.Chdir(dir)
	return dir
}

// draftedStatements returns the lines of the migrations db:diff drafted under dir.
func draftedStatements(t *testing.T, dir string) string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(dir, MigrationDir, "*.go"))
	require.NoError(t, err)
	var drafted []string
	for _, file := range files {
		source, err := os.ReadFile(file)
		require.NoError(t, err)
		drafted = append(drafted, string(source))
	}
	return strings.Join(drafted, "\n")
}

// recordedDiffContext is recordedContext on a handle configured as the Postgres datasource
// configures its own, with DisableForeignKeyConstraintWhenMigrating: CREATE TABLE declares no
// foreign key, and each is drafted by migrateModelConstraints.
func recordedDiffContext(t *testing.T, r *recording) *fakeDBContext {
	t.Helper()

	gormDb, err := gorm.Open(postgres.New(postgres.Config{Conn: sql.OpenDB(r)}), &gorm.Config{
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err)
	return &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": &fakeDataSource{driver: gormDb},
		"legacy":  &fakeDataSource{driver: gormDb, policy: externalSchema},
		"reports": &fakeDataSource{driver: gormDb},
	}}
}

// The domains of the cross-datasource cases. Each owned one relates to a domain of another
// datasource, in one of the ways gorm derives DDL from.

type crossLegacyInvoice struct {
	ID              uint
	CrossCustomerID uint
}

func (crossLegacyInvoice) DbConnectionName() string { return "legacy" }

// crossCustomer has many legacy invoices: gorm puts that foreign key on the invoices' table.
type crossCustomer struct {
	ID       uint
	Invoices []crossLegacyInvoice
}

type crossLegacyClient struct{ ID uint }

func (crossLegacyClient) DbConnectionName() string { return "legacy" }

// crossNote belongs to a legacy client: gorm puts that foreign key on the notes' table,
// referencing the clients'.
type crossNote struct {
	ID                  uint
	CrossLegacyClientID uint
	CrossLegacyClient   crossLegacyClient
}

type crossLegacyParty struct {
	ID   uint
	Name string
}

func (crossLegacyParty) DbConnectionName() string { return "legacy" }

// crossPerson extends a legacy party, whose table would get the struct column.
type crossPerson struct {
	crossLegacyParty `grgorm:"extends"`
	Age              int
}

func (crossPerson) DbConnectionName() string { return "default" }

type crossReport struct {
	ID           uint
	CrossTallyID uint
}

func (crossReport) DbConnectionName() string { return "reports" }

// crossTally has many reports of another datasource gorgany owns, whose database need not be
// this one either.
type crossTally struct {
	ID      uint
	Reports []crossReport
}

// TestDiffDraftsNothingOnAnotherDatasourcesTable: the relations of a diffed domain used to
// draft DDL on, or pointing at, the table of a domain db:diff had skipped. In a database
// `default` shares with an external_schema datasource that is DDL gorgany may not run; where
// the other datasource is its own database, the table is not there, and the diff died on the
// driver's error. So the statement is not sent, and not drafted, and the run says so.
func TestDiffDraftsNothingOnAnotherDatasourcesTable(t *testing.T) {
	for name, c := range map[string]struct {
		owned, other any
		table        string // the other datasource's table
		skipped      string // the line the run prints
		kept         string // what the owned domain still drafts
	}{
		"has many": {
			crossCustomer{}, crossLegacyInvoice{}, "cross_legacy_invoices",
			`Skipping the constraint of crossCustomer.Invoices: it involves domain example.com/app/pkg/domain.Other, which belongs to datasource "legacy"`,
			`CREATE TABLE "cross_customers"`,
		},
		"belongs to": {
			crossNote{}, crossLegacyClient{}, "cross_legacy_clients",
			`Skipping the constraint of crossNote.CrossLegacyClient: it involves domain example.com/app/pkg/domain.Other, which belongs to datasource "legacy"`,
			`CREATE TABLE "cross_notes"`,
		},
		"extends": {
			crossPerson{}, crossLegacyParty{}, "cross_legacy_parties",
			`Skipping the model_struct column for crossPerson: the domain it extends, example.com/app/pkg/domain.Other, belongs to datasource "legacy"`,
			`CREATE TABLE "cross_people"`,
		},
		"has many, another owned datasource": {
			crossTally{}, crossReport{}, "cross_reports",
			`Skipping the constraint of crossTally.Reports: it involves domain example.com/app/pkg/domain.Other, which belongs to datasource "reports"`,
			`CREATE TABLE "cross_tallies"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := inDiffProject(t)
			withArgs(t, "db:diff")
			// Any statement that names the other table fails, as it would where that table is
			// in another database.
			r := &recording{failExec: c.table}
			cmd := DiffCommand{
				domainContext: &stubDomainContext{domains: map[string]any{
					"example.com/app/pkg/domain.Owned": c.owned,
					"example.com/app/pkg/domain.Other": c.other,
				}},
				dbContext: recordedDiffContext(t, r),
			}

			output := printed(t, func() { require.NotPanics(t, func() { cmd.Execute(nil) }) })

			assert.NotContains(t, strings.Join(r.log, "\n"), c.table, "no statement may name it")
			drafted := draftedStatements(t, dir)
			assert.Contains(t, drafted, c.kept)
			assert.NotContains(t, drafted, c.table, "nor may the draft")
			assert.Contains(t, strings.Split(output, "\n"), c.skipped)
		})
	}
}

type crossOwnedAccount struct{ ID uint }

// crossOwnedEntry belongs to an account of the same datasource.
type crossOwnedEntry struct {
	ID                  uint
	CrossOwnedAccountID uint
	CrossOwnedAccount   crossOwnedAccount
}

// TestDiffStillDraftsConstraintsWithinTheDatasource: only a relation that leaves the
// datasource is skipped.
func TestDiffStillDraftsConstraintsWithinTheDatasource(t *testing.T) {
	dir := inDiffProject(t)
	withArgs(t, "db:diff")
	r := &recording{}
	cmd := DiffCommand{
		domainContext: &stubDomainContext{domains: map[string]any{
			"example.com/app/pkg/domain.Account": crossOwnedAccount{},
			"example.com/app/pkg/domain.Entry":   crossOwnedEntry{},
			"example.com/app/pkg/domain.Other":   crossLegacyClient{},
		}},
		dbContext: recordedDiffContext(t, r),
	}

	output := printed(t, func() { require.NotPanics(t, func() { cmd.Execute(nil) }) })

	assert.Contains(t, draftedStatements(t, dir),
		`ALTER TABLE "cross_owned_entries" ADD CONSTRAINT "fk_cross_owned_entries_cross_owned_account" `+
			`FOREIGN KEY ("cross_owned_account_id") REFERENCES "cross_owned_accounts"("id")`)
	assert.NotContains(t, output, "Skipping the constraint")
}

type stubDomainContext struct{ domains map[string]any }

func (s *stubDomainContext) RegisterDomain(key string, domain any) { s.domains[key] = domain }
func (s *stubDomainContext) GetDomains() map[string]any            { return s.domains }

// ------------------------------------------------------------------ domainsFor / DomainDatasourceOf

type undeclaredDomain struct{ ID uint }

type valueNamedDomain struct{ ID uint }

func (valueNamedDomain) DbConnectionName() string { return "reports" }

type pointerNamedDomain struct{ ID uint }

func (*pointerNamedDomain) DbConnectionName() string { return "reports" }

type valueScopedDomain struct{ ID uint }

func (valueScopedDomain) DataSourceName() string { return "legacy" }

type pointerScopedDomain struct{ ID uint }

func (*pointerScopedDomain) DataSourceName() string { return "legacy" }

// bothDeclaredDomain declares both: DbConnectionName, core's interface for an entity's
// connection, wins over the migrations' DataSourceName.
type bothDeclaredDomain struct{}

func (bothDeclaredDomain) DbConnectionName() string { return "reports" }
func (bothDeclaredDomain) DataSourceName() string   { return "legacy" }

// blankNamedDomain's DbConnectionName says nothing, so DataSourceName decides.
type blankNamedDomain struct{}

func (blankNamedDomain) DbConnectionName() string { return "  " }
func (blankNamedDomain) DataSourceName() string   { return "legacy" }

type unconfiguredDomain struct{}

func (unconfiguredDomain) DbConnectionName() string { return "reprots" }

// connectionField names its datasource from a field, through a value receiver.
type connectionField struct{ connection string }

func (c connectionField) DbConnectionName() string { return c.connection }

// embedsConnectionPointer promotes DbConnectionName through an embedded pointer, which is
// nil in its zero value: asking the zero value dereferences it.
type embedsConnectionPointer struct {
	*connectionField
	ID uint
}

// pointerFieldDomain names its datasource from a field, through a pointer receiver, so a
// registered value is asked through a pointer to a copy of it.
type pointerFieldDomain struct{ connection string }

func (p *pointerFieldDomain) DbConnectionName() string { return p.connection }

func TestDomainDatasourceOfHonoursDbConnectionNamerAndDatasourceScoped(t *testing.T) {
	cases := []struct {
		name  string
		model any
		want  string
	}{
		{"undeclared value", undeclaredDomain{}, "default"},
		{"undeclared pointer", &undeclaredDomain{}, "default"},
		{"nil", nil, "default"},

		{"value receiver, value", valueNamedDomain{}, "reports"},
		{"value receiver, pointer", &valueNamedDomain{}, "reports"},
		// A value-receiver method on a nil pointer would dereference it.
		{"value receiver, nil pointer", (*valueNamedDomain)(nil), "reports"},

		// A pointer-receiver method is not in a value's method set.
		{"pointer receiver, value", pointerNamedDomain{}, "reports"},
		{"pointer receiver, pointer", &pointerNamedDomain{}, "reports"},
		{"pointer receiver, nil pointer", (*pointerNamedDomain)(nil), "reports"},

		{"DatasourceScoped value receiver", valueScopedDomain{}, "legacy"},
		{"DatasourceScoped pointer receiver", pointerScopedDomain{}, "legacy"},
		{"DatasourceScoped pointer", &pointerScopedDomain{}, "legacy"},

		{"DbConnectionNamer wins", bothDeclaredDomain{}, "reports"},
		{"a blank name is none", blankNamedDomain{}, "legacy"},

		{"through an embedded pointer", embedsConnectionPointer{connectionField: &connectionField{"reports"}}, "reports"},
		// The registered value answers "", and so does its copy: the zero value, whose embedded
		// pointer is nil, is not asked.
		{"through an embedded pointer, blank", embedsConnectionPointer{connectionField: &connectionField{}}, "default"},
		{"pointer receiver reading the registered value", pointerFieldDomain{connection: "reports"}, "reports"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DomainDatasourceOf(c.model)
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestDomainDatasourceOfReportsAPanickingMethod: nothing asked a domain its datasource before
// db:diff did, so a method that panics when asked — here through a nil embedded pointer — is
// an error naming the method, which domainsFor names the domain in, rather than a crash that
// names neither.
func TestDomainDatasourceOfReportsAPanickingMethod(t *testing.T) {
	for _, model := range []any{embedsConnectionPointer{}, (*embedsConnectionPointer)(nil)} {
		var err error
		require.NotPanics(t, func() { _, err = DomainDatasourceOf(model) })
		assert.ErrorContains(t, err, "its DbConnectionName() panicked: runtime error: invalid memory address or nil pointer dereference")
	}

	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{"default": &fakeDataSource{}}}
	_, _, err := domainsFor(map[string]any{"app/pkg/domain.Party": embedsConnectionPointer{}}, "default", ctx)
	assert.EqualError(t, err, "domain app/pkg/domain.Party: its DbConnectionName() panicked: "+
		"runtime error: invalid memory address or nil pointer dereference")
}

func TestDomainsForSkipsOtherAndExternalDatasourcesAndSortsKeys(t *testing.T) {
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": &fakeDataSource{},
		"reports": &fakeDataSource{},
		"legacy":  &fakeDataSource{policy: externalSchema},
	}}
	first, second := &undeclaredDomain{ID: 1}, &undeclaredDomain{ID: 2}
	domains := map[string]any{
		"app/pkg/domain.Zeta":        second,
		"app/pkg/domain.Alpha":       first,
		"app/pkg/domain.Report":      valueNamedDomain{},
		"app/pkg/domain.LegacyOrder": valueScopedDomain{},
	}

	got, others, err := domainsFor(domains, "default", ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Same(t, first, got[0], "sorted by registration key")
	assert.Same(t, second, got[1])
	assert.Equal(t, map[reflect.Type]otherDomain{
		reflect.TypeOf(valueNamedDomain{}):  {key: "app/pkg/domain.Report", datasource: "reports"},
		reflect.TypeOf(valueScopedDomain{}): {key: "app/pkg/domain.LegacyOrder", datasource: "legacy"},
	}, others, "what migrateModelConstraints keeps out of the draft")

	got, others, err = domainsFor(domains, "reports", ctx)
	require.NoError(t, err)
	assert.Equal(t, []any{valueNamedDomain{}}, got)
	assert.Equal(t, otherDomain{key: "app/pkg/domain.Alpha", datasource: "default"},
		others[reflect.TypeOf(undeclaredDomain{})], "a pointer is keyed by the struct it points to")
}

// TestDomainsForDiffsUndeclaredDomainsAgainstTheSelectedDatasourceWithoutADefault: an app
// with no `default` kept its domains on named datasources, and db:diff --datasource=<name>
// diffed them all. A domain that declares nothing would belong to `default`, so without this
// every diff there failed until each domain declared a datasource. It is diffed against the
// selected datasource instead, as it always was, and the run says so.
func TestDomainsForDiffsUndeclaredDomainsAgainstTheSelectedDatasourceWithoutADefault(t *testing.T) {
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"main":    &fakeDataSource{},
		"reports": &fakeDataSource{},
	}}
	domains := map[string]any{
		"app/pkg/domain.Widget": undeclaredDomain{},
		"app/pkg/domain.Report": valueNamedDomain{},
	}

	var got []any
	output := printed(t, func() {
		var err error
		got, _, err = domainsFor(domains, "main", ctx)
		require.NoError(t, err)
	})

	assert.Equal(t, []any{undeclaredDomain{}}, got)
	assert.Equal(t, `Skipping domain app/pkg/domain.Report: it belongs to datasource "reports", not "main"`+"\n"+
		`No "default" datasource is configured, so the 1 domain(s) that declare no datasource are diffed `+
		`against "main"; give each a DbConnectionName() to diff it against its own datasource`+"\n", output)

	// A domain that names `default` still names a datasource that is not configured.
	_, _, err := domainsFor(map[string]any{"app/pkg/domain.Legacy": namedDefaultDomain{}}, "main", ctx)
	assert.EqualError(t, err, "domain app/pkg/domain.Legacy belongs to datasource \"default\", "+
		"which is not configured under the `databases` key")

	// With a `default`, an undeclared domain is default's, and says nothing about it.
	ctx.sources["default"] = &fakeDataSource{}
	output = printed(t, func() {
		var err error
		got, _, err = domainsFor(domains, "main", ctx)
		require.NoError(t, err)
	})
	assert.Empty(t, got)
	assert.NotContains(t, output, "declare no datasource")
}

type namedDefaultDomain struct{}

func (namedDefaultDomain) DbConnectionName() string { return "default" }

// TestDomainsForRejectsAnUnconfiguredDatasource: as with a migration, a domain naming a
// datasource nobody configured would never be diffed anywhere, and never say so.
func TestDomainsForRejectsAnUnconfiguredDatasource(t *testing.T) {
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{"default": &fakeDataSource{}}}

	_, _, err := domainsFor(map[string]any{
		"app/pkg/domain.Widget": undeclaredDomain{},
		"app/pkg/domain.Report": unconfiguredDomain{},
	}, "default", ctx)

	require.Error(t, err)
	assert.False(t, errors.Is(err, dbCore.ErrExternalSchema))
	assert.EqualError(t, err, "domain app/pkg/domain.Report belongs to datasource \"reprots\", "+
		"which is not configured under the `databases` key")
}

// ------------------------------------------------------------------------- output capture

// printed runs fn and returns what it wrote to os.Stdout, where db:diff reports what it
// leaves out.
func printed(t *testing.T, fn func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	var out bytes.Buffer
	copied := make(chan error, 1)
	go func() {
		_, err := io.Copy(&out, reader)
		copied <- err
	}()

	previous := os.Stdout
	os.Stdout = writer
	func() {
		defer func() { os.Stdout = previous }()
		fn()
	}()

	require.NoError(t, writer.Close())
	require.NoError(t, <-copied)
	require.NoError(t, reader.Close())
	return out.String()
}

// Installed once per test binary: log.SetLoggerFactory panics on a second call, and the
// command/db package had not claimed its slot. Same shape as provider/db_provider_test.go.

type commandLogCapture struct {
	mu      sync.Mutex
	entries []string
}

func (c *commandLogCapture) record(format string, v ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, fmt.Sprintf(format, v...))
}

// lines returns every line logged since captureCommandLog was last called.
func (c *commandLogCapture) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.entries...)
}

func (c *commandLogCapture) text() string { return strings.Join(c.lines(), "\n") }

type commandLogger struct{ c *commandLogCapture }

func (l commandLogger) SetPrefix(string)          {}
func (l commandLogger) Info(v ...any)             { l.c.record("%s", fmt.Sprint(v...)) }
func (l commandLogger) Infof(f string, v ...any)  { l.c.record(f, v...) }
func (l commandLogger) Warn(v ...any)             { l.c.record("%s", fmt.Sprint(v...)) }
func (l commandLogger) Warnf(f string, v ...any)  { l.c.record(f, v...) }
func (l commandLogger) Error(v ...any)            { l.c.record("%s", fmt.Sprint(v...)) }
func (l commandLogger) Errorf(f string, v ...any) { l.c.record(f, v...) }
func (l commandLogger) Panic(v ...any)            { panic(fmt.Sprint(v...)) }
func (l commandLogger) Panicf(f string, v ...any) { panic(fmt.Sprintf(f, v...)) }
func (l commandLogger) Engine() any               { return nil }

var (
	commandLog     commandLogCapture
	commandLogOnce sync.Once
)

// captureCommandLog starts a fresh capture of what the commands log.
func captureCommandLog(t *testing.T) *commandLogCapture {
	t.Helper()

	commandLogOnce.Do(func() {
		grglog.SetLoggerFactory(func(string) core.Logger { return commandLogger{c: &commandLog} })
	})

	commandLog.mu.Lock()
	commandLog.entries = nil
	commandLog.mu.Unlock()
	return &commandLog
}
