// Package testsupport gives an app a database test harness it does not have to write.
//
// db/sql/core.ISession has a Transaction method, so a transaction-per-test seam was
// always *expressible* — but nothing shipped to use it, so every consumer hand-rolled
// the same harness: read connection settings from somewhere, wait for the engine, run
// migrations, clean between tests, skip cleanly when no server is around. v2.0's own
// e2e/tests/live_db_test.go was one more copy of it. This is that code, generalised.
//
// # Quick start
//
//	func TestMain(m *testing.M) {
//	    testsupport.Main(m)
//	}
//
//	func TestSavingAWidget(t *testing.T) {
//	    db := testsupport.RequireDatabase(t)
//
//	    widget := &Widget{Label: "first"}
//	    require.NoError(t, orm.Save(db.Session(), widget))
//
//	    assert.Equal(t, 1, db.CountRows(t, "widgets"))
//	}
//
// RequireDatabase skips the test when no engine is reachable, so the suite stays runnable
// on a laptop with nothing started. Use MustDatabase in CI, where an absent engine is a
// failure rather than a reason to skip.
//
// # Isolation
//
// Two strategies, chosen with Config.Isolation:
//
//   - IsolateByTruncation (the default) empties every table the migrations created, after
//     each test. It works with code that commits, spawns goroutines, or opens its own
//     sessions.
//   - IsolateByRollback runs each test inside a transaction and rolls it back. It is
//     faster and leaves nothing behind even on a panic, but the code under test has to
//     use the session the harness hands it — anything opening its own connection will not
//     see the uncommitted data.
//
// Truncation is the default because it is the one that cannot silently mislead.
//
// # Engines
//
// Postgres, MySQL and SQL Server, selected by environment variable so one suite covers each
// without a code change:
//
//	GORGANY_TEST_DRIVER=postgres_gorm   # or mysql_gorm, or sqlserver_gorm
//	GORGANY_TEST_HOST=127.0.0.1
//	GORGANY_TEST_PORT=5433
//	GORGANY_TEST_USER=postgres
//	GORGANY_TEST_PASSWORD=test
//	GORGANY_TEST_DB=gorgany_test
//
// See Config for the full list and the defaults, which follow the driver. Config.Databases
// lets a suite declare several engines and run the same tests against each.
//
// testsupport registers the Postgres and MySQL drivers itself. SQL Server's is an import of the
// suite's own, _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver", so that an app that
// never speaks SQL Server does not link its driver into every test binary. On SQL Server the
// harness creates the database through master when it is missing, since the engine's container
// image cannot create one from its environment.
//
// # Targets
//
// The harness empties every table its migrations create, so it refuses a database it must
// not run against: an Azure SQL, Azure Database for PostgreSQL or Azure Database for MySQL
// host, a driver it cannot truncate, and a SQL Server database without "test" as a word of
// its name. The refusal fails the test rather than skipping it, as a bad
// config does, and GORGANY_TEST_ALLOW_ANY_TARGET=1 (or Config.AllowAnyTarget) switches it off
// for a target known to be disposable. A driver that is not registered in the test binary
// fails the same way, naming the import that registers it.
package testsupport
