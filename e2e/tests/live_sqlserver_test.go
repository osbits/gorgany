//go:build livedb

// The SQL Server cases of the live suite. They are behind the same `livedb` tag as
// live_db_test.go, and must stay behind it: the CI live-db job lists every case that compiles
// without the tag as a fixture-app case and skips it, so an untagged file here would never run
// there.
//
// Start the engine they default to:
//
//	docker run -d --rm --platform linux/amd64 --name gorgany-mssql \
//	  -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD=Gorgany-Test-1 -e MSSQL_PID=Developer \
//	  -p 14330:1433 mcr.microsoft.com/mssql/server:2022-CU27-ubuntu-22.04
//
// The image is amd64-only; on an Apple Silicon machine it runs under Rosetta once Docker
// Desktop's "Use Rosetta for x86_64/amd64 emulation" is on. Then:
//
//	E2E_REQUIRE_SQLSERVER=1 go test -tags=livedb ./e2e/tests/ -run 'SQLServer' -count=1 -v
//
// Each case passes through gateSQLServer, which skips it when SQL Server is not reachable and
// fails it under E2E_REQUIRE_SQLSERVER=1 (see requireSQLServerEnvVar). The cases create the
// gorgany_test database through master themselves, since the image cannot create one from its
// environment the way the Postgres and MySQL images do.
package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/osbits/gorgany/v2/app/core"
	dbCmd "github.com/osbits/gorgany/v2/command/db"
	"github.com/osbits/gorgany/v2/db/orm"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"
	pgv2 "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	sqlserverv2 "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/osbits/gorgany/v2/provider"
	"github.com/osbits/gorgany/v2/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	// mssqlTestDatabase is the database every SQL Server case runs in unless it says otherwise.
	mssqlTestDatabase = "gorgany_test"

	// mssqlPassword is the sa password the container is started with. SQL Server refuses a
	// password below its complexity policy, so it cannot be the other engines' "test".
	mssqlPassword = "Gorgany-Test-1"

	// mssqlProbeDatabase stands in for a database named the way EF Core projects name theirs,
	// which is only valid bracketed.
	mssqlProbeDatabase = "Gorgany-EF-Probe"
)

// mssqlConfig is the datasource config the SQL Server cases connect with.
//
// It refuses an Azure SQL host by panicking, as liveEnginePort does for a malformed port. The
// cases create and drop databases and tables, so a host variable exported from a shell that
// also talks to a cloud database must stop the run before it dials, not after.
//
// trust_server_certificate is on because the container's certificate is self-signed. The
// connection is still encrypted, which is the engine's default; only the certificate chain is
// not checked.
func mssqlConfig() map[string]any {
	host := liveEngineHost("E2E_MSSQL_HOST", "127.0.0.1")
	if dsconfig.IsAzureSQLHost(host) {
		panic(fmt.Sprintf("E2E_MSSQL_HOST is %s, an Azure SQL host: the SQL Server cases create and "+
			"drop databases and tables, so they run only against a disposable local server", host))
	}
	return map[string]any{
		"driver":   "sqlserver_gorm",
		"host":     host,
		"port":     liveEnginePort("E2E_MSSQL_PORT", 14330),
		"username": "sa",
		"password": mssqlPassword,
		"db":       mssqlTestDatabase,
		"options":  map[string]any{"trust_server_certificate": "true"},
	}
}

// mssqlConfigFor is mssqlConfig pointed at another database on the same server.
func mssqlConfigFor(database string) map[string]any {
	cfg := mssqlConfig()
	cfg["db"] = database
	return cfg
}

// mssqlMasterConfig is the connection databases are created and dropped through.
func mssqlMasterConfig() map[string]any {
	return mssqlConfigFor("master")
}

// mssqlAddress renders the SQL Server endpoint as the datasource names it in its errors.
func mssqlAddress() string {
	cfg := mssqlConfig()
	return fmt.Sprintf("%v:%v", cfg["host"], cfg["port"])
}

// mssqlDatabases remembers which databases this process has already made sure of, so each
// costs one round trip through master per run rather than one per case. A failed attempt is
// not remembered: the gate retries while the server is still starting, and a sync.Once would
// have spent itself on the first refusal.
var mssqlDatabases = struct {
	sync.Mutex
	ensured map[string]bool
}{ensured: map[string]bool{}}

// ensureMSSQLDatabase creates the database through master if it does not exist.
//
// A database it creates gets READ_COMMITTED_SNAPSHOT ON, the setting every Azure SQL database
// has and an on-premises one does not by default. The upsert cases then run where readers see
// a snapshot, which is where a NOT EXISTS without locking hints would miss a concurrent insert;
// the dialect's UPDLOCK, HOLDLOCK hints are what they prove.
func ensureMSSQLDatabase(name string) (err error) {
	mssqlDatabases.Lock()
	defer mssqlDatabases.Unlock()
	if mssqlDatabases.ensured[name] {
		return nil
	}

	master, err := sqlserverv2.NewDataSource(mssqlMasterConfig())
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, master.Close()) }()

	driver, err := master.GetDriver()
	if err != nil {
		return err
	}
	literal, bracketed := mssqlDatabaseNames(name)
	if err := driver.(*gorm.DB).Exec("IF DB_ID(N'" + literal + "') IS NULL BEGIN " +
		"CREATE DATABASE " + bracketed + "; " +
		"ALTER DATABASE " + bracketed + " SET READ_COMMITTED_SNAPSHOT ON; END").Error; err != nil {
		return fmt.Errorf("creating database %s: %w", name, err)
	}
	mssqlDatabases.ensured[name] = true
	return nil
}

// mssqlDatabaseNames returns name as the contents of an N'…' literal and as a bracketed
// identifier.
func mssqlDatabaseNames(name string) (literal, bracketed string) {
	return strings.ReplaceAll(name, "'", "''"), "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

// openMSSQL makes sure cfg's database exists, then opens a datasource on it. It is what a gate
// retries, so an unreachable server is an error, never a failure.
func openMSSQL(cfg map[string]any) (dbCore.IDataSource, error) {
	if err := ensureMSSQLDatabase(cfg["db"].(string)); err != nil {
		return nil, err
	}
	return sqlserverv2.NewDataSource(cfg)
}

// mssqlDataSource admits the case through gateSQLServer on a datasource opened with cfg, and
// closes the datasource when the case ends.
//
// The Close is registered here, before the case registers anything that uses the connection,
// so it runs after every cleanup that does (see waitForDatasource for why that order matters).
func mssqlDataSource(t *testing.T, cfg map[string]any) dbCore.IDataSource {
	t.Helper()

	ds := gateSQLServer(t, engineWait, func() (dbCore.IDataSource, error) { return openMSSQL(cfg) })
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })
	return ds
}

// sqlServerForSharedCase returns a SQL Server datasource for a case that runs on several
// engines, or nil when SQL Server is not reachable and the run does not require it.
//
// gateSQLServer would skip the whole case, Postgres and MySQL subtests with it, so this leaves
// SQL Server out instead and says so, the way ormEngines leaves out an unreachable MySQL. Under
// E2E_REQUIRE_SQLSERVER=1 an unreachable server fails the case, as the gate would.
func sqlServerForSharedCase(t *testing.T) dbCore.IDataSource {
	t.Helper()

	ds, err := reachSQLServer(engineWait, func() (dbCore.IDataSource, error) { return openMSSQL(mssqlConfig()) })
	if err != nil {
		if sqlServerRunIsRequired() {
			t.Fatalf("%s is set, so this case must run against SQL Server too: %v", requireSQLServerEnvVar, err)
		}
		t.Logf("%v; this case runs without SQL Server", err)
		return nil
	}
	admitSQLServerCase()
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })
	return ds
}

// mssqlSession opens a session on ds for the rest of the case.
func mssqlSession(t *testing.T, ds dbCore.IDataSource) dbCore.ISession {
	t.Helper()

	session, err := ds.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Close()) })
	return session
}

// resetMSSQLTables drops the tables now and again when the case ends, and then runs the DDL
// that creates them. Tables are written as T-SQL names, bracketed where they need it, and
// dropped in the order given.
func resetMSSQLTables(t *testing.T, g *gorm.DB, tables []string, ddl ...string) {
	t.Helper()

	drop := func() {
		for _, table := range tables {
			assert.NoError(t, g.Exec("DROP TABLE IF EXISTS "+table).Error)
		}
	}
	drop()
	t.Cleanup(drop)
	for _, statement := range ddl {
		require.NoError(t, g.Exec(statement).Error, "DDL: %s", statement)
	}
}

// sqlServerErrorNumber returns the SQL Server error number err carries, or 0. It asks for the
// driver's method rather than its type, so these cases need not import go-mssqldb.
func sqlServerErrorNumber(err error) int32 {
	var numbered interface{ SQLErrorNumber() int32 }
	if errors.As(err, &numbered) {
		return numbered.SQLErrorNumber()
	}
	return 0
}

// requireUnsupported asserts that err is a dialect refusal naming construct.
func requireUnsupported(t *testing.T, err error, construct string) {
	t.Helper()

	require.Error(t, err, "%s must be refused", construct)
	var unsupported *dbCore.UnsupportedError
	require.ErrorAs(t, err, &unsupported, "the refusal of %s must be an UnsupportedError: %v", construct, err)
	assert.Equal(t, "sqlserver", unsupported.Dialect)
	assert.Contains(t, unsupported.Construct, construct)
}

// countWhere counts the rows of table matching where, a T-SQL predicate with no arguments.
func countWhere(t *testing.T, g *gorm.DB, table, where string) int64 {
	t.Helper()

	var n int64
	require.NoError(t, g.Raw("SELECT COUNT(*) FROM "+table+" WHERE "+where).Scan(&n).Error)
	return n
}

// ------------------------------------------------------------------ configuration

// TestMSSQLConfigRefusesAnAzureHost needs no server: the refusal happens before anything
// dials, which is the point of it.
func TestMSSQLConfigRefusesAnAzureHost(t *testing.T) {
	for _, host := range []string{"example.database.windows.net", "EXAMPLE.DATABASE.WINDOWS.NET."} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("E2E_MSSQL_HOST", host)

			for name, config := range map[string]func() map[string]any{
				"mssqlConfig":       mssqlConfig,
				"mssqlMasterConfig": mssqlMasterConfig,
			} {
				var refusal any
				func() {
					defer func() { refusal = recover() }()
					config()
				}()
				require.NotNil(t, refusal, "%s must refuse an Azure SQL host", name)
				assert.Contains(t, fmt.Sprint(refusal), host)
				assert.Contains(t, fmt.Sprint(refusal), "Azure SQL host")
			}
		})
	}

	t.Setenv("E2E_MSSQL_HOST", "mssql-live")
	assert.Equal(t, "mssql-live", mssqlConfig()["host"], "a compose service name is a local host")
}

// ------------------------------------------------------------------ connection

// TestSQLServerConnectsWithSQLLogin: the datasource signs in with a SQL login, lands in the
// configured database, and sets the session up the way the engine promises: encrypted by
// default, named gorgany, with XACT_ABORT on. A failed sign-in fails the constructor, naming
// the server and the method and never the password.
func TestSQLServerConnectsWithSQLLogin(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())

	assertDialect(t, ds, "sqlserver")
	assertDatabaseName(t, ds, mssqlTestDatabase)

	var session struct {
		Login      string
		AppName    string
		AuthScheme string
		Encrypted  string
		XactAbort  int
	}
	require.NoError(t, gormOf(t, ds).Raw(`SELECT SUSER_SNAME() AS Login, APP_NAME() AS AppName,
		c.auth_scheme AS AuthScheme, c.encrypt_option AS Encrypted, CAST(@@OPTIONS & 16384 AS int) AS XactAbort
		FROM sys.dm_exec_connections AS c WHERE c.session_id = @@SPID`).Scan(&session).Error)
	assert.Equal(t, "sa", session.Login)
	assert.Equal(t, "SQL", session.AuthScheme, "a SQL login, not Windows or Entra ID")
	assert.Equal(t, sqlserverv2.DefaultAppName, session.AppName)
	assert.Equal(t, "TRUE", session.Encrypted, "the connection is encrypted without being asked")
	assert.Equal(t, 16384, session.XactAbort, "every connection runs SET XACT_ABORT ON")

	const wrongPassword = "Not-The-Password-7"
	bad := mssqlConfig()
	bad["password"] = wrongPassword
	_, err := sqlserverv2.NewDataSource(bad)
	require.Error(t, err, "a failed login must fail the constructor")
	assert.Contains(t, err.Error(), fmt.Sprintf("cannot connect to %s/%s (auth: sql)", mssqlAddress(), mssqlTestDatabase))
	assert.Equal(t, int32(18456), sqlServerErrorNumber(err))
	assert.Contains(t, err.Error(), "check username and password", "18456 carries its hint")
	assert.NotContains(t, err.Error(), wrongPassword)
	assert.NotContains(t, err.Error(), mssqlPassword)

	_, err = sqlserverv2.NewDataSource(mssqlConfigFor("gorgany_test_absent"))
	require.Error(t, err, "a database that does not exist must fail the constructor")
	assert.Contains(t, err.Error(), "cannot open the database named in db")
	assert.NotContains(t, err.Error(), mssqlPassword)
	t.Logf("missing database reported as: %v", err)
}

// TestSQLServerDatasourceResolvesBesidePostgres: a SQL Server datasource configured next to a
// Postgres one resolves by name through the provider, speaks its own dialect, and lands in its
// own database.
func TestSQLServerDatasourceResolvesBesidePostgres(t *testing.T) {
	waitForDatasource(t, func() (dbCore.IDataSource, error) { return pgv2.NewDataSource(pgConfig()) }).Close()
	require.NoError(t, secondaryDataSource(t, mssqlConfig()).Close())

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{"default": pgConfig(), "legacy": mssqlConfig()})
	t.Cleanup(func() { viper.Set("databases", previous) })

	c := service.NewContainer()
	provider.NewDbProvider().Register(c)

	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))
	defaultDs, legacyDs := dbContext.GetDataSource("default"), dbContext.GetDataSource("legacy")
	require.NotNil(t, defaultDs)
	require.NotNil(t, legacyDs)
	t.Cleanup(func() {
		assert.NoError(t, defaultDs.Close())
		assert.NoError(t, legacyDs.Close())
	})

	assertDialect(t, defaultDs, "postgres")
	assertDialect(t, legacyDs, "sqlserver")
	assertDatabaseName(t, legacyDs, mssqlTestDatabase)
}

// orders2024 is an EF-style entity on a digit-leading table with a reserved-word column, which
// T-SQL accepts only bracketed.
type orders2024 struct {
	orm.BaseEntity
	ID     int64  `gorm:"column:Id;primaryKey;autoIncrement"`
	Order  int32  `gorm:"column:Order"`
	Status string `gorm:"column:Status"`
}

func (orders2024) TableName() string { return "dbo.2024Orders" }

// TestSQLServerHyphenatedDatabaseName: a database named the way EF Core projects often are is
// valid only bracketed, and the connection string passes it through as it is. The case then
// reads and writes a digit-leading table through the builder and the ORM, which is the other
// half of pointing gorgany at such a schema.
func TestSQLServerHyphenatedDatabaseName(t *testing.T) {
	mssqlDataSource(t, mssqlConfig())

	master, err := sqlserverv2.NewDataSource(mssqlMasterConfig())
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, master.Close()) })

	literal, bracketed := mssqlDatabaseNames(mssqlProbeDatabase)
	dropProbe := "IF DB_ID(N'" + literal + "') IS NOT NULL BEGIN ALTER DATABASE " + bracketed +
		" SET SINGLE_USER WITH ROLLBACK IMMEDIATE; DROP DATABASE " + bracketed + "; END"
	require.NoError(t, gormOf(t, master).Exec(dropProbe).Error)
	require.NoError(t, gormOf(t, master).Exec("CREATE DATABASE "+bracketed).Error)
	t.Cleanup(func() { assert.NoError(t, gormOf(t, master).Exec(dropProbe).Error) })

	probe, err := sqlserverv2.NewDataSource(mssqlConfigFor(mssqlProbeDatabase))
	require.NoError(t, err, "a hyphenated database name must connect")
	t.Cleanup(func() { assert.NoError(t, probe.Close()) })

	assertDatabaseName(t, probe, mssqlProbeDatabase)

	g := gormOf(t, probe)
	require.NoError(t, g.Exec(`CREATE TABLE [dbo].[2024Orders] ([Id] bigint IDENTITY(1,1) PRIMARY KEY,
		[Order] int NOT NULL, [Status] nvarchar(20) NOT NULL)`).Error)

	session := mssqlSession(t, probe)
	ctx := context.Background()

	insert := session.Query().Insert("dbo.2024Orders").Columns("Order", "Status").Values(3, "new")
	sql, _, err := insert.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "[dbo].[2024Orders] ([Order], [Status])")
	require.NoError(t, session.Executor().Exec(ctx, insert).Error)

	var statuses []string
	require.NoError(t, session.Executor().Find(ctx,
		session.Query().Select("Status").From("dbo.2024Orders").Eq("Order", 3), &statuses).Error)
	assert.Equal(t, []string{"new"}, statuses)

	orders := orm.New[*orders2024](session)
	order := &orders2024{Order: 9, Status: "orm"}
	require.NoError(t, orders.Create(order))
	require.NotZero(t, order.ID, "the IDENTITY key comes back through OUTPUT INSERTED")

	found, err := orders.Find(order.ID)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, int32(9), found.Order)

	found.Status = "paid"
	require.NoError(t, orders.Update(found))
	count, err := orders.CountByQuery(session.Query().Eq("Status", "paid"))
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	require.NoError(t, orders.Delete(found))
	total, err := orders.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), total, "only the builder's row is left")
}

// guidProbe reads a uniqueidentifier column back into the uuid types the engine documents.
type guidProbe struct {
	ID  int           `gorm:"column:Id"`
	Ref uuid.NullUUID `gorm:"column:Ref"`
}

// TestSQLServerGuidConversionRoundTrip: SQL Server stores a uniqueidentifier's first three
// groups little-endian, and go-mssqldb swaps them only with "guid conversion" on, which the
// datasource sets. Without it a uuid.UUID written and read back through gorgany compares
// equal, while the same row read by any other client shows different digits. So the case
// compares against the server's own text form, in both directions.
func TestSQLServerGuidConversionRoundTrip(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"guid_probe"},
		"CREATE TABLE guid_probe ([Id] int PRIMARY KEY, [Ref] uniqueidentifier NULL)")

	session := mssqlSession(t, ds)
	ctx := context.Background()

	// The groups are chosen so a byte-order mistake cannot come out the same.
	known := uuid.MustParse("6f9619ff-8b86-d011-b42d-00c04fc964ff")
	other := uuid.MustParse("01234567-89ab-cdef-0123-456789abcdef")

	require.NoError(t, session.Executor().Exec(ctx,
		session.Query().Insert("guid_probe").Columns("Id", "Ref").Values(1, known)).Error)
	require.NoError(t, session.Executor().ExecRaw(ctx,
		"INSERT INTO guid_probe ([Id], [Ref]) VALUES (?, ?)", 2, uuid.NullUUID{}).Error)
	require.NoError(t, session.Executor().ExecRaw(ctx,
		"INSERT INTO guid_probe ([Id], [Ref]) VALUES (?, ?)", 3, uuid.NullUUID{UUID: other, Valid: true}).Error)

	var shown []string
	require.NoError(t, g.Raw("SELECT CAST([Ref] AS nvarchar(36)) FROM guid_probe WHERE [Ref] IS NOT NULL ORDER BY [Id]").
		Scan(&shown).Error)
	require.Len(t, shown, 2)
	assert.True(t, strings.EqualFold(known.String(), shown[0]), "the server shows %s, not %s", shown[0], known)
	assert.True(t, strings.EqualFold(other.String(), shown[1]), "the server shows %s, not %s", shown[1], other)

	var rows []guidProbe
	require.NoError(t, session.Executor().Find(ctx,
		session.Query().Select("Id", "Ref").From("guid_probe").OrderBy("Id", "ASC"), &rows).Error)
	require.Len(t, rows, 3)
	assert.Equal(t, uuid.NullUUID{UUID: known, Valid: true}, rows[0].Ref)
	assert.Equal(t, uuid.NullUUID{}, rows[1].Ref, "a NULL reads back as an invalid NullUUID")
	assert.Equal(t, uuid.NullUUID{UUID: other, Valid: true}, rows[2].Ref)

	var ids []int
	require.NoError(t, session.Executor().Find(ctx,
		session.Query().Select("Id").From("guid_probe").Eq("Ref", known), &ids).Error)
	assert.Equal(t, []int{1}, ids, "a uuid bound as a parameter matches the row")

	var generated struct {
		Value uuid.UUID
		Text  string
	}
	require.NoError(t, g.Raw("SELECT g AS Value, CAST(g AS nvarchar(36)) AS Text FROM (SELECT NEWID() AS g) AS x").
		Scan(&generated).Error)
	assert.True(t, strings.EqualFold(generated.Value.String(), generated.Text),
		"a server-generated GUID scans as %s, but the server shows %s", generated.Value, generated.Text)
}

// TestSQLServerLazyConnectDefersTheLogin: with lazy_connect the constructor opens nothing, so
// a login that is going to fail fails on the first query instead of at boot, and a server that
// is not listening costs nothing until then.
func TestSQLServerLazyConnectDefersTheLogin(t *testing.T) {
	mssqlDataSource(t, mssqlConfig())
	ctx := context.Background()

	const wrongPassword = "Not-The-Password-7"
	bad := mssqlConfig()
	bad["password"] = wrongPassword
	_, err := sqlserverv2.NewDataSource(bad)
	require.Error(t, err, "without lazy_connect the failed login fails the constructor")

	bad["lazy_connect"] = true
	lazy, err := sqlserverv2.NewDataSource(bad)
	require.NoError(t, err, "with lazy_connect the constructor must not sign in")

	session := mssqlSession(t, lazy)
	_, err = session.Executor().CountRaw(ctx, "SELECT 1")
	require.Error(t, err, "the first query signs in, and fails")
	assert.Equal(t, int32(18456), sqlServerErrorNumber(err))
	assert.NotContains(t, err.Error(), wrongPassword)
	assert.NoError(t, lazy.Close())
	assert.NoError(t, lazy.Close(), "Close is idempotent")

	unreachable := mssqlConfig()
	unreachable["port"] = 1
	unreachable["lazy_connect"] = true
	started := time.Now()
	idle, err := sqlserverv2.NewDataSource(unreachable)
	require.NoError(t, err, "with lazy_connect a server that is not listening is not an error yet")
	assert.Less(t, time.Since(started), 2*time.Second, "and nothing was dialled")
	assert.NoError(t, idle.Close())

	good := mssqlConfig()
	good["lazy_connect"] = true
	working, err := sqlserverv2.NewDataSource(good)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, working.Close()) })
	n, err := mssqlSession(t, working).Executor().CountRaw(ctx, "SELECT 1")
	require.NoError(t, err, "the first query makes the first connection")
	assert.Equal(t, int64(1), n)
}

// ------------------------------------------------------------------ the dialect

// dialectProbeRow is a row of the round-trip table.
type dialectProbeRow struct {
	ID     int     `gorm:"column:Id"`
	Region *string `gorm:"column:Region"`
	Name   string  `gorm:"column:Name"`
	Amount int     `gorm:"column:Amount"`
}

// regionTotal is a row of a grouped query; Region is nil on a ROLLUP, CUBE or GROUPING SETS
// total.
type regionTotal struct {
	Region *string `gorm:"column:Region"`
	Total  int     `gorm:"column:Total"`
}

// TestSQLServerQueriesRoundTripThroughTheDialect proves the T-SQL the dialect renders is
// accepted by a real SQL Server 2022 and does what the builder asked, which the unit tests'
// strings cannot. Each subtest asserts the construct is in the SQL, runs it, and checks the
// rows; the refusals at the end are asserted here too, because a refusal that exists only as
// an expected string is a refusal nobody has seen.
func TestSQLServerQueriesRoundTripThroughTheDialect(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"dialect_probe", "dialect_regions", "[Order]"},
		"CREATE TABLE dialect_probe ([Id] int PRIMARY KEY, [Region] nvarchar(50) NULL, [Name] nvarchar(50) NOT NULL, [Amount] int NOT NULL)",
		"CREATE TABLE dialect_regions ([Region] nvarchar(50) PRIMARY KEY)",
		"CREATE TABLE [Order] ([Id] int PRIMARY KEY, [Order] int NOT NULL, [User] nvarchar(20) NOT NULL)",
		"INSERT INTO dialect_probe VALUES (1, 'north', 'ann', 10), (2, 'north', 'bob', 20), "+
			"(3, 'south', 'cid', 30), (4, 'south', 'dee', 40), (5, 'east', 'eve', 50)",
		"INSERT INTO dialect_regions VALUES ('north'), ('south'), ('east'), ('west')",
	)

	session := mssqlSession(t, ds)
	ex := session.Executor()
	ctx := context.Background()

	render := func(t *testing.T, q dbCore.IQueryBuilder, want ...string) {
		t.Helper()
		sql, _, err := q.ToSQL()
		require.NoError(t, err)
		for _, fragment := range want {
			assert.Contains(t, sql, fragment)
		}
		t.Logf("SQL: %s", sql)
	}
	ids := func(t *testing.T, q dbCore.IQueryBuilder) []int {
		t.Helper()
		var got []int
		require.NoError(t, ex.Find(ctx, q, &got).Error)
		return got
	}

	t.Run("TOP", func(t *testing.T) {
		q := session.Query().Select("Id").From("dialect_probe").OrderBy("Id", "DESC").Limit(2)
		render(t, q, "SELECT TOP (2) [Id]")
		assert.Equal(t, []int{5, 4}, ids(t, q))

		zero := session.Query().Select("Id").From("dialect_probe").Limit(0)
		render(t, zero, "SELECT TOP (0) [Id]")
		assert.Empty(t, ids(t, zero), "TOP (0) is legal where FETCH NEXT 0 is not")
	})

	t.Run("OFFSET FETCH", func(t *testing.T) {
		synthetic := session.Query().Select("Id").From("dialect_probe").Offset(1).Limit(2)
		render(t, synthetic, "ORDER BY (SELECT NULL) OFFSET 1 ROWS FETCH NEXT 2 ROWS ONLY")
		assert.Len(t, ids(t, synthetic), 2, "OFFSET needs an ORDER BY, and gets one")

		explicit := session.Query().Select("Id").From("dialect_probe").OrderBy("Id", "ASC").Offset(1).Limit(2)
		render(t, explicit, "ORDER BY [Id] ASC OFFSET 1 ROWS FETCH NEXT 2 ROWS ONLY")
		assert.Equal(t, []int{2, 3}, ids(t, explicit))

		bare := session.Query().Select("Id").From("dialect_probe").OrderBy("Id", "ASC").Offset(3)
		render(t, bare, "OFFSET 3 ROWS")
		assert.Equal(t, []int{4, 5}, ids(t, bare))
	})

	t.Run("OUTPUT on each statement", func(t *testing.T) {
		var inserted []dialectProbeRow
		insert := session.Query().Insert("dialect_probe").Columns("Id", "Region", "Name", "Amount").
			Values(6, "west", "fay", 60).Returning("Id", "Name")
		render(t, insert, "OUTPUT INSERTED.[Id], INSERTED.[Name] VALUES")
		require.NoError(t, ex.Find(ctx, insert, &inserted).Error)
		require.Len(t, inserted, 1)
		assert.Equal(t, 6, inserted[0].ID)
		assert.Equal(t, "fay", inserted[0].Name)

		var updated []int
		update := session.Query().Update("dialect_probe").Set("Amount", 61).Eq("Id", 6).Returning("Amount")
		render(t, update, "OUTPUT INSERTED.[Amount] WHERE")
		require.NoError(t, ex.Find(ctx, update, &updated).Error)
		assert.Equal(t, []int{61}, updated)

		var merged []int
		merge := session.Query().Insert("dialect_probe").Columns("Id", "Region", "Name", "Amount").
			Values(6, "west", "fay", 62).OnConflict("Id").DoUpdate(map[string]any{"Amount": 62}).Returning("Amount")
		render(t, merge, "MERGE INTO", "OUTPUT INSERTED.[Amount]")
		require.NoError(t, ex.Find(ctx, merge, &merged).Error)
		assert.Equal(t, []int{62}, merged)

		var deleted []int
		remove := session.Query().Delete("dialect_probe").Eq("Id", 6).Returning("Id")
		render(t, remove, "OUTPUT DELETED.[Id] WHERE")
		require.NoError(t, ex.Find(ctx, remove, &deleted).Error)
		assert.Equal(t, []int{6}, deleted)

		result := ex.Exec(ctx, session.Query().Delete("dialect_probe").Eq("Id", 6).Returning("Id"))
		require.NoError(t, result.Error)
		assert.Zero(t, result.RowsAffected, "with OUTPUT the count is the rows OUTPUT returned")
	})

	upsertRow := func(id, amount int) dbCore.IQueryBuilder {
		return session.Query().Insert("dialect_probe").Columns("Id", "Region", "Name", "Amount").
			Values(id, "west", "gus", amount)
	}
	amountOf := func(t *testing.T, id int) int {
		t.Helper()
		var amount []int
		require.NoError(t, ex.Find(ctx, session.Query().Select("Amount").From("dialect_probe").Eq("Id", id), &amount).Error)
		require.Len(t, amount, 1)
		return amount[0]
	}

	t.Run("DO NOTHING twice", func(t *testing.T) {
		doNothing := func(amount int) dbCore.IQueryBuilder { return upsertRow(7, amount).OnConflict("Id").DoNothing() }
		render(t, doNothing(70), "WHERE NOT EXISTS", "WITH (UPDLOCK, HOLDLOCK)")

		first := ex.Exec(ctx, doNothing(70))
		require.NoError(t, first.Error)
		assert.Equal(t, int64(1), first.RowsAffected)

		second := ex.Exec(ctx, doNothing(99))
		require.NoError(t, second.Error, "a conflict is not an error")
		assert.Zero(t, second.RowsAffected)
		assert.Equal(t, 70, amountOf(t, 7), "DO NOTHING must not overwrite the row")
	})

	t.Run("MERGE twice", func(t *testing.T) {
		merge := func(amount int) dbCore.IQueryBuilder {
			return upsertRow(8, amount).OnConflict("Id").DoUpdate(map[string]any{"Amount": amount})
		}
		render(t, merge(80), "MERGE INTO [dialect_probe] WITH (HOLDLOCK) AS [tgt]", "WHEN MATCHED THEN UPDATE SET")

		first := ex.Exec(ctx, merge(80))
		require.NoError(t, first.Error)
		assert.Equal(t, int64(1), first.RowsAffected, "the first MERGE inserts")

		second := ex.Exec(ctx, merge(81))
		require.NoError(t, second.Error)
		assert.Equal(t, int64(1), second.RowsAffected, "the second MERGE updates")
		assert.Equal(t, 81, amountOf(t, 8))
	})

	// Every worker upserts the same new key at once. Without the locking hints two of them
	// would both find no row and both insert, and one would fail with a duplicate key (2627).
	t.Run("concurrent upserts of one key", func(t *testing.T) {
		const workers = 16
		run := func(build func(worker int) dbCore.IQueryBuilder) []dbCore.QueryResult {
			results := make([]dbCore.QueryResult, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for worker := range workers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					results[worker] = ex.Exec(ctx, build(worker))
				}()
			}
			close(start)
			wg.Wait()
			return results
		}

		var inserted int64
		for worker, result := range run(func(worker int) dbCore.IQueryBuilder {
			return upsertRow(9, worker).OnConflict("Id").DoNothing()
		}) {
			require.NoError(t, result.Error, "DO NOTHING worker %d", worker)
			inserted += result.RowsAffected
		}
		assert.Equal(t, int64(1), inserted, "exactly one DO NOTHING inserts")
		assert.Equal(t, int64(1), countWhere(t, g, "dialect_probe", "[Id] = 9"))

		var written int64
		for worker, result := range run(func(worker int) dbCore.IQueryBuilder {
			return upsertRow(10, worker).OnConflict("Id").DoUpdate(map[string]any{"Amount": worker})
		}) {
			require.NoError(t, result.Error, "MERGE worker %d", worker)
			written += result.RowsAffected
		}
		assert.Equal(t, int64(workers), written, "each MERGE inserts or updates its one row")
		assert.Equal(t, int64(1), countWhere(t, g, "dialect_probe", "[Id] = 10"))
	})

	// Every worker sends multi-row DO NOTHING batches drawn from one pool of keys, some in key
	// order and some not, in a database where readers see a snapshot. The probe locks the keys
	// that exist as well as the missing ones, so batches that share keys can deadlock (Msg
	// 1205); what the translation must never do is fail a batch with a duplicate key, or lose
	// or double a key. A deadlock victim was rolled back whole and carries the hint that says
	// so, and a retry of it lands.
	t.Run("concurrent overlapping batches", func(t *testing.T) {
		resetMSSQLTables(t, g, []string{"upsert_tags"},
			"CREATE TABLE upsert_tags ([Id] int IDENTITY PRIMARY KEY, [Name] nvarchar(40) NOT NULL UNIQUE)")
		const (
			workers = 8
			batches = 10
			pool    = 60
			size    = 12
		)
		var (
			mu        sync.Mutex
			deadlocks int
			failures  []error
			wg        sync.WaitGroup
		)
		start := make(chan struct{})
		for worker := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for batch := range batches {
					keys := make([]int, size)
					for i := range keys {
						keys[i] = (worker*7 + batch*13 + i*5) % pool
					}
					if worker%2 == 0 {
						slices.Sort(keys)
					}
					keys = slices.Compact(keys)
					insert := session.Query().Insert("upsert_tags").Columns("Name")
					for _, key := range keys {
						insert = insert.Values(fmt.Sprintf("tag-%02d", key))
					}
					for attempt := 0; ; attempt++ {
						err := ex.Exec(ctx, insert.OnConflict("Name").DoNothing()).Error
						if err == nil {
							break
						}
						mu.Lock()
						if sqlServerErrorNumber(err) == 1205 && attempt < 20 {
							deadlocks++
							if !strings.Contains(err.Error(), "safe to run again") {
								failures = append(failures, fmt.Errorf("the deadlock carries no hint: %w", err))
							}
							mu.Unlock()
							continue
						}
						failures = append(failures, err)
						mu.Unlock()
						return
					}
				}
			}()
		}
		close(start)
		wg.Wait()

		require.Empty(t, failures, "only deadlocks, which retry clean; never a duplicate key (2627)")
		t.Logf("%d deadlocks retried", deadlocks)
		var names []string
		require.NoError(t, g.Raw("SELECT [Name] FROM upsert_tags ORDER BY [Name]").Scan(&names).Error)
		assert.Len(t, names, pool, "every key of the pool once")
		assert.Equal(t, slices.Compact(slices.Clone(names)), names, "and none twice")
	})

	// A VALUES list is limited to 1000 rows only in an INSERT … VALUES. As the derived table an
	// upsert reads, it has no limit, so 1500 one-column rows go in one statement.
	t.Run("an upsert of more than 1000 rows", func(t *testing.T) {
		resetMSSQLTables(t, g, []string{"upsert_many"}, "CREATE TABLE upsert_many ([Name] nvarchar(20) PRIMARY KEY)")
		insert := session.Query().Insert("upsert_many").Columns("Name")
		for i := range 1500 {
			insert = insert.Values(fmt.Sprintf("n%04d", i))
		}
		first := ex.Exec(ctx, insert.OnConflict("Name").DoNothing())
		require.NoError(t, first.Error)
		assert.Equal(t, int64(1500), first.RowsAffected)
		again := ex.Exec(ctx, insert.OnConflict("Name").DoNothing())
		require.NoError(t, again.Error)
		assert.Zero(t, again.RowsAffected)
	})

	require.NoError(t, g.Exec("DELETE FROM dialect_probe WHERE [Id] > 5").Error)

	t.Run("ROLLUP, CUBE and GROUPING SETS", func(t *testing.T) {
		totals := func(t *testing.T, q dbCore.IQueryBuilder) map[string]int {
			t.Helper()
			var rows []regionTotal
			require.NoError(t, ex.Find(ctx, q, &rows).Error)
			byRegion := map[string]int{}
			for _, row := range rows {
				key := "(total)"
				if row.Region != nil {
					key = *row.Region
				}
				byRegion[key] = row.Total
			}
			return byRegion
		}
		want := map[string]int{"north": 30, "south": 70, "east": 50, "(total)": 150}
		base := func() dbCore.IQueryBuilder {
			return session.Query().Select("Region", "SUM(Amount) AS Total").From("dialect_probe")
		}

		rollup := base().Rollup("Region")
		render(t, rollup, "GROUP BY ROLLUP ([Region])")
		assert.Equal(t, want, totals(t, rollup))

		cube := base().Cube("Region")
		render(t, cube, "GROUP BY CUBE ([Region])")
		assert.Equal(t, want, totals(t, cube))

		sets := base().GroupingSets([]string{"Region"}, []string{})
		render(t, sets, "GROUP BY GROUPING SETS (([Region]), ())")
		assert.Equal(t, want, totals(t, sets))
	})

	t.Run("CROSS APPLY and OUTER APPLY", func(t *testing.T) {
		biggest := session.Query().Select("p.Name").From("dialect_probe p").
			Where(&dbCore.BinaryCondition{Left: "p.Region", Operator: "=", Right: dbCore.Identifier("r.Region")}).
			OrderBy("p.Amount", "DESC").Limit(1).Build()

		type regionBiggest struct {
			Region string  `gorm:"column:Region"`
			Name   *string `gorm:"column:Name"`
		}

		var crossed []regionBiggest
		cross := session.Query().Select("r.Region", "x.Name").From("dialect_regions r").
			LateralJoin(biggest, "x", nil).OrderBy("r.Region", "ASC")
		render(t, cross, "CROSS APPLY (SELECT TOP (1) [p].[Name]")
		require.NoError(t, ex.Find(ctx, cross, &crossed).Error)
		require.Len(t, crossed, 3, "CROSS APPLY drops the region with no rows")
		assert.Equal(t, "east", crossed[0].Region)
		assert.Equal(t, "eve", *crossed[0].Name)
		assert.Equal(t, "bob", *crossed[1].Name)
		assert.Equal(t, "dee", *crossed[2].Name)

		var outer []regionBiggest
		outerApply := session.Query().Select("r.Region", "x.Name").From("dialect_regions r").
			Join(&dbCore.JoinClause{Type: "LEFT LATERAL", IsLateral: true, IsSubquery: true, Subquery: biggest, Alias: "x"}).
			OrderBy("r.Region", "ASC")
		render(t, outerApply, "OUTER APPLY (SELECT TOP (1)")
		require.NoError(t, ex.Find(ctx, outerApply, &outer).Error)
		require.Len(t, outer, 4, "OUTER APPLY keeps it")
		assert.Equal(t, "west", outer[3].Region)
		assert.Nil(t, outer[3].Name)
	})

	t.Run("DELETE TOP (1)", func(t *testing.T) {
		require.NoError(t, g.Exec("INSERT INTO dialect_probe VALUES (11, 'dup', 'x', 1), (12, 'dup', 'y', 1)").Error)

		remove := session.Query().Delete("dialect_probe").Eq("Region", "dup").Limit(1)
		render(t, remove, "DELETE TOP (1) FROM [dialect_probe]")
		result := ex.Exec(ctx, remove)
		require.NoError(t, result.Error)
		assert.Equal(t, int64(1), result.RowsAffected)
		assert.Equal(t, int64(1), countWhere(t, g, "dialect_probe", "[Region] = 'dup'"))

		update := session.Query().Update("dialect_probe").Set("Amount", 2).Eq("Region", "dup").Limit(1)
		render(t, update, "UPDATE TOP (1) [dialect_probe] SET")
		require.NoError(t, ex.Exec(ctx, update).Error)
		require.NoError(t, g.Exec("DELETE FROM dialect_probe WHERE [Region] = 'dup'").Error)
	})

	t.Run("ILIKE", func(t *testing.T) {
		q := session.Query().Select("Id").From("dialect_probe").
			Where(&dbCore.BinaryCondition{Left: "Name", Operator: "ILIKE", Right: "ANN"})
		sql, _, err := q.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "[Name] LIKE ?")
		assert.NotContains(t, sql, "ILIKE")
		assert.Equal(t, []int{1}, ids(t, q), "LIKE is case-insensitive under the default collation")

		raw := session.Query().Select("Id").From("dialect_probe").
			Where(&dbCore.RawCondition{SQL: "Name ILIKE ?", Args: []any{"B%"}})
		assert.Equal(t, []int{2}, ids(t, raw))

		// Only the operator is rewritten: the word inside a literal is data.
		literal := session.Query().Select("Id").From("dialect_probe").
			Where(&dbCore.RawCondition{SQL: "Name ILIKE ? AND 'an ilike word' = 'an ilike word'", Args: []any{"C%"}})
		assert.Equal(t, []int{3}, ids(t, literal))
	})

	// LIKE's [ ] is a character class on SQL Server, and the pattern is bound as it is, so the
	// same pattern matches more rows here than on Postgres and MySQL; an escaped [ is literal.
	t.Run("LIKE character class", func(t *testing.T) {
		class := session.Query().Select("Id").From("dialect_probe").Like("Name", "[ab]%").OrderBy("Id", "ASC")
		assert.Equal(t, []int{1, 2}, ids(t, class), "[ab] matches ann and bob")

		escaped := session.Query().Select("Id").From("dialect_probe").LikeEscape("Name", "![ab]%", "!")
		assert.Empty(t, ids(t, escaped), "no name begins with a literal [ab]")
		bracketed := session.Query().Select("Id").From("dialect_probe").Like("Name", "[[]ab]%")
		assert.Empty(t, ids(t, bracketed))
	})

	// The builder's Over inlines a window where the select list names it; the dialect renders
	// it, bracketed, with the frame T-SQL spells.
	t.Run("window through Over", func(t *testing.T) {
		type running struct {
			ID    int `gorm:"column:Id"`
			Total int `gorm:"column:running"`
		}
		b := session.Query().Window("SUM(Amount)", &dbCore.WindowDefinition{
			PartitionBy: []string{"Region"},
			OrderBy:     []dbCore.OrderByField{{Field: "Amount", Direction: "desc"}},
			Frame: &dbCore.WindowFrame{Type: "ROWS", Start: &dbCore.FrameBound{Type: "PRECEDING", Value: 1},
				End: &dbCore.FrameBound{Type: "CURRENT ROW"}},
		})
		q := b.Select("Id", b.Over("SUM(Amount)")+" AS running").From("dialect_probe").OrderBy("Id", "ASC")
		render(t, q, "SUM(Amount) OVER (PARTITION BY [Region] ORDER BY [Amount] DESC ROWS BETWEEN 1 PRECEDING AND CURRENT ROW) AS running")
		var rows []running
		require.NoError(t, ex.Find(ctx, q, &rows).Error)
		assert.Equal(t, []running{{1, 30}, {2, 20}, {3, 70}, {4, 40}, {5, 50}}, rows)

		groups := session.Query().Window("ROW_NUMBER()", &dbCore.WindowDefinition{OrderBy: []dbCore.OrderByField{{Field: "Id"}},
			Frame: &dbCore.WindowFrame{Type: "GROUPS", Start: &dbCore.FrameBound{Type: "CURRENT ROW"}}})
		var sink []running
		requireUnsupported(t, ex.Find(ctx, groups.Select("Id").From("dialect_probe"), &sink).Error, "a GROUPS window frame")
	})

	t.Run("[Order]", func(t *testing.T) {
		insert := session.Query().Insert("Order").Columns("Id", "Order", "User").Values(1, 7, "u")
		render(t, insert, "INSERT INTO [Order] ([Id], [Order], [User])")
		require.NoError(t, ex.Exec(ctx, insert).Error)

		update := session.Query().Update("Order").Set("Order", 8).Eq("Order", 7)
		result := ex.Exec(ctx, update)
		require.NoError(t, result.Error)
		assert.Equal(t, int64(1), result.RowsAffected)

		var users []string
		selectQ := session.Query().Select("User").From("Order").Eq("Order", 8)
		render(t, selectQ, "SELECT [User] FROM [Order] WHERE [Order] = ?")
		require.NoError(t, ex.Find(ctx, selectQ, &users).Error)
		assert.Equal(t, []string{"u"}, users)
	})

	t.Run("TOP inside an IN subquery", func(t *testing.T) {
		biggestTwo := session.Query().Select("Id").From("dialect_probe").OrderBy("Amount", "DESC").Limit(2).Build()
		q := session.Query().Select("Id").From("dialect_probe").InSubquery("Id", biggestTwo).OrderBy("Id", "ASC")
		render(t, q, "IN (SELECT TOP (2) [Id] FROM [dialect_probe] ORDER BY [Amount] DESC)")
		assert.Equal(t, []int{4, 5}, ids(t, q))
	})

	t.Run("refusals", func(t *testing.T) {
		withOrder := session.Query().Select("Id").From("dialect_probe").OrderBy("Amount", "DESC").Build()
		withCTE := session.Query().WithCTE("c", session.Query().Select("Id").From("dialect_probe").Build()).
			Select("Id").From("c").Build()
		realOn := &dbCore.BinaryCondition{Left: "x.Id", Operator: "=", Right: dbCore.Identifier("p.Id")}
		tooManyRows := session.Query().Insert("dialect_probe").Columns("Id", "Name", "Amount")
		for i := 0; i <= sqlserverv2.MaxInsertRows; i++ {
			tooManyRows = tooManyRows.Values(1000+i, "n", i)
		}

		for construct, q := range map[string]dbCore.IQueryBuilder{
			"DISTINCT ON":                         session.Query().Select("Id").From("dialect_probe").DistinctOn("Region"),
			"NATURAL JOIN":                        session.Query().Select("Id").From("dialect_probe").NaturalJoin("dialect_regions"),
			"a LATERAL join with an ON":           session.Query().Select("p.Id").From("dialect_probe p").LateralJoin(withOrder, "x", realOn),
			"ORDER BY in a subquery":              session.Query().Select("Id").From("dialect_probe").InSubquery("Id", withOrder),
			"ORDER BY, Limit or Offset on a":      session.Query().Select("Id").From("dialect_probe").Union(session.Query().Select("Id").From("dialect_probe").Build()).OrderBy("Id", "ASC"),
			"a WITH clause inside":                session.Query().Select("Id").From("dialect_probe").InSubquery("Id", withCTE),
			"an aggregate FILTER":                 session.Query().Select("COUNT(*) FILTER (WHERE Amount > 10)").From("dialect_probe"),
			"the ~ operator":                      session.Query().Select("Id").From("dialect_probe").Where(&dbCore.BinaryCondition{Left: "Name", Operator: "~", Right: "^a"}),
			"RETURNING on a SELECT":               session.Query().Select("Id").From("dialect_probe").Returning("Id"),
			"ORDER BY on something":               session.Query().Select("Id").From("dialect_probe").OrderBy("Amount + 1", "ASC"),
			"ORDER BY on an UPDATE":               session.Query().Update("dialect_probe").Set("Amount", 1).OrderBy("Id", "ASC").Limit(1),
			"an INSERT of 1001 rows":              tooManyRows,
			"a GROUP BY position":                 session.Query().Select("Region", "COUNT(*) AS n").From("dialect_probe").GroupBy("1"),
			`LIKE ESCAPE "?"`:                     session.Query().Select("Id").From("dialect_probe").LikeEscape("Name", "x??y", "?").Eq("Amount", 4),
			"the bracketed name [dialect_probe?]": session.Query().Select("Id").From("[dialect_probe?]").Eq("Id", 1),
		} {
			t.Run(construct, func(t *testing.T) {
				_, _, err := q.ToSQL()
				requireUnsupported(t, err, construct)

				var sink []int
				requireUnsupported(t, ex.Find(ctx, q, &sink).Error, construct)
			})
		}

		// The first of them at the server: without the dialect's refusal this is what an app
		// would have met, and the hint is what it would have read.
		var sink []int
		err := ex.FindRaw(ctx, &sink, "SELECT [Id] FROM (SELECT [Id] FROM dialect_probe ORDER BY [Amount]) AS x")
		require.Error(t, err.Error)
		assert.Equal(t, int32(1033), sqlServerErrorNumber(err.Error))
		assert.Contains(t, err.Error.Error(), "drop the ORDER BY, or add a Limit")

		searchPath := mssqlConfig()
		searchPath["search_path"] = "sales"
		_, connectErr := sqlserverv2.NewDataSource(searchPath)
		requireUnsupported(t, connectErr, "search_path")
	})
}

// TestSQLServerParameterLimit: SQL Server takes 2100 parameters per request, and go-mssqldb
// spends two of them on sp_executesql, so a statement may carry 2098. The builder and the raw
// paths both refuse the 2099th before anything is sent, counted after gorm expands an IN (?)
// slice, and the last check shows the number is the server's and not a guess.
func TestSQLServerParameterLimit(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"param_probe"},
		"CREATE TABLE param_probe ([Id] int PRIMARY KEY)",
		"INSERT INTO param_probe VALUES (1), (2), (3)")

	session := mssqlSession(t, ds)
	ex := session.Executor()
	ctx := context.Background()

	require.Equal(t, sqlserverv2.MaxBindParameters, dbCore.BindParameterLimit(session.Query().Dialect()))

	values := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = i
		}
		return out
	}
	ints := func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out
	}

	var rows []int
	require.NoError(t, ex.Find(ctx, session.Query().Select("Id").From("param_probe").In("Id", values(2098)...), &rows).Error)
	assert.Len(t, rows, 3, "2098 builder parameters run")

	over := session.Query().Select("Id").From("param_probe").In("Id", values(2099)...)
	_, _, err := over.ToSQL()
	requireUnsupported(t, err, "2099 bound parameters")
	requireUnsupported(t, ex.Find(ctx, over, &rows).Error, "2099 bound parameters")

	rows = nil
	require.NoError(t, ex.FindRaw(ctx, &rows, "SELECT [Id] FROM param_probe WHERE [Id] IN (?)", ints(2098)).Error)
	assert.Len(t, rows, 3, "2098 raw parameters run")
	requireUnsupported(t, ex.FindRaw(ctx, &rows, "SELECT [Id] FROM param_probe WHERE [Id] IN (?)", ints(2099)).Error,
		"2099 bound parameters")
	_, err = ex.CountRaw(ctx, "SELECT COUNT(*) FROM param_probe WHERE [Id] IN (?)", ints(2099))
	requireUnsupported(t, err, "2099 bound parameters")
	requireUnsupported(t, ex.ExecRaw(ctx, "DELETE FROM param_probe WHERE [Id] IN (?)", ints(2099)).Error,
		"2099 bound parameters")
	assert.Equal(t, int64(3), countWhere(t, g, "param_probe", "1 = 1"), "the refused DELETE sent nothing")

	// Straight through database/sql, past gorgany: 2098 run, and the server refuses 2099.
	sqlDB, err := g.DB()
	require.NoError(t, err)
	direct := func(n int) error {
		placeholders := make([]string, n)
		for i := range placeholders {
			placeholders[i] = fmt.Sprintf("@p%d", i+1)
		}
		var count int
		return sqlDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM param_probe WHERE [Id] IN ("+strings.Join(placeholders, ", ")+")", values(n)...).
			Scan(&count)
	}
	require.NoError(t, direct(2098))
	err = direct(2099)
	require.Error(t, err, "the server itself refuses the 2099th parameter")
	assert.Equal(t, int32(8003), sqlServerErrorNumber(err))
}

// ------------------------------------------------------------------ row counts and triggers

// triggerTables creates trig_probe with a trigger that, like many written for EF-owned
// schemas, has no SET NOCOUNT ON and writes three audit rows for every statement, and a second
// trigger that returns a result set of its own.
func triggerTables(t *testing.T, g *gorm.DB) {
	t.Helper()

	resetMSSQLTables(t, g, []string{"trig_probe", "trig_audit"},
		"CREATE TABLE trig_probe ([Id] int PRIMARY KEY, [Status] nvarchar(20) NOT NULL)",
		"CREATE TABLE trig_audit ([Id] int IDENTITY(1000, 1) PRIMARY KEY, [Note] nvarchar(20) NOT NULL)",
		"CREATE TRIGGER trig_probe_audit ON trig_probe AFTER INSERT, UPDATE, DELETE AS "+
			"INSERT INTO trig_audit ([Note]) SELECT 'a' UNION ALL SELECT 'b' UNION ALL SELECT 'c'",
		"CREATE TRIGGER trig_probe_chatter ON trig_probe AFTER UPDATE AS SELECT 'from the trigger' AS [note]",
		"INSERT INTO trig_probe VALUES (1, 'new'), (2, 'new')",
	)
}

// TestSQLServerRowsAffectedIgnoresTriggerRows: go-mssqldb reports every row a request touched,
// a trigger's included, so a builder write reads its own count from ROWCOUNT_BIG() instead. A
// guarded UPDATE that matches nothing must report 0, or the ORM's update guard would take it
// for a success.
func TestSQLServerRowsAffectedIgnoresTriggerRows(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	triggerTables(t, g)

	session := mssqlSession(t, ds)
	ex := session.Executor()
	ctx := context.Background()
	audited := func() int64 { return countWhere(t, g, "trig_audit", "1 = 1") }

	before := audited()
	update := ex.Exec(ctx, session.Query().Update("trig_probe").Set("Status", "paid").Eq("Id", 1))
	require.NoError(t, update.Error, "the trigger's own result set must not break the batch")
	assert.Equal(t, int64(1), update.RowsAffected, "the UPDATE's own row, not the trigger's three")
	assert.Equal(t, before+3, audited(), "the trigger did write its rows")

	none := ex.Exec(ctx, session.Query().Update("trig_probe").Set("Status", "paid").Eq("Id", 99))
	require.NoError(t, none.Error)
	assert.Zero(t, none.RowsAffected, "an UPDATE that matched nothing reports 0, although the trigger ran")

	insert := ex.Exec(ctx, session.Query().Insert("trig_probe").Columns("Id", "Status").Values(3, "new"))
	require.NoError(t, insert.Error)
	assert.Equal(t, int64(1), insert.RowsAffected)

	remove := ex.Exec(ctx, session.Query().Delete("trig_probe").Eq("Id", 3))
	require.NoError(t, remove.Error)
	assert.Equal(t, int64(1), remove.RowsAffected)
}

// TestSQLServerExecRawCountIncludesTriggerRows pins the documented other side: ExecRaw reports
// the driver's count, which is every row count the request's statements sent, a trigger's
// included when it has no SET NOCOUNT ON, and the rows a trigger SELECTs among them.
func TestSQLServerExecRawCountIncludesTriggerRows(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	triggerTables(t, g)

	result := mssqlSession(t, ds).Executor().ExecRaw(context.Background(),
		"UPDATE trig_probe SET [Status] = 'raw' WHERE [Id] = 1")
	require.NoError(t, result.Error)
	assert.Equal(t, int64(5), result.RowsAffected,
		"the UPDATE's row, the audit trigger's three and the row the other trigger selected")
}

// TestSQLServerRowcountBigAfterInsert: ExecInsert reads the key with SCOPE_IDENTITY() and the
// count with ROWCOUNT_BIG() in one batch after the INSERT. SCOPE_IDENTITY() is the INSERT's
// own key, never the one a trigger's INSERT generated, which is what @@IDENTITY would return.
func TestSQLServerRowcountBigAfterInsert(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"rc_probe", "rc_plain", "rc_audit"},
		"CREATE TABLE rc_probe ([Id] bigint IDENTITY(1, 1) PRIMARY KEY, [Name] nvarchar(20) NOT NULL)",
		"CREATE TABLE rc_plain ([Code] nvarchar(10) PRIMARY KEY)",
		"CREATE TABLE rc_audit ([Id] int IDENTITY(500, 1) PRIMARY KEY, [Note] nvarchar(20) NOT NULL)")

	session := mssqlSession(t, ds)
	inserter, ok := session.Executor().(dbCore.LastInsertIDExecutor)
	require.True(t, ok, "the SQL Server executor reads generated keys")
	ctx := context.Background()

	one := inserter.ExecInsert(ctx, session.Query().Insert("rc_probe").Columns("Name").Values("a"))
	require.NoError(t, one.Error)
	assert.True(t, one.HasLastInsertID)
	assert.Equal(t, int64(1), one.LastInsertID)
	assert.Equal(t, int64(1), one.RowsAffected)

	two := inserter.ExecInsert(ctx, session.Query().Insert("rc_probe").Columns("Name").Values("b").Values("c"))
	require.NoError(t, two.Error)
	assert.Equal(t, int64(3), two.LastInsertID, "a multi-row INSERT reports its last key")
	assert.Equal(t, int64(2), two.RowsAffected)

	three := session.Executor().Exec(ctx, session.Query().Insert("rc_probe").Columns("Name").Values("d").Values("e").Values("f"))
	require.NoError(t, three.Error)
	assert.Equal(t, int64(3), three.RowsAffected)

	copied := session.Executor().Exec(ctx, session.Query().Insert("rc_plain").Columns("Code").
		FromSelect(session.Query().Select("Name").From("rc_probe").Build()))
	require.NoError(t, copied.Error)
	assert.Equal(t, int64(6), copied.RowsAffected, "INSERT … SELECT counts the rows it copied")

	plain := inserter.ExecInsert(ctx, session.Query().Insert("rc_plain").Columns("Code").Values("zz"))
	require.NoError(t, plain.Error)
	assert.False(t, plain.HasLastInsertID, "a table without IDENTITY has no key to report")
	assert.Equal(t, int64(1), plain.RowsAffected)

	require.NoError(t, g.Exec("CREATE TRIGGER rc_probe_audit ON rc_probe AFTER INSERT AS "+
		"INSERT INTO rc_audit ([Note]) VALUES ('i')").Error)
	audited := inserter.ExecInsert(ctx, session.Query().Insert("rc_probe").Columns("Name").Values("g"))
	require.NoError(t, audited.Error, "ExecInsert works on a table with a trigger, where OUTPUT does not")
	assert.Equal(t, int64(7), audited.LastInsertID, "the INSERT's own key, not the trigger's 500")
	assert.Equal(t, int64(1), audited.RowsAffected)
	assert.Equal(t, int64(1), countWhere(t, g, "rc_audit", "[Id] = 500"), "the trigger did insert")

	var refused []int64
	find := session.Executor().Find(ctx, session.Query().Insert("rc_probe").Columns("Name").Values("h").Returning("Id"), &refused)
	require.Error(t, find.Error, "OUTPUT without INTO on a table with a trigger")
	assert.Equal(t, int32(334), sqlServerErrorNumber(find.Error))
	assert.Contains(t, find.Error.Error(), "ExecInsert", "Msg 334 carries its hint")
}

// TestSQLServerRowsAffectedIsMatchedRows: an UPDATE counts the rows it matched, also those it
// set to the value they had. That is what the ORM's update guard assumes, and what MySQL
// reports only with CLIENT_FOUND_ROWS.
func TestSQLServerRowsAffectedIsMatchedRows(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"matched_probe"},
		"CREATE TABLE matched_probe ([Id] int PRIMARY KEY, [Kind] nvarchar(10) NOT NULL, [Amount] int NOT NULL)",
		"INSERT INTO matched_probe VALUES (1, 'a', 10), (2, 'a', 20), (3, 'b', 30)")

	session := mssqlSession(t, ds)
	ex := session.Executor()
	ctx := context.Background()

	unchanged := ex.Exec(ctx, session.Query().Update("matched_probe").Set("Amount", 10).Eq("Id", 1))
	require.NoError(t, unchanged.Error)
	assert.Equal(t, int64(1), unchanged.RowsAffected, "a row set to its own value still counts")

	both := ex.Exec(ctx, session.Query().Update("matched_probe").Set("Kind", "a").Eq("Kind", "a"))
	require.NoError(t, both.Error)
	assert.Equal(t, int64(2), both.RowsAffected)

	none := ex.Exec(ctx, session.Query().Update("matched_probe").Set("Amount", 1).Eq("Id", 99))
	require.NoError(t, none.Error)
	assert.Zero(t, none.RowsAffected)

	raw := ex.ExecRaw(ctx, "UPDATE matched_probe SET [Amount] = [Amount] WHERE [Kind] = ?", "b")
	require.NoError(t, raw.Error)
	assert.Equal(t, int64(1), raw.RowsAffected)
}

// ------------------------------------------------------------------ transactions

// TestSQLServerXactAbortDoomsASwallowedError: without XACT_ABORT, SQL Server fails only the
// statement that raised an error and keeps the transaction, so code that ignores the error and
// carries on commits everything else. Every connection runs SET XACT_ABORT ON, so, as on
// Postgres, the error dooms the transaction: nothing after it can commit, the statements
// before it included.
func TestSQLServerXactAbortDoomsASwallowedError(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"xact_probe"},
		"CREATE TABLE xact_probe ([Id] int PRIMARY KEY, [Status] nvarchar(20) NOT NULL)",
		"INSERT INTO xact_probe VALUES (1, 'before')")

	session := mssqlSession(t, ds)
	ctx := context.Background()

	var swallowed, after error
	err := session.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
		options, err := tx.CountRaw(ctx, "SELECT CAST(@@OPTIONS & 16384 AS bigint)")
		if err != nil {
			return err
		}
		if options != 16384 {
			return fmt.Errorf("XACT_ABORT is off inside the transaction (@@OPTIONS & 16384 = %d)", options)
		}
		if result := tx.Exec(ctx, tx.Query().Update("xact_probe").Set("Status", "during").Eq("Id", 1)); result.Error != nil {
			return result.Error
		}
		swallowed = tx.ExecRaw(ctx, "INSERT INTO xact_probe ([Id], [Status]) VALUES (1, 'duplicate')").Error
		after = tx.ExecRaw(ctx, "INSERT INTO xact_probe ([Id], [Status]) VALUES (2, 'after')").Error
		return nil
	})

	require.Error(t, swallowed, "the duplicate key must fail")
	assert.Equal(t, int32(2627), sqlServerErrorNumber(swallowed))
	require.Error(t, after, "the server rolled the transaction back, so the driver refuses what follows")
	require.Error(t, err, "a transaction an error has doomed must not report a commit")
	t.Logf("the statement after the error: %v; the commit: %v", after, err)

	var rows []string
	require.NoError(t, g.Raw("SELECT CONCAT([Id], ':', [Status]) FROM xact_probe ORDER BY [Id]").Scan(&rows).Error)
	assert.Equal(t, []string{"1:before"}, rows,
		"neither the UPDATE before the error nor the INSERT after it may be committed")
}

// TestSQLServerTransactionRollbackAndPanic: a transaction commits when fn returns nil, rolls
// back when it returns an error, and rolls back when it panics, with the panic carrying on to
// the caller. The pool has one connection, so the check that no transaction is left open runs
// on the connection the panicking one used.
func TestSQLServerTransactionRollbackAndPanic(t *testing.T) {
	cfg := mssqlConfig()
	cfg["properties"] = map[string]any{"maxOpenConnections": 1}
	ds := mssqlDataSource(t, cfg)
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"tx_probe"},
		"CREATE TABLE tx_probe ([Id] int PRIMARY KEY, [Status] nvarchar(20) NOT NULL)",
		"INSERT INTO tx_probe VALUES (1, 'before')")

	session := mssqlSession(t, ds)
	ctx := context.Background()
	status := func() string {
		var s []string
		require.NoError(t, session.Executor().FindRaw(ctx, &s, "SELECT [Status] FROM tx_probe WHERE [Id] = 1").Error)
		require.Len(t, s, 1)
		return s[0]
	}
	set := func(tx dbCore.IDBTransaction, value string) error {
		return tx.Exec(ctx, tx.Query().Update("tx_probe").Set("Status", value).Eq("Id", 1)).Error
	}

	refusal := errors.New("changed my mind")
	err := session.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
		if err := set(tx, "returned an error"); err != nil {
			return err
		}
		return refusal
	})
	require.ErrorIs(t, err, refusal)
	assert.Equal(t, "before", status(), "an error rolls back")

	assert.PanicsWithValue(t, "boom", func() {
		_ = session.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
			require.NoError(t, set(tx, "panicked"))
			panic("boom")
		})
	}, "the panic reaches the caller")
	assert.Equal(t, "before", status(), "a panic rolls back")

	open, err := session.Executor().CountRaw(ctx, "SELECT @@TRANCOUNT")
	require.NoError(t, err)
	assert.Zero(t, open, "the only connection went back to the pool with no transaction open")

	require.NoError(t, session.Transaction(ctx, func(tx dbCore.IDBTransaction) error { return set(tx, "committed") }))
	assert.Equal(t, "committed", status())
}

// ------------------------------------------------------------------ binary values

// TestSQLServerByteSliceFirstValue: gorm expands a slice bound right after "(" into one
// parameter per element, so that IN (?) takes a slice, and a []byte is a slice. The first
// value of a VALUES list is exactly there, so a []byte in that position used to arrive as one
// parameter per byte. The executor binds it whole, on the builder and the raw paths.
func TestSQLServerByteSliceFirstValue(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"bytes_probe"},
		"CREATE TABLE bytes_probe ([Data] varbinary(max) NOT NULL, [Id] int PRIMARY KEY)")

	session := mssqlSession(t, ds)
	ctx := context.Background()

	type rawBytes []byte
	payload := []byte{1, 2, 3, 0, 255}
	require.NoError(t, session.Executor().Exec(ctx,
		session.Query().Insert("bytes_probe").Columns("Data", "Id").Values(payload, 1)).Error)
	require.NoError(t, session.Executor().ExecRaw(ctx,
		"INSERT INTO bytes_probe ([Data], [Id]) VALUES (?, ?)", payload, 2).Error)
	require.NoError(t, session.Executor().ExecRaw(ctx,
		"INSERT INTO bytes_probe ([Data], [Id]) VALUES (?, ?)", rawBytes(payload), 3).Error,
		"a named type over []byte is bound whole too")

	type stored struct {
		ID     int    `gorm:"column:Id"`
		Data   []byte `gorm:"column:Data"`
		Length int    `gorm:"column:Length"`
	}
	var rows []stored
	require.NoError(t, g.Raw("SELECT [Id], [Data], DATALENGTH([Data]) AS [Length] FROM bytes_probe ORDER BY [Id]").
		Scan(&rows).Error)
	require.Len(t, rows, 3)
	for _, row := range rows {
		assert.Equal(t, payload, row.Data, "row %d", row.ID)
		assert.Equal(t, len(payload), row.Length, "row %d", row.ID)
	}

	var found []int
	require.NoError(t, session.Executor().Find(ctx,
		session.Query().Select("Id").From("bytes_probe").Eq("Data", payload).OrderBy("Id", "ASC"), &found).Error)
	assert.Equal(t, []int{1, 2, 3}, found, "a []byte compares as one varbinary value")
}

// TestSQLServerNullVarbinaryRoundTrip: go-mssqldb sends a NULL with no type as nvarchar, which
// a varbinary column refuses (Msg 257). The executor binds a nil *[]byte, and a nil []byte, as
// a varbinary NULL, and the refusal of an untyped nil carries the hint that says so.
func TestSQLServerNullVarbinaryRoundTrip(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"null_bytes_probe"},
		"CREATE TABLE null_bytes_probe ([Id] int PRIMARY KEY, [Data] varbinary(max) NULL)")

	session := mssqlSession(t, ds)
	ex := session.Executor()
	ctx := context.Background()

	var nilPointer *[]byte
	present := []byte{9, 8, 7}
	require.NoError(t, ex.Exec(ctx, session.Query().Insert("null_bytes_probe").Columns("Id", "Data").Values(1, nilPointer)).Error)
	require.NoError(t, ex.ExecRaw(ctx, "INSERT INTO null_bytes_probe ([Id], [Data]) VALUES (?, ?)", 2, nilPointer).Error)
	require.NoError(t, ex.ExecRaw(ctx, "INSERT INTO null_bytes_probe ([Id], [Data]) VALUES (?, ?)", 3, []byte(nil)).Error)
	require.NoError(t, ex.ExecRaw(ctx, "INSERT INTO null_bytes_probe ([Id], [Data]) VALUES (?, ?)", 4, &present).Error)
	require.NoError(t, ex.Exec(ctx, session.Query().Update("null_bytes_probe").Set("Data", nilPointer).Eq("Id", 4)).Error)
	require.NoError(t, ex.ExecRaw(ctx, "UPDATE null_bytes_probe SET [Data] = ? WHERE [Id] = ?", &present, 3).Error)

	type stored struct {
		ID   int     `gorm:"column:Id"`
		Data *[]byte `gorm:"column:Data"`
	}
	var rows []stored
	require.NoError(t, ex.Find(ctx, session.Query().Select("Id", "Data").From("null_bytes_probe").OrderBy("Id", "ASC"), &rows).Error)
	require.Len(t, rows, 4)
	assert.Nil(t, rows[0].Data)
	assert.Nil(t, rows[1].Data)
	require.NotNil(t, rows[2].Data)
	assert.Equal(t, present, *rows[2].Data)
	assert.Nil(t, rows[3].Data)

	untyped := ex.ExecRaw(ctx, "UPDATE null_bytes_probe SET [Data] = ? WHERE [Id] = ?", nil, 1)
	require.Error(t, untyped.Error, "an untyped nil is an nvarchar NULL")
	assert.Equal(t, int32(257), sqlServerErrorNumber(untyped.Error))
	assert.Contains(t, untyped.Error.Error(), "bind it as a []byte")
}

// ------------------------------------------------------------------ DISTINCT and counting

// TestSQLServerArgumentsKeepTheirMeaning: a time and a JSON document go to the server as what
// they mean, not as their Go shape.
//
// go-mssqldb sends a time.Time as a datetimeoffset with its own offset, and SQL Server keeps
// the wall clock and drops the offset converting it to datetime2 or datetime, EF Core's types
// for a DateTime: 10:00 in Zurich was stored as 10:00, read back as 10:00 UTC, and an equality
// lookup with the same time matched nothing. And a json.RawMessage is a []byte, sent as
// varbinary, which an nvarchar(max) column, EF Core's JSON column, took without an error by
// reading the bytes as UTF-16.
func TestSQLServerArgumentsKeepTheirMeaning(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"typed_args_probe"},
		"CREATE TABLE typed_args_probe ([Id] int PRIMARY KEY, [D2] datetime2 NULL, [DT] datetime NULL, "+
			"[DO] datetimeoffset NULL, [J] nvarchar(max) NULL)")

	session := mssqlSession(t, ds)
	ex := session.Executor()
	ctx := context.Background()

	t.Run("time", func(t *testing.T) {
		zurich, err := time.LoadLocation("Europe/Zurich")
		require.NoError(t, err)
		local := time.Date(2026, 9, 27, 10, 0, 0, 0, zurich)

		require.NoError(t, ex.Exec(ctx, session.Query().Insert("typed_args_probe").Columns("Id", "D2", "DT", "DO").
			Values(1, local, local, local)).Error)
		require.NoError(t, ex.ExecRaw(ctx, "INSERT INTO typed_args_probe ([Id], [D2]) VALUES (?, ?)", 2, &local).Error)

		type stored struct {
			D2 time.Time  `gorm:"column:D2"`
			DT *time.Time `gorm:"column:DT"`
			DO *time.Time `gorm:"column:DO"`
		}
		var rows []stored
		require.NoError(t, ex.Find(ctx, session.Query().Select("D2", "DT", "DO").From("typed_args_probe").OrderBy("Id", "ASC"), &rows).Error)
		require.Len(t, rows, 2)
		assert.Truef(t, rows[0].D2.Equal(local), "datetime2 holds the instant: %v", rows[0].D2)
		require.NotNil(t, rows[0].DT)
		assert.Truef(t, rows[0].DT.Equal(local), "and so does datetime: %v", *rows[0].DT)
		require.NotNil(t, rows[0].DO)
		assert.Truef(t, rows[0].DO.Equal(local), "and datetimeoffset: %v", *rows[0].DO)
		assert.Truef(t, rows[1].D2.Equal(local), "a *time.Time too: %v", rows[1].D2)

		var wall string
		require.NoError(t, g.Raw("SELECT CONVERT(varchar(19), [D2], 120) FROM typed_args_probe WHERE [Id] = 1").Scan(&wall).Error)
		assert.Equal(t, "2026-09-27 08:00:00", wall, "stored as UTC wall time")

		n, err := ex.Count(ctx, session.Query().Select("COUNT(*)").From("typed_args_probe").Eq("D2", local))
		require.NoError(t, err)
		assert.Equal(t, int64(2), n, "an equality lookup with the same time finds both rows")
	})

	t.Run("json", func(t *testing.T) {
		document := json.RawMessage(`{"a":1,"name":"Größe"}`)
		require.NoError(t, ex.Exec(ctx, session.Query().Insert("typed_args_probe").Columns("Id", "J").Values(3, document)).Error)
		require.NoError(t, ex.ExecRaw(ctx, "UPDATE typed_args_probe SET [J] = ? WHERE [Id] = ?", document, 1).Error)

		var stored []string
		require.NoError(t, g.Raw("SELECT [J] FROM typed_args_probe WHERE [Id] IN (1, 3) ORDER BY [Id]").Scan(&stored).Error)
		assert.Equal(t, []string{string(document), string(document)}, stored)
		var name string
		require.NoError(t, g.Raw("SELECT JSON_VALUE([J], '$.name') FROM typed_args_probe WHERE [Id] = 3").Scan(&name).Error)
		assert.Equal(t, "Größe", name, "the server reads it as JSON")
	})
}

// TestSQLServerDistinctWithSelectNullOrderBy decides the dialect's DISTINCT refusal. OFFSET
// needs an ORDER BY, and the dialect supplies ORDER BY (SELECT NULL) when a query has none, but
// SQL Server refuses that under DISTINCT (Msg 145: ORDER BY items must appear in the select
// list). So a DISTINCT query with an Offset and no ORDER BY is refused before it is sent, with
// the fix in the hint, and the same query ordered by a selected column runs. DISTINCT written
// into the first select item, which is how an app asks for it, counts as DISTINCT: it is also
// what has to come before TOP.
func TestSQLServerDistinctWithSelectNullOrderBy(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"distinct_probe"},
		"CREATE TABLE distinct_probe ([Id] int PRIMARY KEY, [Region] nvarchar(10) NOT NULL)",
		"INSERT INTO distinct_probe VALUES (1, 'north'), (2, 'north'), (3, 'south'), (4, 'east')")

	session := mssqlSession(t, ds)
	ex := session.Executor()
	ctx := context.Background()

	var regions []string
	err := ex.FindRaw(ctx, &regions, "SELECT DISTINCT [Region] FROM distinct_probe ORDER BY (SELECT NULL) OFFSET 1 ROWS").Error
	require.Error(t, err, "the server refuses the synthetic ORDER BY under DISTINCT")
	assert.Equal(t, int32(145), sqlServerErrorNumber(err))

	unordered := session.Query().Select("DISTINCT Region").From("distinct_probe").Offset(1).Limit(2)
	_, _, err = unordered.ToSQL()
	requireUnsupported(t, err, "DISTINCT with an Offset and no ORDER BY")
	assert.Contains(t, err.Error(), "order the query by the selected columns")
	requireUnsupported(t, ex.Find(ctx, unordered, &regions).Error, "DISTINCT with an Offset and no ORDER BY")

	ordered := session.Query().Select("DISTINCT Region").From("distinct_probe").OrderBy("Region", "ASC").Offset(1).Limit(2)
	sql, _, err := ordered.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "SELECT DISTINCT [Region] FROM [distinct_probe] ORDER BY [Region] ASC OFFSET 1 ROWS")
	regions = nil
	require.NoError(t, ex.Find(ctx, ordered, &regions).Error)
	assert.Equal(t, []string{"north", "south"}, regions)

	top := session.Query().Select("DISTINCT Region").From("distinct_probe").OrderBy("Region", "ASC").Limit(2)
	sql, _, err = top.ToSQL()
	require.NoError(t, err)
	assert.Contains(t, sql, "SELECT DISTINCT TOP (2) [Region]", "T-SQL puts DISTINCT before TOP")
	regions = nil
	require.NoError(t, ex.Find(ctx, top, &regions).Error)
	assert.Equal(t, []string{"east", "north"}, regions)

	parenthesised := session.Query().Select("DISTINCT(Region)").From("distinct_probe").Limit(10)
	regions = nil
	require.NoError(t, ex.Find(ctx, parenthesised, &regions).Error)
	assert.ElementsMatch(t, []string{"north", "south", "east"}, regions)
}

// TestSQLServerCountByQueryShapes runs CountByQuery's wrapped counts on SQL Server, which is
// stricter about derived tables than the other engines: every column needs a name (Msg 8155)
// and no ORDER BY may go without TOP (Msg 1033). The shapes are the ones the other engines run
// (see countByQueryShapes); the two that GROUP BY a select-list alias or position are MySQL and
// Postgres spellings: SQL Server answers the alias with its own error, and the dialect refuses
// the position before it is sent, and both are asserted.
func TestSQLServerCountByQueryShapes(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	assertCountByQueryShapes(t, ormEngine{name: "sqlserver", ds: ds})
}

// ------------------------------------------------------------------ read_only and external_schema

// TestSQLServerReadOnlyRefusesWritesThroughEveryPath: read_only is enforced by gorgany on every
// path a write can take, before it is sent, while the database itself still takes writes. So
// the refusals below are gorgany's, and reads, paged ones included, still run.
func TestSQLServerReadOnlyRefusesWritesThroughEveryPath(t *testing.T) {
	owner := mssqlDataSource(t, mssqlConfig())
	ownerGorm := gormOf(t, owner)
	resetMSSQLTables(t, ownerGorm, []string{"ro_probe"},
		"CREATE TABLE ro_probe ([Id] bigint IDENTITY(1, 1) PRIMARY KEY, [Name] nvarchar(20) NOT NULL)",
		"INSERT INTO ro_probe ([Name]) VALUES ('a'), ('b'), ('c')")

	var updateability string
	require.NoError(t, ownerGorm.Raw("SELECT CAST(DATABASEPROPERTYEX(DB_NAME(), 'Updateability') AS nvarchar(20))").
		Scan(&updateability).Error)
	require.Equal(t, "READ_WRITE", updateability, "the database takes writes, so a refusal is gorgany's")

	cfg := mssqlConfig()
	cfg["read_only"] = true
	readOnly, err := sqlserverv2.NewDataSource(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, readOnly.Close()) })
	assert.True(t, dbCore.IsReadOnly(readOnly))

	session := mssqlSession(t, readOnly)
	ex := session.Executor()
	ctx := context.Background()

	insert := session.Query().Insert("ro_probe").Columns("Name").Values("x")
	_, _, err = insert.ToSQL()
	assert.ErrorIs(t, err, dbCore.ErrReadOnly, "the builder refuses the write where it is built")
	assert.ErrorIs(t, ex.Exec(ctx, insert).Error, dbCore.ErrReadOnly)
	assert.ErrorIs(t, ex.Exec(ctx, session.Query().Update("ro_probe").Set("Name", "x").Eq("Id", 1)).Error, dbCore.ErrReadOnly)
	assert.ErrorIs(t, ex.ExecRaw(ctx, "DELETE FROM ro_probe WHERE [Id] = ?", 1).Error, dbCore.ErrReadOnly)

	driver, err := readOnly.GetDriver()
	require.NoError(t, err)
	assert.ErrorIs(t, driver.(*gorm.DB).Exec("UPDATE ro_probe SET [Name] = 'x' WHERE [Id] = 1").Error, dbCore.ErrReadOnly)
	assert.ErrorIs(t, driver.(*gorm.DB).Table("ro_probe").Create(map[string]any{"Name": "x"}).Error, dbCore.ErrReadOnly,
		"gorm's own Create on the handle GetDriver returns is refused as well")

	type roProbe struct {
		orm.BaseEntity
		ID   int64  `gorm:"column:Id;primaryKey;autoIncrement"`
		Name string `gorm:"column:Name"`
	}
	assert.ErrorIs(t, orm.New[*roProbe](session).Create(&roProbe{Name: "x"}), dbCore.ErrReadOnly)

	err = session.Transaction(ctx, func(tx dbCore.IDBTransaction) error {
		return tx.ExecRaw(ctx, "INSERT INTO ro_probe ([Name]) VALUES ('x')").Error
	})
	assert.ErrorIs(t, err, dbCore.ErrReadOnly, "a transaction's statements are guarded too")

	var page []int64
	require.NoError(t, ex.Find(ctx, session.Query().Select("Id").From("ro_probe").OrderBy("Id", "ASC").Offset(1).Limit(1), &page).Error,
		"a paged read, whose FETCH NEXT is not a write, still runs")
	assert.Equal(t, []int64{2}, page)
	n, err := ex.CountRaw(ctx, "SELECT COUNT(*) FROM ro_probe")
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "and nothing was written")
}

// TestExternalSchemaRefusesMigrateSeedAndDiffOnSQLServer runs db:migrate, db:seed and db:diff
// against an external_schema datasource on SQL Server, where the schema gorgany is pointed at
// is typically EF Core's. Each is refused as a configuration error before it sends a
// statement, with core.ErrExternalSchema, and not with db:diff's refusal of a dialect that
// commits DDL, which SQL Server is not. EF Core's own history table is left as it was, and no
// migrations or seeders table appears beside it.
func TestExternalSchemaRefusesMigrateSeedAndDiffOnSQLServer(t *testing.T) {
	owner := mssqlDataSource(t, mssqlConfig())
	ownerGorm := gormOf(t, owner)

	const history = "[__EFMigrationsHistory]"
	resetMSSQLTables(t, ownerGorm, []string{history},
		"CREATE TABLE "+history+" ([MigrationId] nvarchar(150) NOT NULL PRIMARY KEY, [ProductVersion] nvarchar(32) NOT NULL)",
		"INSERT INTO "+history+" VALUES ('20240101000000_Initial', '8.0.0')")
	historyColumns := describeTable(t, ownerGorm, "__EFMigrationsHistory")

	assertDbCommandsRefuseAnExternalSchema(t, "mssql", ownerGorm)

	// The refusal the children print is this error, and it is the sentinel an app can test.
	legacy := mssqlConfig()
	legacy["external_schema"] = true
	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{"default": mssqlConfig(), "legacy": legacy})
	t.Cleanup(func() { viper.Set("databases", previous) })

	c := service.NewContainer()
	p := provider.NewDbProvider()
	p.Register(c)
	p.Boot(c)
	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))
	t.Cleanup(func() {
		assert.NoError(t, dbContext.GetDataSource("default").Close())
		assert.NoError(t, dbContext.GetDataSource("legacy").Close())
	})

	for _, command := range []string{"db:migrate", "db:seed", "db:diff"} {
		err := dbCmd.RequireOwnedDatasource(dbContext, "legacy", command)
		require.ErrorIs(t, err, dbCore.ErrExternalSchema, command)
		assert.NotContains(t, err.Error(), "commits DDL immediately", command)
	}
	require.NoError(t, dbCmd.RequireOwnedDatasource(dbContext, "default", "db:migrate"))

	assert.Equal(t, historyColumns, describeTable(t, ownerGorm, "__EFMigrationsHistory"), "EF Core's history keeps its columns")
	var migrations []string
	require.NoError(t, ownerGorm.Raw("SELECT [MigrationId] FROM "+history).Scan(&migrations).Error)
	assert.Equal(t, []string{"20240101000000_Initial"}, migrations, "and its rows")
	for _, table := range []string{"migrations", "seeders", "sessions"} {
		assert.Falsef(t, tableInInformationSchema(t, ownerGorm, table), "booting the provider creates no %s table", table)
	}
}

// extCommentProbe maps the owner's table with a column comment that differs from the one the
// owner set, which gorm's SQL Server migrator writes with sp_updateextendedproperty.
type extCommentProbe struct {
	ID   int64  `gorm:"column:Id;primaryKey;autoIncrement"`
	Note string `gorm:"column:Note;size:100;comment:rewritten by gorgany"`
}

func (extCommentProbe) TableName() string { return "ext_comment_probe" }

// TestSQLServerExternalSchemaRefusesAutoMigrateCommentChange: on SQL Server a column comment is
// an extended property, MS_Description, which EF Core and SSMS both write, and gorm's
// AutoMigrate rewrites one that differs from the model's with a stored procedure rather than
// ALTER. The external_schema guard knows that procedure, so AutoMigrate on the driver an app
// can reach is refused and the owner's description stays.
func TestSQLServerExternalSchemaRefusesAutoMigrateCommentChange(t *testing.T) {
	owner := mssqlDataSource(t, mssqlConfig())
	ownerGorm := gormOf(t, owner)
	resetMSSQLTables(t, ownerGorm, []string{"ext_comment_probe"},
		"CREATE TABLE ext_comment_probe ([Id] bigint IDENTITY(1, 1) PRIMARY KEY, [Note] nvarchar(100) NULL)",
		"EXEC sp_addextendedproperty @name = N'MS_Description', @value = N'set by the schema owner', "+
			"@level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'ext_comment_probe', "+
			"@level2type = N'COLUMN', @level2name = N'Note'")
	description := func() string {
		var value string
		require.NoError(t, ownerGorm.Raw("SELECT CAST(value AS nvarchar(200)) FROM sys.fn_listextendedproperty("+
			"N'MS_Description', N'SCHEMA', N'dbo', N'TABLE', N'ext_comment_probe', N'COLUMN', N'Note')").Scan(&value).Error)
		return value
	}
	require.Equal(t, "set by the schema owner", description())
	columns := describeTable(t, ownerGorm, "ext_comment_probe")

	cfg := mssqlConfig()
	cfg["external_schema"] = true
	external, err := sqlserverv2.NewDataSource(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, external.Close()) })

	driver, err := external.GetDriver()
	require.NoError(t, err)
	err = driver.(*gorm.DB).AutoMigrate(&extCommentProbe{})
	require.ErrorIs(t, err, dbCore.ErrExternalSchema, "AutoMigrate must not rewrite the owner's schema")
	t.Logf("AutoMigrate refused as: %v", err)

	assert.Equal(t, "set by the schema owner", description(), "the owner's description is unchanged")
	assert.Equal(t, columns, describeTable(t, ownerGorm, "ext_comment_probe"), "and so are the columns")
}

// TestSQLServerExternalSchemaRefusesDBCCAndSecurityProcedures: T-SQL changes an owner's
// schema and its security through commands and procedures as well as through DDL. DBCC
// CHECKIDENT … RESEED moves an EF table's IDENTITY for good, sp_settriggerorder rearranges its
// triggers and sp_addrolemember hands out a role, each the procedural twin of a statement the
// guard already refuses. Through an external_schema datasource each is refused before it is
// sent, and the owner's table keeps its IDENTITY.
func TestSQLServerExternalSchemaRefusesDBCCAndSecurityProcedures(t *testing.T) {
	owner := mssqlDataSource(t, mssqlConfig())
	ownerGorm := gormOf(t, owner)
	resetMSSQLTables(t, ownerGorm, []string{"ext_identity_probe"},
		"CREATE TABLE ext_identity_probe ([Id] int IDENTITY(1, 1) PRIMARY KEY, [Note] nvarchar(20) NULL)",
		"INSERT INTO ext_identity_probe ([Note]) VALUES ('a'), ('b')")
	identity := func() int64 {
		var current int64
		require.NoError(t, ownerGorm.Raw("SELECT CAST(IDENT_CURRENT('dbo.ext_identity_probe') AS bigint)").Scan(&current).Error)
		return current
	}
	require.Equal(t, int64(2), identity())

	cfg := mssqlConfig()
	cfg["external_schema"] = true
	external := mssqlDataSource(t, cfg)
	ex := mssqlSession(t, external).Executor()
	ctx := context.Background()

	for _, statement := range []string{
		"DBCC CHECKIDENT ('dbo.ext_identity_probe', RESEED, 1000)",
		"EXEC sp_settriggerorder @triggername = 'dbo.no_such_trigger', @order = 'First', @stmttype = 'UPDATE'",
		"EXEC sp_addrolemember 'db_datareader', 'user@example.com'",
	} {
		result := ex.ExecRaw(ctx, statement)
		require.ErrorIsf(t, result.Error, dbCore.ErrExternalSchema, "%s must be refused before it is sent", statement)
		assert.Zero(t, sqlServerErrorNumber(result.Error), "the refusal is gorgany's, not the server's")
	}
	assert.Equal(t, int64(2), identity(), "the owner's IDENTITY is where it was")

	// Rows are still the app's to write.
	require.NoError(t, ex.ExecRaw(ctx, "INSERT INTO ext_identity_probe ([Note]) VALUES (?)", "c").Error)
	assert.Equal(t, int64(3), identity())
}

// ------------------------------------------------------------------ db:migrate and db:seed

// TestMigrateUpRecordsOnlyWhatCommitsOnSQLServer is TestMigrateUpRecordsOnlyWhatCommitsOnPostgres
// on SQL Server, whose DDL is transactional as Postgres's is. A migration that swallows an
// error cannot commit, since XACT_ABORT has already rolled its transaction back, and a
// bookkeeping row that cannot be written rolls the migration's table back with it.
func TestMigrateUpRecordsOnlyWhatCommitsOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	assertMigrateUpRecordsOnlyWhatCommits(t, gormOf(t, ds), "mssql-succeeds",
		"mssql-transaction-aborted", "mssql-record-fails")
}

// TestSeedCommitsEachSeederWithItsRecordOnSQLServer is the db:seed counterpart.
func TestSeedCommitsEachSeederWithItsRecordOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	gormDb := gormOf(t, ds)

	for _, scenario := range []string{"mssql-succeeds", "mssql-one-connection"} {
		t.Run(scenario, func(t *testing.T) {
			prepareSeedScenario(t, gormDb, scenario)

			code, output := runSeedInChild(t, scenario)
			require.Equal(t, 0, code, "the seeder must succeed:\n%s", output)
			assert.Equal(t, []string{"one", "two"}, probeLabels(t, gormDb))
			assert.True(t, isSeeded(t, gormDb, "probe_seeds"))

			code, output = runSeedInChild(t, scenario)
			require.Equal(t, 0, code, "a second run must succeed:\n%s", output)
			assert.Equal(t, []string{"one", "two"}, probeLabels(t, gormDb))
		})
	}

	for _, scenario := range []string{"mssql-save-fails", "mssql-record-fails"} {
		t.Run(scenario, func(t *testing.T) {
			assertSeedFailedCleanly(t, gormDb, scenario)
		})
	}
}
