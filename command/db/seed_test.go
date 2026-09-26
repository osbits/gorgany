package db

import (
	"database/sql"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The recording driver these tests run on is in migrate_test.go.

const (
	seedRecordSQL = `tx: INSERT INTO "seeders" ("name","date") VALUES ($1,$2)`
	rowInsertSQL  = `tx: INSERT INTO "seed_rows" ("label") VALUES ($1) RETURNING "id"`
	tagInsertSQL  = `tx: INSERT INTO "seed_tags" ("label") VALUES ($1) RETURNING "id"`
)

// seedRow and seedTag are seeded models in two tables, so a test can fail the save of one
// and not the other. Their keys are zero, so Save inserts them.
type seedRow struct {
	ID    uint
	Label string
}

type seedTag struct {
	ID    uint
	Label string
}

// modelSeeder is a seeder that saves the given models.
type modelSeeder struct {
	name   string
	models []any
}

func (s modelSeeder) Name() string               { return s.name }
func (s modelSeeder) CollectInsertModels() []any { return s.models }

func twoModels(name string) modelSeeder {
	return modelSeeder{name: name, models: []any{&seedRow{Label: "a"}, &seedTag{Label: "b"}}}
}

// ------------------------------------------------------------ applySeeder

// TestApplySeederSavesAndRecordsInsideOneTransaction: the models and the row used to be
// written on the pool, outside the transaction the command had opened, so a failure part-way
// left the seeder half-applied or unrecorded. They now all sit between BEGIN and COMMIT.
func TestApplySeederSavesAndRecordsInsideOneTransaction(t *testing.T) {
	r := &recording{}

	err := applySeeder(recordingGorm(t, r), twoModels("s1"))

	require.NoError(t, err)
	assert.Equal(t, []string{"BEGIN", rowInsertSQL, tagInsertSQL, seedRecordSQL, "COMMIT"}, r.log)
}

// TestApplySeederRollsBackAFailedSave: a model that failed to save used to leave the models
// before it committed and the seeder unrecorded, so the next run saved them again.
func TestApplySeederRollsBackAFailedSave(t *testing.T) {
	r := &recording{failQuery: `INSERT INTO "seed_tags"`}

	err := applySeeder(recordingGorm(t, r), twoModels("s1"))

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "seeder s1 failed")
	assert.Equal(t, []string{"BEGIN", rowInsertSQL, tagInsertSQL, "ROLLBACK"}, r.log,
		"the first model must be rolled back with the second, and nothing recorded")
}

// TestApplySeederRollsBackWhenTheRowCannotBeWritten: the insert's error used to be
// unchecked, leaving an applied seeder unrecorded, to run again next time.
func TestApplySeederRollsBackWhenTheRowCannotBeWritten(t *testing.T) {
	r := &recording{failExec: `INSERT INTO "seeders"`}

	err := applySeeder(recordingGorm(t, r), twoModels("s1"))

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "seeder s1 ran but could not be recorded")
	assert.Equal(t, []string{"BEGIN", rowInsertSQL, tagInsertSQL, seedRecordSQL, "ROLLBACK"}, r.log)
}

// TestApplySeederReportsAFailedCommit: the commit's error used to be discarded.
func TestApplySeederReportsAFailedCommit(t *testing.T) {
	r := &recording{commitErr: errInjected}

	err := applySeeder(recordingGorm(t, r), twoModels("s1"))

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "cannot commit seeder s1")
	assert.Equal(t, []string{"BEGIN", rowInsertSQL, tagInsertSQL, seedRecordSQL, "COMMIT"}, r.log,
		"the models and the row must have been part of the transaction that failed to commit")
}

func TestApplySeederReportsAFailedBegin(t *testing.T) {
	r := &recording{beginErr: errInjected}

	err := applySeeder(recordingGorm(t, r), twoModels("s1"))

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "cannot begin a transaction for seeder s1")
	assert.Empty(t, r.log)
}

// TestApplySeederWithNoModelsIsStillRecorded: a seeder that returns nothing has run, and
// is not asked again.
func TestApplySeederWithNoModelsIsStillRecorded(t *testing.T) {
	r := &recording{}

	err := applySeeder(recordingGorm(t, r), modelSeeder{name: "s1"})

	require.NoError(t, err)
	assert.Equal(t, []string{"BEGIN", seedRecordSQL, "COMMIT"}, r.log)
}

// ------------------------------------------------------------ applyPending

func TestSeedPendingSkipsRecordedSeedersAndStopsAtTheFirstFailure(t *testing.T) {
	r := &recording{applied: map[string]bool{"s1": true}, failQuery: `INSERT INTO "seed_tags"`}

	err := SeedCommand{}.applyPending(recordingGorm(t, r), []core.ISeeder{
		modelSeeder{name: "s1", models: []any{&seedRow{Label: "one"}}},
		modelSeeder{name: "s2", models: []any{&seedRow{Label: "two"}}},
		twoModels("s3"),
		modelSeeder{name: "s4", models: []any{&seedRow{Label: "four"}}},
	})

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "seeder s3 failed")
	assert.Equal(t, []string{
		"BEGIN", rowInsertSQL, seedRecordSQL, "COMMIT",
		"BEGIN", rowInsertSQL, tagInsertSQL, "ROLLBACK",
	}, withoutReads(r.log), "s1 is recorded, s2 applies, s3 fails, and s4 never starts")
}

// TestSeedPendingReportsAFailedLookup: a failed read of the seeders table used to count as
// "not seeded", so the seeder ran again.
func TestSeedPendingReportsAFailedLookup(t *testing.T) {
	r := &recording{failQuery: `FROM "seeders"`}

	err := SeedCommand{}.applyPending(recordingGorm(t, r), []core.ISeeder{twoModels("s1")})

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "cannot read whether seeder s1 has run")
	assert.Empty(t, withoutReads(r.log), "nothing may run")
}

// TestSeedPendingRunsOnAOneConnectionPool: the command used to hold a transaction open for
// the whole run while it read the seeders table on the pool. With maxOpenConnections: 1 that
// read waited forever for the connection the transaction held. No statement now runs on the
// pool while a seeder's transaction is open.
func TestSeedPendingRunsOnAOneConnectionPool(t *testing.T) {
	r := &recording{}
	sqlDb := sql.OpenDB(r)
	sqlDb.SetMaxOpenConns(1)
	gormDb, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDb}), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- SeedCommand{}.applyPending(gormDb, []core.ISeeder{twoModels("s1"), twoModels("s2")})
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("applyPending is waiting for a connection that its own transaction holds")
	}
	assert.Equal(t, []string{
		"BEGIN", rowInsertSQL, tagInsertSQL, seedRecordSQL, "COMMIT",
		"BEGIN", rowInsertSQL, tagInsertSQL, seedRecordSQL, "COMMIT",
	}, withoutReads(r.log))
}
