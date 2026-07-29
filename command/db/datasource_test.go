package db

import (
	"os"
	"testing"

	"github.com/osbits/gorgany/app/core"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ------------------------------------------------------------------- test doubles

type fakeDataSource struct {
	driver any
	err    error
}

func (f *fakeDataSource) NewSession() (dbCore.ISession, error) { return nil, nil }
func (f *fakeDataSource) GetDriver() (any, error)              { return f.driver, f.err }
func (f *fakeDataSource) Close() error                         { return nil }

type fakeDBContext struct {
	sources map[string]dbCore.IDataSource
}

func (f *fakeDBContext) RegisterDataSource(name string, ds dbCore.IDataSource) {
	if f.sources == nil {
		f.sources = map[string]dbCore.IDataSource{}
	}
	f.sources[name] = ds
}

func (f *fakeDBContext) GetDataSource(name string) dbCore.IDataSource {
	return f.sources[name]
}

// scopedMigration declares which datasource it targets.
type scopedMigration struct {
	name   string
	target string
}

func (m scopedMigration) Name() string              { return m.name }
func (m scopedMigration) DataSourceName() string    { return m.target }
func (m scopedMigration) Up() core.MigrationClosure { return func(*gorm.DB) error { return nil } }
func (m scopedMigration) Down() core.MigrationClosure {
	return func(*gorm.DB) error { return nil }
}

// plainMigration declares nothing.
type plainMigration struct{ name string }

func (m plainMigration) Name() string                { return m.name }
func (m plainMigration) Up() core.MigrationClosure   { return func(*gorm.DB) error { return nil } }
func (m plainMigration) Down() core.MigrationClosure { return func(*gorm.DB) error { return nil } }

func withArgs(t *testing.T, args ...string) {
	t.Helper()
	previous := os.Args
	os.Args = append([]string{"cli"}, args...)
	t.Cleanup(func() { os.Args = previous })
}

// ------------------------------------------------------- flag parsing (T1.6)

// TestSelectedDatasourceDefaultsToDefault keeps the pre-v2 behaviour for anyone
// who passes no flag.
func TestSelectedDatasourceDefaultsToDefault(t *testing.T) {
	withArgs(t, "db:migrate", "up")
	assert.Equal(t, core.DefaultKeyInRegistrar, SelectedDatasource())
}

// TestSelectedDatasourceIsFoundAfterAPositionalArgument is the reason this does not
// go through command.Resolver's flag mechanism: that hands os.Args[2:] to
// flag.FlagSet.Parse, and Go's flag package stops at the first non-flag argument —
// which for `db:migrate up --datasource=x` is the positional `up`, so the flag
// would never be seen at all.
func TestSelectedDatasourceIsFoundAfterAPositionalArgument(t *testing.T) {
	tests := [][]string{
		{"db:migrate", "up", "--datasource=creatio"},
		{"db:migrate", "up", "-datasource=creatio"},
		{"db:migrate", "up", "--datasource", "creatio"},
		{"db:migrate", "up", "-datasource", "creatio"},
		{"db:migrate", "--datasource=creatio", "up"},
		{"db:seed", "--datasource=creatio"},
	}

	for _, args := range tests {
		t.Run(args[len(args)-1], func(t *testing.T) {
			withArgs(t, args...)
			assert.Equal(t, "creatio", SelectedDatasource())
		})
	}
}

func TestSelectedSteps(t *testing.T) {
	withArgs(t, "db:migrate", "down")
	steps, err := SelectedSteps()
	require.NoError(t, err)
	assert.Equal(t, 1, steps, "steps defaults to 1")

	withArgs(t, "db:migrate", "down", "--steps=3")
	steps, err = SelectedSteps()
	require.NoError(t, err)
	assert.Equal(t, 3, steps)

	withArgs(t, "db:migrate", "down", "--steps", "2")
	steps, err = SelectedSteps()
	require.NoError(t, err)
	assert.Equal(t, 2, steps)
}

func TestSelectedStepsRejectsBadValues(t *testing.T) {
	for _, value := range []string{"nope", "0", "-1"} {
		withArgs(t, "db:migrate", "down", "--steps="+value)
		_, err := SelectedSteps()
		require.Errorf(t, err, "steps=%q must be rejected", value)
	}
}

// ------------------------------------------------------- datasource resolution

func TestResolveGormReturnsTheNamedDatasource(t *testing.T) {
	primary := &gorm.DB{}
	secondary := &gorm.DB{}
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": &fakeDataSource{driver: primary},
		"creatio": &fakeDataSource{driver: secondary},
	}}

	got, err := ResolveGorm(ctx, "creatio")
	require.NoError(t, err)
	assert.Same(t, secondary, got, "must resolve the named datasource, not the default")

	got, err = ResolveGorm(ctx, "default")
	require.NoError(t, err)
	assert.Same(t, primary, got)
}

// TestResolveGormReportsAnUnknownName: the commands used to call
// GetDataSource(DefaultKeyInRegistrar) unconditionally and would nil-dereference
// when `default` was not configured.
func TestResolveGormReportsAnUnknownName(t *testing.T) {
	ctx := &fakeDBContext{}

	_, err := ResolveGorm(ctx, "creatio")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `datasource "creatio" is not configured`)
	assert.Contains(t, err.Error(), "--datasource")
}

func TestResolveGormReportsANonGormDriver(t *testing.T) {
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": &fakeDataSource{driver: "not a gorm handle"},
	}}

	_, err := ResolveGorm(ctx, "default")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not GORM-backed")
}

func TestResolveGormPropagatesDriverError(t *testing.T) {
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": &fakeDataSource{err: assert.AnError},
	}}

	_, err := ResolveGorm(ctx, "default")
	require.ErrorIs(t, err, assert.AnError)
}

func TestResolveGormRejectsNilContext(t *testing.T) {
	_, err := ResolveGorm(nil, "default")
	require.Error(t, err)
}

// ------------------------------------------------- migration target declaration

// TestUndeclaredMigrationsTargetDefault is what keeps `--datasource=creatio` from
// sweeping every existing migration onto the wrong engine: a migration that
// declares nothing is treated as belonging to `default`.
func TestUndeclaredMigrationsTargetDefault(t *testing.T) {
	assert.Equal(t, core.DefaultKeyInRegistrar, TargetDatasourceOf(plainMigration{name: "m1"}))
	assert.Equal(t, core.DefaultKeyInRegistrar,
		TargetDatasourceOf(scopedMigration{name: "m2", target: "   "}),
		"a blank declaration is the same as none")
}

func TestDeclaredMigrationsTargetWhatTheySay(t *testing.T) {
	assert.Equal(t, "creatio", TargetDatasourceOf(scopedMigration{name: "m", target: "creatio"}))
}

// TestPartitionByDatasourceRoutesMigrations is the T1.6 headline: a migration
// written for the second database used to execute against the first — a Postgres
// DDL statement fired at a MySQL connection with no warning.
func TestPartitionByDatasourceRoutesMigrations(t *testing.T) {
	migrations := []core.IMigration{
		plainMigration{name: "legacy_untagged"},
		scopedMigration{name: "for_default", target: "default"},
		scopedMigration{name: "for_creatio", target: "creatio"},
	}
	configured := func(name string) bool { return name == "default" || name == "creatio" }
	describe := func(m core.IMigration) string { return "migration " + m.Name() }

	t.Run("default run", func(t *testing.T) {
		matching, skipped, err := PartitionByDatasource(migrations, "default", configured, describe)
		require.NoError(t, err)
		assert.Equal(t, []string{"legacy_untagged", "for_default"}, names(matching))
		assert.Equal(t, []string{"for_creatio"}, names(skipped))
	})

	t.Run("creatio run touches only its own migrations", func(t *testing.T) {
		matching, skipped, err := PartitionByDatasource(migrations, "creatio", configured, describe)
		require.NoError(t, err)
		assert.Equal(t, []string{"for_creatio"}, names(matching))
		assert.Equal(t, []string{"legacy_untagged", "for_default"}, names(skipped))
	})
}

// TestPartitionByDatasourceRefusesAnUnconfiguredTarget is the "fails loudly"
// half: a migration naming a datasource nobody registered would otherwise never
// run and never say so.
func TestPartitionByDatasourceRefusesAnUnconfiguredTarget(t *testing.T) {
	migrations := []core.IMigration{
		scopedMigration{name: "for_typo", target: "creatioo"},
	}
	configured := func(name string) bool { return name == "default" || name == "creatio" }

	_, _, err := PartitionByDatasource(migrations, "default", configured,
		func(m core.IMigration) string { return "migration " + m.Name() })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "for_typo")
	assert.Contains(t, err.Error(), `"creatioo"`)
	assert.Contains(t, err.Error(), "not configured")
}

func TestIsConfigured(t *testing.T) {
	ctx := &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": &fakeDataSource{},
	}}

	check := IsConfigured(ctx)
	assert.True(t, check("default"))
	assert.False(t, check("creatio"))

	assert.False(t, IsConfigured(nil)("default"))
}

// TestMigrateCommandName pins the command name apps invoke.
func TestMigrateCommandName(t *testing.T) {
	assert.Equal(t, "db:migrate", MigrateCommand{}.GetName())
	assert.Equal(t, "db:seed", SeedCommand{}.GetName())
	assert.Equal(t, "db:diff", DiffCommand{}.GetName())
}

// TestDownIsNoLongerAStub is the T1.7 regression. `down` used to be
// `func (thiz MigrateCommand) down() {}` — it reported success and did nothing.
// It now takes a context and reaches the datasource, so an unconfigured one is
// reported instead of being silently ignored.
func TestDownIsNoLongerAStub(t *testing.T) {
	withArgs(t, "db:migrate", "down")

	cmd := MigrateCommand{
		dataContext: &stubDataContext{},
		dbContext:   &fakeDBContext{}, // nothing configured
	}

	assert.PanicsWithError(t,
		`datasource "default" is not configured; check the `+"`databases`"+
			` config key and the --datasource flag`,
		func() { cmd.down(nil) },
		"down must report a missing datasource, not silently succeed")
}

// TestDownRejectsABadStepsFlagBeforeTouchingTheDatabase
func TestDownRejectsABadStepsFlagBeforeTouchingTheDatabase(t *testing.T) {
	withArgs(t, "db:migrate", "down", "--steps=0")

	cmd := MigrateCommand{dataContext: &stubDataContext{}, dbContext: &fakeDBContext{}}
	assert.Panics(t, func() { cmd.down(nil) })
}

// TestExecuteRejectsAnUnknownSubcommandAndSaysHow
func TestExecuteRejectsAnUnknownSubcommandAndSaysHow(t *testing.T) {
	withArgs(t, "db:migrate", "sideways")

	cmd := MigrateCommand{dataContext: &stubDataContext{}, dbContext: &fakeDBContext{}}

	defer func() {
		r := recover()
		require.NotNil(t, r)
		assert.Contains(t, r.(string), "--datasource")
		assert.Contains(t, r.(string), "--steps")
	}()
	cmd.Execute(nil)
}

func TestExecuteRequiresASubcommand(t *testing.T) {
	withArgs(t, "db:migrate")

	cmd := MigrateCommand{dataContext: &stubDataContext{}, dbContext: &fakeDBContext{}}

	defer func() {
		r := recover()
		require.NotNil(t, r)
		assert.Contains(t, r.(string), "db:migrate up")
	}()
	cmd.Execute(nil)
}

type stubDataContext struct {
	migrations []core.IMigration
	seeders    []core.ISeeder
}

func (s *stubDataContext) Migrations() []core.IMigration  { return s.migrations }
func (s *stubDataContext) AddMigration(m core.IMigration) { s.migrations = append(s.migrations, m) }
func (s *stubDataContext) Seeders() []core.ISeeder        { return s.seeders }
func (s *stubDataContext) AddSeeder(seeder core.ISeeder)  { s.seeders = append(s.seeders, seeder) }

func names(migrations []core.IMigration) []string {
	out := make([]string, 0, len(migrations))
	for _, m := range migrations {
		out = append(out, m.Name())
	}
	return out
}
