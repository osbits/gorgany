//go:build livedb

// The harness's own live verification. Behind the `livedb` build tag because it needs real
// servers, like the rest of the framework's live checks.
//
//	docker run --rm -d --name gorgany-pg -e POSTGRES_PASSWORD=test \
//	  -e POSTGRES_DB=gorgany_test -p 5433:5432 postgres:16-alpine
//
//	docker run --rm -d --name gorgany-mysql -e MYSQL_ROOT_PASSWORD=test \
//	  -e MYSQL_DATABASE=gorgany_test -p 3307:3306 mysql:8 \
//	  --character-set-server=utf8mb4 --collation-server=utf8mb4_unicode_ci
//
//	go test -tags=livedb ./testsupport/ -count=1 -v
//
// A harness nobody has run against a real engine is exactly the failure mode this whole
// brief is about: the MySQL driver shipped in v2 with a green suite while being unable to
// insert anything.
package testsupport

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------- fixtures

// widgetMigration creates two tables with a foreign key between them, because the
// interesting part of truncation is ordering.
type widgetMigration struct{}

func (widgetMigration) Name() string { return "testsupport_widgets" }

func (widgetMigration) Up() core.MigrationClosure {
	return func(db *gorm.DB) error {
		if err := db.Exec(`CREATE TABLE IF NOT EXISTS ts_widgets (
			id INT PRIMARY KEY,
			label VARCHAR(64) NOT NULL
		)`).Error; err != nil {
			return err
		}
		return db.Exec(`CREATE TABLE IF NOT EXISTS ts_widget_notes (
			id INT PRIMARY KEY,
			widget_id INT NOT NULL,
			body VARCHAR(64) NOT NULL,
			CONSTRAINT fk_ts_note_widget FOREIGN KEY (widget_id) REFERENCES ts_widgets(id)
		)`).Error
	}
}

func (widgetMigration) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		if err := db.Exec(`DROP TABLE IF EXISTS ts_widget_notes`).Error; err != nil {
			return err
		}
		return db.Exec(`DROP TABLE IF EXISTS ts_widgets`).Error
	}
}

// liveHarness builds a harness over both engines, so EachDatabase covers them.
func liveHarness(t *testing.T, isolation Isolation) *Harness {
	t.Helper()

	return New(Config{
		Databases: []DatabaseConfig{
			{
				Name: "postgres", Driver: DriverPostgres,
				Host: "127.0.0.1", Port: 5433,
				User: "postgres", Password: "test", Database: "gorgany_test",
			},
			{
				Name: "mysql", Driver: DriverMySQL,
				Host: "127.0.0.1", Port: 3307,
				User: "root", Password: "test", Database: "gorgany_test",
			},
		},
		Isolation:   isolation,
		EngineWait:  10 * time.Second,
		MigrateDown: true,
	}).AddMigration(widgetMigration{})
}

// insertWidget writes a row through the handle the harness says to use.
func insertWidget(t *testing.T, db *Database, id int, label string) {
	t.Helper()
	db.Exec(t, "INSERT INTO ts_widgets (id, label) VALUES (?, ?)", id, label)
}

// ------------------------------------------------------------------- tests

// TestTheHarnessMigratesAndConnects is the baseline: an engine comes up, the migrations
// run, and the tables the harness will truncate are the ones the migrations created.
func TestTheHarnessMigratesAndConnects(t *testing.T) {
	liveHarness(t, IsolateByTruncation).EachDatabase(t, func(t *testing.T, db *Database) {
		require.NotNil(t, db.Gorm())
		require.NotNil(t, db.Session())

		assert.ElementsMatch(t, []string{"ts_widgets", "ts_widget_notes"}, db.Tables(),
			"the harness must know exactly what the migrations created")

		// And the schema is real, not merely reported.
		insertWidget(t, db, 1, "first")
		assert.Equal(t, 1, db.CountRows(t, "ts_widgets"))
	})
}

// TestTruncationIsolatesTests is the property the whole package exists for. Two tests
// writing the same primary key would collide if either leaked.
func TestTruncationIsolatesTests(t *testing.T) {
	harness := liveHarness(t, IsolateByTruncation)

	for _, round := range []string{"first", "second", "third"} {
		t.Run(round, func(t *testing.T) {
			harness.EachDatabase(t, func(t *testing.T, db *Database) {
				require.Equal(t, 0, db.CountRows(t, "ts_widgets"),
					"the previous round's rows must be gone")

				insertWidget(t, db, 1, round)
				require.Equal(t, 1, db.CountRows(t, "ts_widgets"))
			})
		})
	}
}

// TestTruncationRespectsForeignKeys. The migrations create the parent first, so deleting
// forwards hits the constraint — this is why truncation walks the tables in reverse.
func TestTruncationRespectsForeignKeys(t *testing.T) {
	harness := liveHarness(t, IsolateByTruncation)

	t.Run("write parent and child", func(t *testing.T) {
		harness.EachDatabase(t, func(t *testing.T, db *Database) {
			insertWidget(t, db, 1, "parent")
			db.Exec(t, "INSERT INTO ts_widget_notes (id, widget_id, body) VALUES (?, ?, ?)", 1, 1, "note")

			require.Equal(t, 1, db.CountRows(t, "ts_widget_notes"))
		})
	})

	t.Run("both are gone", func(t *testing.T) {
		harness.EachDatabase(t, func(t *testing.T, db *Database) {
			assert.Equal(t, 0, db.CountRows(t, "ts_widgets"))
			assert.Equal(t, 0, db.CountRows(t, "ts_widget_notes"))
		})
	})
}

// TestForeignKeyChecksAreRestoredAfterTruncation. MySQL truncation suspends them, and
// leaving them off would make every later test in the process pass without referential
// integrity.
func TestForeignKeyChecksAreRestoredAfterTruncation(t *testing.T) {
	harness := liveHarness(t, IsolateByTruncation)

	harness.EachDatabase(t, func(t *testing.T, db *Database) {
		db.Truncate(t)

		// A child row with no parent must still be refused.
		err := db.Gorm().Exec(
			"INSERT INTO ts_widget_notes (id, widget_id, body) VALUES (?, ?, ?)", 1, 999, "orphan").Error
		require.Error(t, err, "the foreign key must still be enforced after a truncate")
	})
}

// TestRollbackIsolatesTests covers the other strategy.
func TestRollbackIsolatesTests(t *testing.T) {
	harness := liveHarness(t, IsolateByRollback)

	for _, round := range []string{"first", "second"} {
		t.Run(round, func(t *testing.T) {
			harness.EachDatabase(t, func(t *testing.T, db *Database) {
				require.NotNil(t, db.Tx(), "rollback isolation must expose the transaction")

				require.Equal(t, 0, db.CountRows(t, "ts_widgets"),
					"the previous round must have rolled back")

				insertWidget(t, db, 1, round)
				require.Equal(t, 1, db.CountRows(t, "ts_widgets"),
					"the test must see its own uncommitted rows")
			})
		})
	}
}

// TestRollbackLeavesNothingCommitted checks from outside the transaction, which is the
// assertion that actually proves the rollback happened.
func TestRollbackLeavesNothingCommitted(t *testing.T) {
	harness := liveHarness(t, IsolateByRollback)

	var outside []*gorm.DB

	t.Run("write inside a rolled-back scope", func(t *testing.T) {
		harness.EachDatabase(t, func(t *testing.T, db *Database) {
			insertWidget(t, db, 42, "doomed")
			outside = append(outside, db.Gorm())
		})
	})

	// The subtest's cleanup has run by now, so the transaction is unwound.
	for _, handle := range outside {
		var count int64
		require.NoError(t, handle.Table("ts_widgets").Where("id = ?", 42).Count(&count).Error)
		assert.Equal(t, int64(0), count, "the rolled-back row must not be visible on a fresh connection")
	}
}

// TestTheDriverIsReportedCorrectly, since dialect-specific assertions depend on it.
func TestTheDriverIsReportedCorrectly(t *testing.T) {
	liveHarness(t, IsolateByTruncation).EachDatabase(t, func(t *testing.T, db *Database) {
		switch db.Label() {
		case "postgres":
			assert.True(t, db.IsPostgres())
			assert.False(t, db.IsMySQL())
			assert.Equal(t, DriverPostgres, db.Driver())
		case "mysql":
			assert.True(t, db.IsMySQL())
			assert.False(t, db.IsPostgres())
			assert.Equal(t, DriverMySQL, db.Driver())
		default:
			t.Fatalf("unexpected label %q", db.Label())
		}
	})
}

// TestTheSessionIsUsable end to end, since Session is what a test is told to use.
func TestTheSessionIsUsable(t *testing.T) {
	liveHarness(t, IsolateByTruncation).EachDatabase(t, func(t *testing.T, db *Database) {
		session := db.Session()
		require.NotNil(t, session)

		result := session.Executor().ExecRaw(context.Background(),
			"INSERT INTO ts_widgets (id, label) VALUES (?, ?)", 7, "via-session")
		require.NoError(t, result.Error)

		assert.Equal(t, 1, db.CountRows(t, "ts_widgets"))
	})
}

// TestAnUnreachableEngineSkipsRatherThanHangs. A wrong port must cost the wait budget and
// then skip, not fail the whole package — and the message has to name the address.
func TestAnUnreachableEngineSkipsRatherThanHangs(t *testing.T) {
	harness := New(Config{
		Databases: []DatabaseConfig{{
			Name: "nowhere", Driver: DriverPostgres,
			Host: "127.0.0.1", Port: 1, // nothing listens here
			User: "postgres", Password: "test", Database: "gorgany_test",
		}},
		EngineWait: time.Second,
	})

	// Run it as a subtest so the skip does not stop this test.
	result := t.Run("unreachable", func(t *testing.T) {
		harness.EachDatabase(t, func(t *testing.T, db *Database) {
			t.Fatal("the body must not run for an unreachable engine")
		})
	})

	assert.True(t, result, "a skip is not a failure")
}

// TestMigrateDownClearsAPreviousSchema. An interrupted run leaves tables behind, and a
// migration that is not idempotent would then fail on the next run.
func TestMigrateDownClearsAPreviousSchema(t *testing.T) {
	// Two harnesses over the same engine: the second must not trip over the first's
	// tables. connect() caches per engine, so this shares the connection and re-runs the
	// migration path.
	first := liveHarness(t, IsolateByTruncation)
	first.EachDatabase(t, func(t *testing.T, db *Database) {
		insertWidget(t, db, 1, "before")
	})

	second := liveHarness(t, IsolateByTruncation)
	second.EachDatabase(t, func(t *testing.T, db *Database) {
		assert.Equal(t, 0, db.CountRows(t, "ts_widgets"))
	})
}

// TestKeepDataLeavesRowsBehind, for debugging a failure by hand.
func TestKeepDataLeavesRowsBehind(t *testing.T) {
	config := Config{
		Databases: []DatabaseConfig{{
			Name: "postgres", Driver: DriverPostgres,
			Host: "127.0.0.1", Port: 5433,
			User: "postgres", Password: "test", Database: "gorgany_test",
		}},
		Isolation:  IsolateByTruncation,
		EngineWait: 10 * time.Second,
		KeepData:   true,
	}

	harness := New(config).AddMigration(widgetMigration{})

	t.Run("write", func(t *testing.T) {
		db := harness.RequireDatabase(t)
		insertWidget(t, db, 99, "kept")
	})

	t.Run("still there", func(t *testing.T) {
		db := harness.RequireDatabase(t)
		assert.Equal(t, 1, db.CountRows(t, "ts_widgets"))

		// Clean up by hand, since KeepData means the harness will not.
		db.Exec(t, "DELETE FROM ts_widgets WHERE id = ?", 99)
	})
}

// TestConcurrentPreparationIsSafe: `go test` runs tests in one process and a suite may use
// t.Parallel, so connect and migrateOnce have to be race-free.
func TestConcurrentPreparationIsSafe(t *testing.T) {
	harness := liveHarness(t, IsolateByTruncation)

	// Prepare once serially so the migration has run, then hammer the cached path.
	harness.EachDatabase(t, func(t *testing.T, db *Database) {})

	for i := 0; i < 8; i++ {
		t.Run(fmt.Sprintf("parallel-%d", i), func(t *testing.T) {
			t.Parallel()
			db := harness.RequireDatabase(t)
			require.NotNil(t, db.Gorm())
		})
	}
}
