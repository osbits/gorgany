package provider

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	dbCmd "github.com/osbits/gorgany/v2/command/db"
	"github.com/osbits/gorgany/v2/db"
	"github.com/osbits/gorgany/v2/db/migration"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	grglog "github.com/osbits/gorgany/v2/log"
	"github.com/osbits/gorgany/v2/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDataSource stands in for a real connection. It carries the name it was
// configured under so a test can tell which connection it actually got, and the
// external_schema / read_only its config set, which it reports as a real engine does.
type stubDataSource struct {
	name   string
	policy dbCore.DataSourcePolicy
}

func (s *stubDataSource) Policy() dbCore.DataSourcePolicy { return s.policy }

func (s *stubDataSource) NewSession() (dbCore.ISession, error) {
	return &stubSession{ds: s}, nil
}
func (s *stubDataSource) GetDriver() (any, error) { return nil, nil }
func (s *stubDataSource) Close() error            { return nil }

type stubSession struct{ ds *stubDataSource }

func (s *stubSession) Executor() dbCore.IQueryExecutor { return nil }
func (s *stubSession) Query() dbCore.IQueryBuilder     { return nil }
func (s *stubSession) DataSource() dbCore.IDataSource  { return s.ds }
func (s *stubSession) Close() error                    { return nil }
func (s *stubSession) Transaction(_ context.Context, _ func(dbCore.IDBTransaction) error) error {
	return nil
}

// registerStubDriver installs a driver that yields stubDataSource values named
// after the database they were configured for. It copies the policy flags, because
// driver.New refuses a datasource that does not report a flag its config sets.
func registerStubDriver(t *testing.T, name string) {
	t.Helper()

	if _, already := driver.Lookup(name); already {
		return
	}
	driver.Register(name, func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return &stubDataSource{
			name:   cfg.Database,
			policy: dbCore.DataSourcePolicy{ExternalSchema: cfg.ExternalSchema, ReadOnly: cfg.ReadOnly},
		}, nil
	})
}

func withDatabasesConfig(t *testing.T, databases map[string]any) {
	t.Helper()

	previous := viper.Get("databases")
	viper.Set("databases", databases)
	t.Cleanup(func() { viper.Set("databases", previous) })
}

func stubDbConfig(dbName string) map[string]any {
	return map[string]any{
		"driver": "stub_gorm",
		"host":   "localhost",
		"port":   5432,
		"db":     dbName,
	}
}

// bootDbProvider runs one Register pass and returns the resulting DBContext.
func bootDbProvider(t *testing.T) (core.IDBContext, core.IContainer) {
	t.Helper()

	c := service.NewContainer()
	p := NewDbProvider()
	p.Register(c)

	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))
	return dbContext, c
}

// TestEveryConfiguredDatasourceIsRegistered is the T1.1 headline. The old
// constructor returned from inside its loop over the `databases` map, so
// configuring two databases registered exactly one — and because Go randomises
// map iteration order, which one changed on every boot. One boot proves nothing,
// so this asserts across ten consecutive boots.
func TestEveryConfiguredDatasourceIsRegistered(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withDatabasesConfig(t, map[string]any{
		"default": stubDbConfig("primary"),
		"creatio": stubDbConfig("creatio"),
		"reports": stubDbConfig("reports"),
	})

	for boot := 0; boot < 10; boot++ {
		t.Run(fmt.Sprintf("boot-%d", boot), func(t *testing.T) {
			dbContext, _ := bootDbProvider(t)

			for name, wantDb := range map[string]string{
				"default": "primary",
				"creatio": "creatio",
				"reports": "reports",
			} {
				ds := dbContext.GetDataSource(name)
				require.NotNilf(t, ds, "datasource %q must be registered", name)
				assert.Equal(t, wantDb, ds.(*stubDataSource).name,
					"datasource %q resolved to the wrong connection", name)
			}
		})
	}
}

// TestUnnamedTransientsResolveToDefault is the T1.2 headline. The three transient
// bindings were re-registered once per connection inside the loop and
// Container.bind overwrites, so a bare injected dbCore.ISession resolved to
// whichever connection happened to register last — nondeterministically.
func TestUnnamedTransientsResolveToDefault(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withDatabasesConfig(t, map[string]any{
		"default": stubDbConfig("primary"),
		"zulu":    stubDbConfig("zulu"),
		"alpha":   stubDbConfig("alpha"),
	})

	for boot := 0; boot < 10; boot++ {
		_, c := bootDbProvider(t)

		var session dbCore.ISession
		require.NoError(t, c.Make(&session))
		require.NotNil(t, session)

		assert.Equal(t, "primary", session.DataSource().(*stubDataSource).name,
			"boot %d: the unnamed ISession must always be the 'default' connection", boot)
	}
}

// TestNamedResolutionReachesNonDefaultConnections documents the supported way to
// reach a second database.
func TestNamedResolutionReachesNonDefaultConnections(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withDatabasesConfig(t, map[string]any{
		"default": stubDbConfig("primary"),
		"creatio": stubDbConfig("creatio"),
	})

	dbContext, _ := bootDbProvider(t)

	session, err := dbContext.GetDataSource("creatio").NewSession()
	require.NoError(t, err)
	assert.Equal(t, "creatio", session.DataSource().(*stubDataSource).name)
}

// TestNoDatabasesConfiguredSkipsTransientRegistration is the second half of T1.2.
// With no `databases` key the old built-in constructor returned ("", nil) and the
// three transient closures were registered anyway, capturing a nil connection —
// they panicked if ever resolved.
func TestNoDatabasesConfiguredSkipsTransientRegistration(t *testing.T) {
	withDatabasesConfig(t, map[string]any{})

	c := service.NewContainer()
	p := NewDbProvider()
	require.NotPanics(t, func() { p.Register(c) })

	var session dbCore.ISession
	err := c.Make(&session)

	// No binding at all is the correct outcome: an unresolvable dependency is a
	// clear error, whereas a binding over a nil connection panics later.
	require.Error(t, err)
	assert.Nil(t, session)
}

// TestNoDefaultConnectionSkipsTransientRegistration covers the same rule when
// databases exist but none is called "default".
func TestNoDefaultConnectionSkipsTransientRegistration(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withDatabasesConfig(t, map[string]any{
		"creatio": stubDbConfig("creatio"),
	})

	c := service.NewContainer()
	p := NewDbProvider()
	require.NotPanics(t, func() { p.Register(c) })

	// The named connection is still reachable.
	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))
	require.NotNil(t, dbContext.GetDataSource("creatio"))

	// The unnamed transient is not bound.
	var session dbCore.ISession
	require.Error(t, c.Make(&session))
}

// TestConfigIsReadAtRegisterNotAtConstruction pins the ordering trap.
//
// An app builds its bootstrapper — and therefore this provider — as the argument
// to app.NewServerApp(...), which is evaluated *before* ServerApp.Run() calls
// config.Parse("config/config"). A provider that read viper in its constructor
// would see an empty config and silently register no connections at all, which
// looks exactly like a misconfigured app.
func TestConfigIsReadAtRegisterNotAtConstruction(t *testing.T) {
	registerStubDriver(t, "stub_gorm")

	// Construct the provider with NO config present, mimicking
	// app.NewServerApp(NewBootstrapper()).
	withDatabasesConfig(t, map[string]any{})
	p := NewDbProvider()

	// Config arrives afterwards, as ServerApp.Run() does it.
	withDatabasesConfig(t, map[string]any{
		"default": stubDbConfig("primary"),
		"creatio": stubDbConfig("creatio"),
	})

	c := service.NewContainer()
	p.Register(c)

	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))

	require.NotNil(t, dbContext.GetDataSource("default"),
		"config parsed after construction must still be honoured")
	require.NotNil(t, dbContext.GetDataSource("creatio"))
	assert.Equal(t, "primary", dbContext.GetDataSource("default").(*stubDataSource).name)
}

// TestAddConnectionStillWorks pins the escape hatch apps use for connections the
// config loop cannot express.
func TestAddConnectionStillWorks(t *testing.T) {
	withDatabasesConfig(t, map[string]any{})

	c := service.NewContainer()
	p := NewDbProvider()
	p.AddConnection("default", func() dbCore.IDataSource {
		return &stubDataSource{name: "hand-built"}
	})
	p.Register(c)

	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))
	require.NotNil(t, dbContext.GetDataSource("default"))

	// And it feeds the unnamed transient, because it registered as "default".
	var session dbCore.ISession
	require.NoError(t, c.Make(&session))
	assert.Equal(t, "hand-built", session.DataSource().(*stubDataSource).name)
}

// TestAddConnectionCoexistsWithConfiguredDatabases proves the config loop and the
// programmatic escape hatch do not clobber each other.
func TestAddConnectionCoexistsWithConfiguredDatabases(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withDatabasesConfig(t, map[string]any{"default": stubDbConfig("primary")})

	c := service.NewContainer()
	p := NewDbProvider()
	p.AddConnection("legacy", func() dbCore.IDataSource {
		return &stubDataSource{name: "legacy"}
	})
	p.Register(c)

	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))
	assert.Equal(t, "primary", dbContext.GetDataSource("default").(*stubDataSource).name)
	assert.Equal(t, "legacy", dbContext.GetDataSource("legacy").(*stubDataSource).name)
}

func TestAddConnectionEPropagatesError(t *testing.T) {
	withDatabasesConfig(t, map[string]any{})

	c := service.NewContainer()
	p := NewDbProvider()
	p.AddConnectionE("default", func() (dbCore.IDataSource, error) {
		return nil, fmt.Errorf("cannot dial")
	})

	assert.PanicsWithError(t, "connection 'default': cannot dial", func() { p.Register(c) })
}

func TestAddConnectionRejectsNilDataSource(t *testing.T) {
	withDatabasesConfig(t, map[string]any{})

	c := service.NewContainer()
	p := NewDbProvider()
	p.AddConnection("default", func() dbCore.IDataSource { return nil })

	assert.Panics(t, func() { p.Register(c) })
}

// TestUnknownDriverIsFatalAndNamesAlternatives: an unknown driver used to fall
// through the hard-coded switch and register nothing at all.
func TestUnknownDriverIsFatalAndNamesAlternatives(t *testing.T) {
	withDatabasesConfig(t, map[string]any{
		"default": map[string]any{
			"driver": "oracle_gorm",
			"host":   "localhost",
			"db":     "d",
		},
	})

	c := service.NewContainer()
	p := NewDbProvider()

	assert.Panics(t, func() { p.Register(c) })
}

// TestMistypedDatabaseConfigIsAnErrorNamingTheDatabase ties T1.3 into the provider
// path: the error must say which database entry is wrong.
func TestMistypedDatabaseConfigIsAnErrorNamingTheDatabase(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withDatabasesConfig(t, map[string]any{
		"creatio": map[string]any{
			"driver": "stub_gorm",
			"host":   "localhost",
			"db":     "d",
			"log":    "definitely",
		},
	})

	c := service.NewContainer()
	p := NewDbProvider()

	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = r.(error)
			}
		}()
		p.Register(c)
		return nil
	}()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "database 'creatio'")
	assert.Contains(t, err.Error(), "'log'")
}

// TestNonMapDatabaseEntryIsAnError covers a scalar written where a map belongs.
func TestNonMapDatabaseEntryIsAnError(t *testing.T) {
	withDatabasesConfig(t, map[string]any{"default": "postgres://localhost/db"})

	c := service.NewContainer()
	p := NewDbProvider()

	assert.Panics(t, func() { p.Register(c) })
}

// ------------------------------------------------ the sessions table and the default's policy
//
// The sessions migrations and database session storage both live on `default`. They may only
// use a default gorgany owns and may write to: the migrations would create a table in someone
// else's schema, or have their DDL refused, and the storage would write on every login.

// stubDbConfigWith is stubDbConfig with extra keys, such as external_schema or read_only.
func stubDbConfigWith(dbName string, extra map[string]any) map[string]any {
	conf := stubDbConfig(dbName)
	for key, value := range extra {
		conf[key] = value
	}
	return conf
}

// bootAndListMigrations runs Register and Boot, and returns the migrations Boot attached.
// Boot publishes its DBContext to the db package's process-global, which is put back.
func bootAndListMigrations(t *testing.T) []core.IMigration {
	t.Helper()

	previous := db.GetDBContext()
	t.Cleanup(func() { db.SetDBContext(previous) })

	c := service.NewContainer()
	p := NewDbProvider()
	p.Register(c)
	p.Boot(c)

	var dataContext core.IDataContext
	require.NoError(t, c.Make(&dataContext))
	return dataContext.Migrations()
}

// registerPanic runs Register and returns the error it panicked with, or nil.
func registerPanic(t *testing.T) (refusal error) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			err, ok := r.(error)
			require.Truef(t, ok, "Register must panic with an error, got %T: %v", r, r)
			refusal = err
		}
	}()
	NewDbProvider().Register(service.NewContainer())
	return nil
}

var sessionsMigrations = []string{
	migration.NewSessionsMigration().Name(),
	migration.NewSessionsVersionMigration().Name(),
}

// TestSessionsMigrationsAreAttachedWhateverTheDefault: Boot adds both, wrapped with
// dbCmd.OnOwnedDefault, whatever `default` is and whether there is one, and says nothing
// about them. db:migrate decides when it runs whether they may run there, and says so then:
// an app may register its `default` on the DBContext from a provider that boots after this
// one, and an app with no database at all has nothing to be told at every boot.
func TestSessionsMigrationsAreAttachedWhateverTheDefault(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withSessionStorageConfig(t, "memory")
	wrapped := reflect.TypeOf(dbCmd.OnOwnedDefault(migration.NewSessionsMigration()))

	for name, databases := range map[string]map[string]any{
		"owned default": {
			"default": stubDbConfig("primary"),
			"reports": stubDbConfigWith("reports", map[string]any{"external_schema": true, "read_only": true}),
		},
		"external default":  {"default": stubDbConfigWith("legacy", map[string]any{"external_schema": true})},
		"read-only default": {"default": stubDbConfigWith("replica", map[string]any{"read_only": true})},
		"no default":        {"reports": stubDbConfig("reports")},
		"no databases":      {},
	} {
		t.Run(name, func(t *testing.T) {
			withDatabasesConfig(t, databases)
			logged := captureProviderLog(t)

			migrations := bootAndListMigrations(t)

			names := []string{}
			for _, m := range migrations {
				names = append(names, m.Name())
				assert.Equal(t, wrapped, reflect.TypeOf(m), "%s must be left to db:migrate to place", m.Name())
			}
			assert.Equal(t, sessionsMigrations, names,
				"both, and create_sessions_table first: the version column is added to the table it makes")
			assert.NotContains(t, logged.text(), "sessions migrations")
			assert.NotContains(t, logged.text(), sessionsMigrations[0])
		})
	}
}

func TestDatabaseSessionStorageRefusesAnExternalDefaultAtRegister(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withSessionStorageConfig(t, "database")
	withDatabasesConfig(t, map[string]any{
		"default": stubDbConfigWith("legacy", map[string]any{"external_schema": true}),
	})

	refusal := registerPanic(t)

	require.ErrorIs(t, refusal, dbCore.ErrExternalSchema)
	assert.Contains(t, refusal.Error(), `auth.session.storage: database keeps sessions in datasource "default"`)
	assert.Contains(t, refusal.Error(), "auth.session.storage: memory", "it has to say what to use instead")
	assert.Contains(t, refusal.Error(), `"More than one instance"`, "and where the trade-off is explained")
}

func TestDatabaseSessionStorageRefusesAReadOnlyDefaultAtRegister(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withSessionStorageConfig(t, "database")
	withDatabasesConfig(t, map[string]any{
		"default": stubDbConfigWith("replica", map[string]any{"read_only": true}),
	})

	refusal := registerPanic(t)

	require.ErrorIs(t, refusal, dbCore.ErrReadOnly)
	assert.Contains(t, refusal.Error(), "auth.session.storage: memory")
}

// TestDatabaseSessionStorageRefusesAProgrammaticDefault: AddConnection feeds the same map as
// config, so a hand-built default is checked like a configured one.
func TestDatabaseSessionStorageRefusesAProgrammaticDefault(t *testing.T) {
	withSessionStorageConfig(t, "database")
	withDatabasesConfig(t, map[string]any{})

	p := NewDbProvider()
	p.AddConnection("default", func() dbCore.IDataSource {
		return &stubDataSource{name: "hand-built", policy: dbCore.DataSourcePolicy{ExternalSchema: true}}
	})

	assert.PanicsWithError(t,
		`auth.session.storage: database keeps sessions in datasource "default", which refuses them: `+
			dbCore.ErrExternalSchema.Error()+`. Use auth.session.storage: memory, which is only correct `+
			`for a single instance, or point "default" at a database gorgany owns and configure this one `+
			`under another name; see gorgany's docs/DEPLOYMENT.md, "More than one instance"`,
		func() { p.Register(service.NewContainer()) })
}

// TestMemorySessionStorageAcceptsAnExternalDefault: nothing of gorgany's goes into the default
// then, so an external default is an ordinary datasource and the bare ISession still resolves.
func TestMemorySessionStorageAcceptsAnExternalDefault(t *testing.T) {
	registerStubDriver(t, "stub_gorm")
	withSessionStorageConfig(t, "memory")
	withDatabasesConfig(t, map[string]any{
		"default": stubDbConfigWith("legacy", map[string]any{"external_schema": true, "read_only": true}),
	})
	captureProviderLog(t)

	previous := db.GetDBContext()
	t.Cleanup(func() { db.SetDBContext(previous) })

	c := service.NewContainer()
	p := NewDbProvider()
	require.NotPanics(t, func() { p.Register(c) })
	require.NotPanics(t, func() { p.Boot(c) })

	var session dbCore.ISession
	require.NoError(t, c.Make(&session))
	assert.Equal(t, "legacy", session.DataSource().(*stubDataSource).name)
}

// ------------------------------------------------------------------- log capture
//
// Installed once per test binary: log.SetLoggerFactory panics on a second call, and the
// provider package had not claimed its slot. Same shape as auth/session_sweep_test.go.

type providerLogCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *providerLogCapture) record(format string, v ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, fmt.Sprintf(format, v...))
}

func (c *providerLogCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

type providerLogger struct{ c *providerLogCapture }

func (l providerLogger) SetPrefix(string)          {}
func (l providerLogger) Info(v ...any)             { l.c.record("%v", v) }
func (l providerLogger) Infof(f string, v ...any)  { l.c.record(f, v...) }
func (l providerLogger) Warn(v ...any)             { l.c.record("%v", v) }
func (l providerLogger) Warnf(f string, v ...any)  { l.c.record(f, v...) }
func (l providerLogger) Error(v ...any)            { l.c.record("%v", v) }
func (l providerLogger) Errorf(f string, v ...any) { l.c.record(f, v...) }
func (l providerLogger) Panic(v ...any)            { panic(fmt.Sprint(v...)) }
func (l providerLogger) Panicf(f string, v ...any) { panic(fmt.Sprintf(f, v...)) }
func (l providerLogger) Engine() any               { return nil }

var (
	providerLog     providerLogCapture
	providerLogOnce sync.Once
)

func captureProviderLog(t *testing.T) *providerLogCapture {
	t.Helper()

	providerLogOnce.Do(func() {
		grglog.SetLoggerFactory(func(string) core.Logger { return providerLogger{c: &providerLog} })
	})

	providerLog.mu.Lock()
	providerLog.lines = nil
	providerLog.mu.Unlock()
	return &providerLog
}
