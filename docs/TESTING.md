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

Two details of truncation that matter:

- Tables are emptied in reverse creation order, because the migrations create parents
  before children and deleting forwards hits the foreign key. The harness records which
  tables each migration created, so it knows the order.
- On MySQL, `TRUNCATE` has no `CASCADE`, so foreign key checks are suspended for the
  duration — and restored even if a truncate fails. Leaving them off would make every later
  test in the process pass without referential integrity.

Under rollback isolation, `db.Gorm()` is **not** inside the transaction: gorm's transaction
is a separate handle the session does not expose. Use `db.Exec`, `db.CountRows` or
`db.Session()` to read back what the test wrote; `db.Gorm()` is for checking what actually
committed.

## Testing both engines

```go
func TestWidgetsOnEveryEngine(t *testing.T) {
    testsupport.EachDatabase(t, func(t *testing.T, db *testsupport.Database) {
        // runs once per configured engine, as a subtest named after it
        if db.IsMySQL() {
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
    },
})
```

An engine that is not reachable skips its own subtest rather than the whole set, so a laptop
with only Postgres running still gets Postgres coverage.

## Configuration

Everything is settable by environment variable, so one test file covers both engines in CI
without a code change:

| Variable | Default |
|----------|---------|
| `GORGANY_TEST_DRIVER` | `postgres_gorm` |
| `GORGANY_TEST_HOST` | `127.0.0.1` |
| `GORGANY_TEST_PORT` | `5433` (`3307` for MySQL) |
| `GORGANY_TEST_USER` | `postgres` (`root` for MySQL) |
| `GORGANY_TEST_PASSWORD` | `test` |
| `GORGANY_TEST_DB` | `gorgany_test` |
| `GORGANY_TEST_SSL` | `disable` |
| `GORGANY_TEST_ISOLATION` | `truncate` |
| `GORGANY_TEST_ENGINE_WAIT` | `30s` |
| `GORGANY_TEST_KEEP_DATA` | unset |
| `GORGANY_TEST_MIGRATE_DOWN` | unset |
| `GORGANY_TEST_ALLOW_ANY_TARGET` | unset |

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
registers Postgres and MySQL itself, through `driver/builtin`. SQL Server's driver is a separate
import, `_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"`, which the test package (or
the `pkg/provider` package it imports) has to make; the error names it. The harness does not
truncate SQL Server yet: under `IsolateByTruncation` it fails the test with an error saying so.

## What `Database` gives you

| Member | Purpose |
|--------|---------|
| `Session()` | The session to use. Required under rollback isolation |
| `Tx()` | The test's transaction under rollback isolation, else nil |
| `DataSource()` | For a test that opens its own session |
| `Gorm()` | Raw `*gorm.DB`, for assertions the ORM cannot express |
| `Driver()`, `IsPostgres()`, `IsMySQL()` | Which engine, for dialect-specific assertions |
| `Tables()` | The tables the migrations created |
| `CountRows(t, table)` | Row count, failing the test on error |
| `Exec(t, sql, args...)` | Run a statement, failing the test on error |
| `Truncate(t, tables...)` | A clean slate part-way through a test |

Connecting and migrating happen **once per package**, not once per test. That is the
difference between a suite that runs in seconds and one nobody runs.

## The harness's own tests

`testsupport/live_test.go` is behind the `livedb` build tag and runs against real Postgres
and MySQL:

```bash
go test -tags=livedb ./testsupport/ -count=1 -v
```

It exists because a harness nobody has run against a real engine is exactly the failure
mode this framework has hit before: the MySQL driver shipped in v2.0 with a green suite
while being unable to insert anything. The rollback-isolation bug in the first version of
this package — helper writes went to the non-transactional handle, so rollback isolation
silently did nothing — was caught by that test and by nothing else.

### The framework's live suite

`e2e/tests` holds the framework's own live cases, behind the same `livedb` tag. The Postgres
and MySQL cases run against the two containers in [Quick start](#quick-start), and the SQL
Server cases against this one:

```bash
docker run --rm -d --platform linux/amd64 --name gorgany-mssql -e ACCEPT_EULA=Y -e MSSQL_SA_PASSWORD=Gorgany-Test-1 -e MSSQL_PID=Developer -p 14330:1433 mcr.microsoft.com/mssql/server:2022-CU27-ubuntu-22.04
```

The image is amd64-only and about 1.5 GB. On an Apple Silicon Mac it runs under Rosetta, once
Docker Desktop's "Use Rosetta for x86_64/amd64 emulation on Apple Silicon" setting is on; under
Docker's default emulation it does not start. The password is not the other engines' `test`
because SQL Server refuses one below its complexity policy, and the cases create their
`gorgany_test` database through `master` themselves, since the image cannot create one from
its environment.

```bash
E2E_REQUIRE_SQLSERVER=1 go test -tags=livedb ./e2e/tests/ -run SQLServer -count=1 -v
```

| Variable | Effect |
|----------|--------|
| `E2E_REQUIRE_LIVE=1` | A Postgres or MySQL case that cannot reach its engine fails instead of skipping, and a run in which no live case executed fails |
| `E2E_REQUIRE_SQLSERVER=1` | The same for the SQL Server cases, which otherwise skip, after one wait for the whole run, when SQL Server is not reachable |
| `E2E_PG_HOST`, `E2E_PG_PORT`, `E2E_MYSQL_HOST`, `E2E_MYSQL_PORT`, `E2E_MSSQL_HOST`, `E2E_MSSQL_PORT` | Where the engines listen. The defaults are `127.0.0.1` and the ports above. `E2E_MSSQL_HOST` refuses an Azure SQL host, since the cases create and drop databases |

`E2E_REQUIRE_SQLSERVER` is a switch of its own because SQL Server is often absent where the
other engines are not, and `E2E_REQUIRE_LIVE` keeps meaning Postgres and MySQL. The
dockerised harness starts SQL Server only when asked, with `E2E_REQUIRE_SQLSERVER=1 sh
e2e/run.sh`, and the CI live-db job sets both switches.

The testsupport harness itself does not run against SQL Server yet: it cannot truncate its
tables (see [Which databases the harness refuses](#which-databases-the-harness-refuses)).
