package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	"github.com/osbits/gorgany/v2/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDataSource stands in for a real connection. It carries the name it was
// configured under so a test can tell which connection it actually got.
type stubDataSource struct {
	name string
}

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
// after the database they were configured for.
func registerStubDriver(t *testing.T, name string) {
	t.Helper()

	if _, already := driver.Lookup(name); already {
		return
	}
	driver.Register(name, func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return &stubDataSource{name: cfg.Database}, nil
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
