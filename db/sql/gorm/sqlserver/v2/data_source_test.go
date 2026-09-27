package v2

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/db"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The datasource, constructed against 127.0.0.1:1, where nothing listens and a connection is
// refused at once. A constructor that dials therefore fails, fast; one that succeeds there
// opened nothing.

var (
	_ dbCore.IDataSource    = (*gormSQLServerDataSource)(nil)
	_ dbCore.PolicyReporter = (*gormSQLServerDataSource)(nil)
)

const unreachablePassword = "Gorgany-Test-1-unreachable"

// unreachable is a SQL login to a server nobody runs.
func unreachable() dsconfig.DataSource {
	return dsconfig.DataSource{
		Driver:   "sqlserver_gorm",
		Host:     "127.0.0.1",
		Port:     1,
		Database: "Example-db",
		Username: "sa",
		Password: unreachablePassword,
	}
}

// lazy is unreachable with lazy_connect, which the constructor survives only if it dials
// nothing.
func lazy() dsconfig.DataSource {
	cfg := unreachable()
	cfg.LazyConnect = true
	return cfg
}

// construct builds a datasource that must construct, and closes it when the test ends.
func construct(t *testing.T, cfg dsconfig.DataSource) *gormSQLServerDataSource {
	t.Helper()
	ds, err := NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })
	impl, ok := ds.(*gormSQLServerDataSource)
	require.True(t, ok)
	return impl
}

func gormOf(t *testing.T, ds dbCore.IDataSource) *gorm.DB {
	t.Helper()
	driver, err := ds.GetDriver()
	require.NoError(t, err)
	gdb, ok := driver.(*gorm.DB)
	require.True(t, ok)
	return gdb
}

// collectWarnings records what datasources warn about for the rest of t.
func collectWarnings(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	var said []string
	saved := warn
	warn = func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		said = append(said, msg)
	}
	t.Cleanup(func() { warn = saved })
	return &said
}

// countingAuthenticator is an Authenticator that counts its calls and its sources' token
// requests, and keeps the last request it was given.
type countingAuthenticator struct {
	mu      sync.Mutex
	calls   int
	tokens  int
	request AuthRequest
	source  TokenSource
	err     error
}

func (c *countingAuthenticator) authenticate(req AuthRequest) (TokenSource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.request = req
	if c.err != nil {
		return nil, c.err
	}
	return c, nil
}

func (c *countingAuthenticator) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	c.tokens++
	source := c.source
	c.mu.Unlock()
	if source != nil {
		return source.Token(ctx)
	}
	return "token", nil
}

// azureCLI registers c as the azure_cli method, as the Entra ID package would, and returns a
// config that signs in with it to the unreachable server.
func azureCLI(t *testing.T, c *countingAuthenticator) dsconfig.DataSource {
	t.Helper()
	resetAuthenticators(t)
	RegisterAuthenticator(AuthMethodAzureCLI, c.authenticate)

	cfg := unreachable()
	cfg.Username, cfg.Password = "", ""
	cfg.Auth = dsconfig.Auth{Method: AuthMethodAzureCLI}
	return cfg
}

// TestNewDataSourceReturnsErrorsNotPanics: a config that cannot work is an error at the
// constructor, never a panic, and never an error that repeats the password.
func TestNewDataSourceReturnsErrorsNotPanics(t *testing.T) {
	for name, raw := range map[string]map[string]any{
		"nil":             nil,
		"no host":         {"driver": "sqlserver_gorm", "db": "Example-db"},
		"a bad port":      {"driver": "sqlserver_gorm", "host": "127.0.0.1", "db": "Example-db", "port": "abc"},
		"an unknown key":  {"driver": "sqlserver_gorm", "host": "127.0.0.1", "db": "Example-db", "encrpyt": true},
		"no credentials":  {"driver": "sqlserver_gorm", "host": "127.0.0.1", "db": "Example-db"},
		"a host and port": {"driver": "sqlserver_gorm", "host": "127.0.0.1,1433", "db": "Example-db", "username": "sa", "password": unreachablePassword},
		"a bad option": {"driver": "sqlserver_gorm", "host": "127.0.0.1", "db": "Example-db", "username": "sa",
			"password": unreachablePassword, "options": map[string]any{"dial_timeout": "soon"}},
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { _, err = NewDataSource(raw) })
			require.Error(t, err)
			assert.NotContains(t, err.Error(), unreachablePassword)
		})
	}
}

func TestLazyConnectDoesNotDial(t *testing.T) {
	ds := construct(t, lazy())

	assert.NotNil(t, ds.sqlDB)
	assert.Zero(t, ds.sqlDB.Stats().OpenConnections)
	assert.Equal(t, 10, ds.sqlDB.Stats().MaxOpenConnections, "the pool defaults apply")
}

// TestEagerConnectFailsWithDescribeOnly: the error names the server and the method, and not
// the DSN, which carries the password.
func TestEagerConnectFailsWithDescribeOnly(t *testing.T) {
	start := time.Now()
	_, err := NewDataSourceWithConfig(unreachable())
	require.Error(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "a refused connection is not retried")

	assert.Contains(t, err.Error(), "sqlserver: cannot connect to 127.0.0.1:1/Example-db (auth: sql)")
	assert.NotContains(t, err.Error(), unreachablePassword)
	assert.NotContains(t, err.Error(), "sqlserver://")
}

// TestAFakeAuthenticatorIsCalledOnceAndWarmedUp: the authenticator builds one source for the
// datasource, and the constructor asks it for the first token before it connects, so a
// browser sign-in happens at boot. What it is told comes from the config.
func TestAFakeAuthenticatorIsCalledOnceAndWarmedUp(t *testing.T) {
	c := &countingAuthenticator{}
	_, err := NewDataSourceWithConfig(azureCLI(t, c))
	require.Error(t, err, "the unreachable server still fails the connection")
	assert.Contains(t, err.Error(), "(auth: azure_cli)")

	assert.Equal(t, 1, c.calls)
	assert.Equal(t, 2, c.tokens, "the warm-up asked, and the connection asked again before its dial was refused, "+
		"of a source that caches nothing")
	assert.Equal(t, AuthMethodAzureCLI, c.request.Method)
	assert.Equal(t, "127.0.0.1", c.request.Host)
	assert.Equal(t, "Example-db", c.request.Database)
	assert.Equal(t, "https://database.windows.net/.default", c.request.Scope)
	assert.Equal(t, dsconfig.AzureCloudPublic, c.request.Cloud)
	assert.Equal(t, DefaultLoginTimeout, c.request.LoginTimeout)
	require.NotNil(t, c.request.Context)
	assert.Error(t, c.request.Context.Err(), "a datasource that failed to construct has closed its lifetime")
}

func TestLazyConnectDoesNotWarmUp(t *testing.T) {
	c := &countingAuthenticator{}
	cfg := azureCLI(t, c)
	cfg.LazyConnect = true
	cfg.Auth.LoginTimeout = 45 * time.Second

	construct(t, cfg)
	assert.Equal(t, 1, c.calls)
	assert.Zero(t, c.tokens)
	assert.Equal(t, 45*time.Second, c.request.LoginTimeout)
}

// failingTokens fails every request.
type failingTokens struct{ err error }

func (f failingTokens) Token(context.Context) (string, error) { return "", f.err }

func TestWarmUpFailureNamesTheMethod(t *testing.T) {
	c := &countingAuthenticator{source: failingTokens{errors.New("no browser to open")}}
	_, err := NewDataSourceWithConfig(azureCLI(t, c))
	require.Error(t, err)
	assert.Equal(t, "sqlserver: cannot sign in to 127.0.0.1:1/Example-db (auth: azure_cli): no browser to open", err.Error())
}

func TestTheAuthenticatorErrorIsReported(t *testing.T) {
	c := &countingAuthenticator{err: errors.New("tenant not found")}
	_, err := NewDataSourceWithConfig(azureCLI(t, c))
	require.Error(t, err)
	assert.Equal(t, "sqlserver: auth.method azure_cli for 127.0.0.1:1/Example-db: tenant not found", err.Error())

	resetAuthenticators(t)
	RegisterAuthenticator(AuthMethodAzureCLI, func(AuthRequest) (TokenSource, error) { return nil, nil })
	cfg := unreachable()
	cfg.Username, cfg.Password = "", ""
	cfg.Auth = dsconfig.Auth{Method: AuthMethodAzureCLI}
	_, err = NewDataSourceWithConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no token source")
}

// TestCloseAbandonsATokenRequestInFlight: a lazy interactive sign-in waits on a person, under
// a request whose context is often context.Background(). Close must not wait for it, and must
// end it, so the query that started it fails instead of hanging.
func TestCloseAbandonsATokenRequestInFlight(t *testing.T) {
	source := newBlockingTokens()
	c := &countingAuthenticator{source: source}
	cfg := azureCLI(t, c)
	cfg.LazyConnect = true
	ds := construct(t, cfg)

	requested := make(chan error, 1)
	go func() {
		// What a new connection asks before it dials.
		_, err := ds.tokens.token(context.Background())
		requested <- err
	}()
	<-source.entered

	closed := make(chan error, 1)
	go func() { closed <- ds.Close() }()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on a token request")
	}

	select {
	case err := <-requested:
		require.ErrorIs(t, err, errClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("the token request outlived Close")
	}
	assert.Error(t, c.request.Context.Err(), "the authenticator's lifetime ended with the datasource")
}

func TestCloseIsIdempotent(t *testing.T) {
	ds, err := NewDataSourceWithConfig(lazy())
	require.NoError(t, err)

	require.NoError(t, ds.Close())
	require.NoError(t, ds.Close())
}

// TestDBContextCloseOverASQLServerDataSource: graceful shutdown closes every registered
// datasource, and this one is no exception.
func TestDBContextCloseOverASQLServerDataSource(t *testing.T) {
	ds, err := NewDataSourceWithConfig(lazy())
	require.NoError(t, err)

	var dbContext db.DBContext
	dbContext.Init()
	dbContext.RegisterDataSource("legacy", ds)
	require.NoError(t, dbContext.Close())
	require.NoError(t, ds.Close(), "closing again after shutdown is harmless")
}

func TestDialectCarriesReadOnly(t *testing.T) {
	rw := construct(t, lazy())
	assert.Equal(t, &SQLServerDialect{}, rw.Dialect())

	cfg := lazy()
	cfg.ReadOnly = true
	ro := construct(t, cfg)
	assert.Equal(t, &SQLServerDialect{ReadOnly: true}, ro.Dialect())

	session, err := ro.NewSession()
	require.NoError(t, err)
	_, _, err = session.Query().Insert("t").Columns("a").Values(1).ToSQL()
	require.ErrorIs(t, err, dbCore.ErrReadOnly)
}

func TestPolicyReportsTheFlags(t *testing.T) {
	for _, flags := range []dbCore.DataSourcePolicy{{}, {ReadOnly: true}, {ExternalSchema: true}, {ExternalSchema: true, ReadOnly: true}} {
		cfg := lazy()
		cfg.ReadOnly = flags.ReadOnly
		cfg.ExternalSchema = flags.ExternalSchema
		assert.Equal(t, flags, dbCore.PolicyOf(construct(t, cfg)))
	}
}

// TestReadOnlyInstallsTheGuard: what an app sends to GetDriver's handle is guarded as well as
// what the builders render.
func TestReadOnlyInstallsTheGuard(t *testing.T) {
	cfg := lazy()
	cfg.ReadOnly = true
	gdb := gormOf(t, construct(t, cfg))

	require.ErrorIs(t, gdb.Exec("DELETE FROM [dbo].[2024Orders]").Error, dbCore.ErrReadOnly)
	require.ErrorIs(t, gdb.Create(&migratedOrder{Status: "new"}).Error, dbCore.ErrReadOnly)

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Contains(t, dsn, "applicationintent=ReadOnly")
}

func TestExternalSchemaInstallsTheDDLGuard(t *testing.T) {
	cfg := lazy()
	cfg.ExternalSchema = true
	gdb := gormOf(t, construct(t, cfg))

	require.ErrorIs(t, gdb.Exec("DROP TABLE [dbo].[2024Orders]").Error, dbCore.ErrExternalSchema)
	require.ErrorIs(t, gdb.Exec("EXEC sp_rename 'dbo.2024Orders', 'Orders'").Error, dbCore.ErrExternalSchema)
}

func TestParamLimitCallbackIsInstalled(t *testing.T) {
	gdb := gormOf(t, construct(t, lazy()))

	err := gdb.Exec("DELETE FROM [t] WHERE [Id] IN (?)", ids(MaxBindParameters+1)).Error
	requireUnsupported(t, err, fmt.Sprintf("a statement with %d bound parameters", MaxBindParameters+1))
}

// TestLogWarnsAndTurnsOnDebug: statement logging includes the bound values, which on a
// customer's database are customer data.
func TestLogWarnsAndTurnsOnDebug(t *testing.T) {
	said := collectWarnings(t)
	cfg := lazy()
	cfg.Log = true
	ds := construct(t, cfg)

	require.Len(t, *said, 1)
	assert.Contains(t, (*said)[0], "log is on for 127.0.0.1:1/Example-db")
	assert.NotContains(t, (*said)[0], unreachablePassword)
	assert.NotEqual(t, gormOf(t, ds).Config.Logger, construct(t, lazy()).db.Config.Logger, "Debug replaces the silent logger")
}

// TestAnIgnoredInstanceIsWarnedAboutOnce: a DataGrip config for Azure SQL carries an instance
// that must be dropped; the boot says so, once.
func TestAnIgnoredInstanceIsWarnedAboutOnce(t *testing.T) {
	said := collectWarnings(t)
	cfg := lazy()
	cfg.Host = azureHost
	cfg.Port = 1433
	cfg.Instance = "legacy"
	construct(t, cfg)

	require.Len(t, *said, 1)
	assert.Contains(t, (*said)[0], `instance "legacy" is ignored for `+azureHost)
}

func TestSessionsUseTheGuardedConnection(t *testing.T) {
	cfg := lazy()
	cfg.ExternalSchema = true
	ds := construct(t, cfg)

	session, err := ds.NewSession()
	require.NoError(t, err)
	res := session.Executor().ExecRaw(ctx, "ALTER TABLE [dbo].[2024Orders] ADD [Note] nvarchar(10) NULL")
	require.ErrorIs(t, res.Error, dbCore.ErrExternalSchema)

	err = session.Transaction(ctx, func(tx dbCore.IDBTransaction) error { return nil })
	require.Error(t, err, "BEGIN dials the unreachable server")
	assert.NotContains(t, err.Error(), unreachablePassword)
}
