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
// Both Postgres and MySQL, selected by environment variable so one suite covers both
// without a code change:
//
//	GORGANY_TEST_DRIVER=postgres_gorm   # or mysql_gorm
//	GORGANY_TEST_HOST=127.0.0.1
//	GORGANY_TEST_PORT=5433
//	GORGANY_TEST_USER=postgres
//	GORGANY_TEST_PASSWORD=test
//	GORGANY_TEST_DB=gorgany_test
//
// See Config for the full list and the defaults. Config.Databases lets a suite declare
// several engines and run the same tests against each.
package testsupport
