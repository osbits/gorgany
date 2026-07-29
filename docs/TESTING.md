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

An explicitly empty `GORGANY_TEST_PASSWORD=` is a real value, not an absence — substituting
the default for it would silently connect as something else.

`GORGANY_TEST_ENGINE_WAIT` exists because a container started in the same CI step is
usually not accepting connections yet, and a suite that gives up on the first refused dial
is a suite that fails intermittently. The harness also makes a real round trip before
declaring the engine up: a lazily-connecting driver reports success and fails on first
query.

`GORGANY_TEST_KEEP_DATA=1` leaves rows in place so you can inspect them after a failure.
Tests will interfere with each other; it is for debugging by hand.

`GORGANY_TEST_MIGRATE_DOWN=1` runs every migration's `Down` before `Up`, so a schema left
behind by an interrupted run does not poison the next one.

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
