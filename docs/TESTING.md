# Testing an app against a real database

`db/sql/core.ISession` has a `Transaction` method, so a transaction-per-test seam was
always *expressible* — but nothing shipped to use it, so every consumer hand-rolled the
same harness: read connection settings from somewhere, wait for the engine, run
migrations, clean between tests, skip when no server is around. v2.0's own
`e2e/tests/live_db_test.go` was one more copy of it.

`testsupport` is that code, generalised.

## Quick start

```go
func TestMain(m *testing.M) {
    testsupport.AddMigration(migrations.All()...)
    testsupport.Main(m)
}

func TestSavingAWidget(t *testing.T) {
    db := testsupport.RequireDatabase(t)

    widget := &Widget{Label: "first"}
    require.NoError(t, orm.Save(db.Session(), widget))

    assert.Equal(t, 1, db.CountRows(t, "widgets"))
}
```

Start an engine:

```bash
docker run --rm -d --name gorgany-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=gorgany_test -p 5433:5432 postgres:16-alpine
```

That is the default the harness assumes, so no configuration is needed for it.

```bash
docker run --rm -d --name gorgany-mysql -e MYSQL_ROOT_PASSWORD=test -e MYSQL_DATABASE=gorgany_test -p 3307:3306 mysql:8 --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci
```

```bash
docker run --rm -d --platform linux/amd64 --name gorgany-mssql -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD=Gorgany-Test-1 -e MSSQL_PID=Developer -p 14330:1433 mcr.microsoft.com/mssql/server:2022-CU27-ubuntu-22.04
```

The SQL Server image is amd64-only and about 1.5 GB. On an Apple Silicon Mac it runs under
Rosetta, once Docker Desktop's "Use Rosetta for x86_64/amd64 emulation on Apple Silicon" setting
is on; under Docker's default emulation it does not start. Its first start upgrades the system
databases and takes a minute or so, which `GORGANY_TEST_ENGINE_WAIT` may need to cover. The
password is not the other engines' `test` because SQL Server refuses one below its complexity
policy, and the container then exits. The image cannot create a database from its environment
either, so the harness creates `gorgany_test` itself; see [SQL Server](#sql-server).

The MySQL and SQL Server containers need `GORGANY_TEST_DRIVER` set (see
[Configuration](#configuration)); the port, user, password and SSL defaults follow it.

## Skip or fail

| Function | No engine reachable |
|----------|--------------------|
| `RequireDatabase(t)` | **skips** the test |
| `MustDatabase(t)` | **fails** the test |

`RequireDatabase` is right for a developer's machine: a suite that fails because nothing is
started is a suite people learn to ignore. `MustDatabase` is right for CI, where an absent
engine means the pipeline is misconfigured and skipping would hide it.

A *bad config* always fails, whichever you use. Skipping would report a typo in an
environment variable as "no engine available", which sends people to look at Docker
instead of at their config.

A *failing migration* always fails too: the engine is there and the schema is wrong.

So does a *refused target* (see [Which databases the harness refuses](#which-databases-the-harness-refuses)),
and a driver that is not registered in the test binary. Up to and including v2.4.3, an
unregistered driver was retried for the whole engine wait and then skipped, as though the
engine were not running.

## Isolation

```go
testsupport.Configure(testsupport.Config{Isolation: testsupport.IsolateByRollback})
```

| Strategy | How | When |
|----------|-----|------|
| `IsolateByTruncation` (default) | Empties every migrated table after each test | Always works |
| `IsolateByRollback` | Runs the test in a transaction and rolls it back | Faster, but the code under test must use `db.Session()` |

Truncation is the default because it is the one that cannot silently mislead. Under
rollback isolation, anything that opens its own connection — a goroutine, a service that
resolves its own session — will not see the test's uncommitted rows, and that looks like a
bug in the code rather than in the harness.

Truncation also runs **before** the first test as well as after each one. A previous run
that was interrupted (a panic, a `^C`, a `go test -timeout` kill) leaves rows behind, and a
test that fails because of the *last* run's data is the worst kind to debug.

Three details of truncation that matter:

- Tables are emptied in reverse creation order, because the migrations create parents
  before children and deleting forwards hits the foreign key. The harness records which
  tables each migration created, so it knows the order.
- On MySQL, `TRUNCATE` has no `CASCADE`, so foreign key checks are suspended for the
  duration — and restored even if a truncate fails. Leaving them off would make every later
  test in the process pass without referential integrity.
- On SQL Server there is neither, so the harness switches each table's constraints off,
  deletes the rows, reseeds the identities and switches the constraints back on. See
  [How SQL Server tables are emptied](#how-sql-server-tables-are-emptied).

Under rollback isolation, `db.Gorm()` is **not** inside the transaction: gorm's transaction
is a separate handle the session does not expose. Use `db.Exec`, `db.CountRows` or
`db.Session()` to read back what the test wrote; `db.Gorm()` is for checking what actually
committed.

## Testing every engine

```go
func TestWidgetsOnEveryEngine(t *testing.T) {
    testsupport.EachDatabase(t, func(t *testing.T, db *testsupport.Database) {
        // runs once per configured engine, as a subtest named after it
        if db.IsSQLServer() {
            // a dialect-specific assertion
        }
    })
}
```

```go
testsupport.Configure(testsupport.Config{
    Databases: []testsupport.DatabaseConfig{
        {Name: "postgres", Driver: testsupport.DriverPostgres, Host: "127.0.0.1", Port: 5433,
         User: "postgres", Password: "test", Database: "gorgany_test"},
        {Name: "mysql", Driver: testsupport.DriverMySQL, Host: "127.0.0.1", Port: 3307,
         User: "root", Password: "test", Database: "gorgany_test"},
        {Name: "sqlserver", Driver: testsupport.DriverSQLServer, Host: "127.0.0.1", Port: 14330,
         User: "sa", Password: "Gorgany-Test-1", Database: "gorgany_test"},
    },
})
```

An engine that is not reachable skips its own subtest rather than the whole set, so a laptop
with only Postgres running still gets Postgres coverage.

The SQL Server entry needs the engine's driver in the test binary, which testsupport does not
link; see [SQL Server](#sql-server).

## Configuration

Everything is settable by environment variable, so one test file covers every engine in CI
without a code change:

| Variable | Default |
|----------|---------|
| `GORGANY_TEST_DRIVER` | `postgres_gorm` (or `mysql_gorm`, `sqlserver_gorm`) |
| `GORGANY_TEST_HOST` | `127.0.0.1` |
| `GORGANY_TEST_PORT` | `5433` (`3307` for MySQL, `14330` for SQL Server) |
| `GORGANY_TEST_USER` | `postgres` (`root` for MySQL, `sa` for SQL Server) |
| `GORGANY_TEST_PASSWORD` | `test` (`Gorgany-Test-1` for SQL Server) |
| `GORGANY_TEST_DB` | `gorgany_test` |
| `GORGANY_TEST_SSL` | `disable` (`true` for SQL Server) |
| `GORGANY_TEST_TRUST_SERVER_CERT` | unset: SQL Server trusts the server's certificate, except on an Azure SQL host or with `GORGANY_TEST_SSL=strict` |
| `GORGANY_TEST_ISOLATION` | `truncate` |
| `GORGANY_TEST_ENGINE_WAIT` | `30s` |
| `GORGANY_TEST_KEEP_DATA` | unset |
| `GORGANY_TEST_MIGRATE_DOWN` | unset |
| `GORGANY_TEST_ALLOW_ANY_TARGET` | unset |

The defaults in brackets follow `GORGANY_TEST_DRIVER`, to the containers in
[Quick start](#quick-start), so switching engines is one variable. SQL Server's SSL default is
not Postgres's `disable` because on SQL Server that turns encryption off. On SQL Server,
`GORGANY_TEST_SSL` takes `true`, `strict`, `false` or `disable` (or `mandatory`, `yes`, `1`,
`optional`, `no` and `0`); any other value, such as a Postgres `require` still exported after
switching drivers, fails the test as a bad config instead of being retried for the engine wait.

This holds for `Main`, `RequireDatabase` and `MustDatabase`, and for any harness from
`Configure` or `New`. A field set in code keeps its value, and a field left zero takes its
variable, then the default above. So `Configure(Config{Isolation: IsolateByRollback})`
still honours `GORGANY_TEST_ENGINE_WAIT`, while a `Databases` list set in code ignores the
connection variables. `KeepData`, `MigrateDown` and `AllowAnyTarget` are booleans and
cannot be "unset", so their variables can switch them on for any harness but cannot switch
them off.

An explicitly empty `GORGANY_TEST_PASSWORD=` is a real value, not an absence — substituting
the default for it would silently connect as something else. An empty
`GORGANY_TEST_ISOLATION=` or `GORGANY_TEST_ENGINE_WAIT=` means the default. An unknown
isolation fails the test, like any other bad config.

`GORGANY_TEST_ENGINE_WAIT` exists because a container started in the same CI step is
usually not accepting connections yet, and a suite that gives up on the first refused dial
is a suite that fails intermittently. The harness also makes a real round trip before
declaring the engine up: a lazily-connecting driver reports success and fails on first
query.

`GORGANY_TEST_KEEP_DATA=1` leaves rows in place so you can inspect them after a failure.
Tests will interfere with each other; it is for debugging by hand.

`GORGANY_TEST_MIGRATE_DOWN=1` runs every migration's `Down` before `Up`, so a schema left
behind by an interrupted run does not poison the next one.

`GORGANY_TEST_ALLOW_ANY_TARGET=1` switches off the target guard below. Set it only for a
database you know is disposable.

`GORGANY_TEST_TRUST_SERVER_CERT` is SQL Server's `options.trust_server_certificate`, and
`DatabaseConfig.TrustServerCertificate` in code; Postgres and MySQL ignore it. Unset, the
harness trusts the certificate, because a SQL Server container generates a self-signed one when
it starts; the connection is still encrypted, only the certificate chain goes unchecked. It
does not trust one from an Azure SQL host, whose certificate always verifies, or with
`GORGANY_TEST_SSL=strict`, which verifies it by definition, since the engine refuses both.
Set it to `false` for a server whose certificate a trusted authority signed. A value that is
not `true` or `false` fails the test as a bad config.

## SQL Server

testsupport registers the Postgres and MySQL drivers itself, through `driver/builtin`. SQL
Server's is a separate import, so that an app that never speaks SQL Server does not link its
driver into every test binary. A suite that tests against SQL Server makes it, in the test
package or in the `pkg/provider` package the tests import:

```go
import _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"
```

Without it the harness fails the test with an error naming that import.

### The test database is created for you

The SQL Server image cannot create a database from its environment, as the Postgres and MySQL
images do, so the harness creates it. Before it connects, it connects to `master` with the same
server, credentials and TLS settings and runs:

```sql
IF DB_ID(N'gorgany_test') IS NULL BEGIN
    CREATE DATABASE [gorgany_test];
    ALTER DATABASE [gorgany_test] SET READ_COMMITTED_SNAPSHOT ON;
END
```

It does this inside the engine wait, since a server that has just started accepts logins before
it can create a database. A login that may not create databases can still use one somebody else
created: if the creation fails and the database is there, the harness connects to it.

- It creates only a database named with letters, digits and underscores, since `CREATE
  DATABASE` cannot take the name as a parameter. A database with another name, such as
  `e2e-testing`, works once you have created it yourself. Until then, when the harness cannot
  connect to it, it asks `master` whether the database exists, and fails the test at once when
  it does not, rather than retrying for the engine wait and skipping as though the server were
  down.
- A database it creates gets `READ_COMMITTED_SNAPSHOT ON`, the setting every Azure SQL database
  has and an on-premises one lacks by default. With it a read sees the last committed version
  of a row instead of waiting for the transaction that holds it, which is how Postgres and
  MySQL read too. Without it, a test under rollback isolation that reads a table through
  another connection, as `db.Gorm()` is, waits for the test's own transaction, which ends only
  when the test does.
- A database that already exists is left as it is, snapshot setting included: how it reads is
  its owner's decision, and switching the setting needs every other connection to it gone.
- On an Azure SQL host, which the target guard refuses unless it is switched off, nothing is
  created: creating a database there provisions a billable one.

`master` is a system database, which the [target guard](#which-databases-the-harness-refuses)
refuses. The harness never runs a test there or empties it; it runs the one statement above, for
a database name the guard has accepted, which is why that connection does not pass through the
guard.

### How SQL Server tables are emptied

SQL Server has no `TRUNCATE ... CASCADE`, and `TRUNCATE TABLE` refuses any table a foreign key
references, even a disabled one. Nor can foreign key checks be suspended for a session, as on
MySQL: switching a table's constraints off changes the table, for every connection, until
something switches them back on. So the harness empties the tables in four steps:

1. `ALTER TABLE [t] NOCHECK CONSTRAINT ALL` on each table, so rows can go in any order.
2. `DELETE FROM [t]`, in reverse creation order.
3. `DBCC CHECKIDENT (N'[t]', RESEED, n) WITH NO_INFOMSGS` for each table whose identity has
   issued a value, `n` being the seed less the increment, 0 for the usual `IDENTITY(1,1)`, so
   the next row gets the seed, as `RESTART IDENTITY` does on Postgres. A table without an
   identity is skipped, since `DBCC CHECKIDENT` refuses it (Msg 7997), and so is one whose
   identity has never issued a value, which would hand the next row the reseed value itself, 0.
4. `ALTER TABLE [t] WITH CHECK CHECK CONSTRAINT ALL` on each table from step 1, whatever
   happened in between. `WITH CHECK` marks the foreign keys trusted again, which the optimizer
   relies on. A restore that fails is reported, even beside an earlier failure, because the
   constraints stay off until one succeeds; the next truncation that gets through restores
   them.

The tables are looked up in `sys.tables` first, so a view among the migrations' tables, which
gorm lists with them on SQL Server, is left alone; a view or a missing table named in
`db.Truncate(t, ...)` is an error, as it is on Postgres and MySQL. A constraint a migration
created disabled comes back enabled.

Truncations on one engine run one at a time. Restoring a child's foreign key and deleting from
its parent take the two tables in opposite orders, so two tests truncating at once, under
`t.Parallel`, could deadlock.

## Which databases the harness refuses

The harness empties every table its migrations create, before the first test and after each
one, so a stray `GORGANY_TEST_HOST` is all it takes to empty a database somebody needed. It
therefore refuses, for `Databases` set in code as well as for the one from the environment:

| Target | Why |
|--------|-----|
| An Azure SQL host, such as `example.database.windows.net`, or an Azure Database for PostgreSQL or MySQL host, such as `example.postgres.database.azure.com`, on any driver | A cloud database is never the disposable one a harness is for |
| A driver other than `postgres_gorm`, `mysql_gorm` and `sqlserver_gorm` | Isolation has to know how to empty the engine's tables. Up to and including v2.4.3, any driver that was not Postgres was emptied as though it were MySQL |
| A `sqlserver_gorm` database without `test`, `tests` or `testing` as a word of its name, or a system database (`master`, `model`, `msdb`, `tempdb`). `gorgany_test`, `AppTests` and `e2e-testing` pass; `LatestOrders` and `Attestations` do not | One SQL Server instance commonly hosts databases that are not disposable. Postgres and MySQL suites keep the names they had |

A refused target fails the test, as a bad config does. `GORGANY_TEST_ALLOW_ANY_TARGET=1`, or
`Config{AllowAnyTarget: true}`, switches all three off.

Whatever that setting says, the driver must be registered in the test binary. testsupport
registers Postgres and MySQL itself, through `driver/builtin`; SQL Server's driver is an import
of the suite's own (see [SQL Server](#sql-server)), and the error names it.

## What `Database` gives you

| Member | Purpose |
|--------|---------|
| `Session()` | The session to use. Required under rollback isolation |
| `Tx()` | The test's transaction under rollback isolation, else nil |
| `DataSource()` | For a test that opens its own session |
| `Gorm()` | Raw `*gorm.DB`, for assertions the ORM cannot express |
| `Driver()`, `IsPostgres()`, `IsMySQL()`, `IsSQLServer()` | Which engine, for dialect-specific assertions |
| `Tables()` | The tables the migrations created |
| `CountRows(t, table)` | Row count, failing the test on error |
| `Exec(t, sql, args...)` | Run a statement, failing the test on error |
| `Truncate(t, tables...)` | A clean slate part-way through a test |

Connecting and migrating happen **once per package**, not once per test. That is the
difference between a suite that runs in seconds and one nobody runs.

## The harness's own tests

`testsupport/live_test.go` is behind the `livedb` build tag and runs against real Postgres,
MySQL and SQL Server, the three containers in [Quick start](#quick-start):

```bash
go test -tags=livedb ./testsupport/ -count=1 -v
```

It finds the engines where the framework's live suite below does, through `E2E_PG_HOST`,
`E2E_PG_PORT`, `E2E_MYSQL_HOST`, `E2E_MYSQL_PORT`, `E2E_MSSQL_HOST` and `E2E_MSSQL_PORT`, and
otherwise at `127.0.0.1` on the harness's default ports, so one set of variables points both
suites at containers on other ports. An engine that is not running skips its own subtests. It
blank-imports SQL Server's driver itself, as an app's suite does; the import is in a test file
behind the tag, so testsupport's own imports stay free of the engine.

It exists because a harness nobody has run against a real engine is exactly the failure
mode this framework has hit before: the MySQL driver shipped in v2.0 with a green suite
while being unable to insert anything. The rollback-isolation bug in the first version of
this package — helper writes went to the non-transactional handle, so rollback isolation
silently did nothing — was caught by that test and by nothing else.

### The framework's live suite

`e2e/tests` holds the framework's own live cases, behind the same `livedb` tag. It runs against
the three containers in [Quick start](#quick-start). The SQL Server cases create their
`gorgany_test` database through `master` themselves, as the harness does.

```bash
E2E_REQUIRE_SQLSERVER=1 go test -tags=livedb ./e2e/tests/ -run SQLServer -count=1 -v
```

`-run SQLServer` selects the cases named for SQL Server, not the `sqlserver` subtests of the
cases that run on every engine. The whole suite, as the CI live-db job runs it, leaves out every
case that compiles without the tag:

```bash
fixture_cases=$(go test -list . ./e2e/tests | grep '^Test' | paste -sd '|' -)
E2E_REQUIRE_LIVE=1 E2E_REQUIRE_SQLSERVER=1 go test -tags=livedb ./e2e/tests/ -count=1 -v -skip "^(${fixture_cases})\$"
```

Those are the cases in `e2e_test.go`, which need the fixture app that `sh e2e/run.sh` starts,
and the SQL Server gate's own tests in `sqlserver_gate_test.go`, which need no engine and skip
subtests on purpose. `sh e2e/run.sh` runs both, and `go test ./...` the gate's.

| Variable | Effect |
|----------|--------|
| `E2E_REQUIRE_LIVE=1` | A Postgres or MySQL case that cannot reach its engine fails instead of skipping, and a run in which no live case executed fails |
| `E2E_REQUIRE_SQLSERVER=1` | The same for the SQL Server cases, which otherwise skip, after one wait for the whole run, when SQL Server is not reachable |
| `E2E_PG_HOST`, `E2E_PG_PORT`, `E2E_MYSQL_HOST`, `E2E_MYSQL_PORT`, `E2E_MSSQL_HOST`, `E2E_MSSQL_PORT` | Where the engines listen. The defaults are `127.0.0.1` and the ports in [Quick start](#quick-start). `E2E_MSSQL_HOST` refuses an Azure SQL host, since the cases create and drop databases |

`E2E_REQUIRE_SQLSERVER` is a switch of its own because SQL Server is often absent where the
other engines are not, and `E2E_REQUIRE_LIVE` keeps meaning Postgres and MySQL. The
dockerised harness starts SQL Server only when asked, with `E2E_REQUIRE_SQLSERVER=1 sh
e2e/run.sh`, and the CI live-db job sets both switches.

The SQL Server port, `sa` password and image are repeated by hand in CI, `e2e/docker-compose.yml`,
the e2e suite's defaults, the harness's defaults and this page.
`TestTheSQLServerTestSettingsAgreeEverywhere` in `testsupport/drift_test.go` fails when one of
them drifts, since a copy that drifts fails far from its cause: a password changed in one place
reads as an engine that never came up.
