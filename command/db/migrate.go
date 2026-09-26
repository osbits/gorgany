package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/log"
	"gorm.io/gorm"
)

type MigrateCommand struct {
	dataContext core.IDataContext `container:"inject"`
	dbContext   core.IDBContext   `container:"inject"`
}

func (thiz MigrateCommand) GetName() string {
	return "db:migrate"
}

type MigrationType string

const (
	Up   MigrationType = "up"
	Down MigrationType = "down"
)

const migrateUsage = "Use 'cli db:migrate up [--datasource=<name>]' or " +
	"'cli db:migrate down [--datasource=<name>] [--steps=<n>]'"

func (thiz MigrateCommand) Execute(ctx context.Context) {
	if len(os.Args) < 3 {
		panic(migrateUsage)
	}
	migrationKind := MigrationType(os.Args[2])

	switch migrationKind {
	case Up:
		thiz.up(ctx)
	case Down:
		thiz.down(ctx)
	default:
		panic("Can`t resolve type of migration(Up or Down?). " + migrateUsage)
	}
}

// migrationsFor returns the GORM handle for the selected datasource together with
// the migrations that target it. It sends no SQL.
//
// The bookkeeping table lives in the selected database, so each datasource tracks
// its own applied set independently.
//
// It used to create that table first, before it knew whether any migration targets the
// datasource, and before anything could say the datasource was not gorgany's to change:
// every run left a `migrations` table behind, in someone else's database too. Now an
// external_schema or read_only datasource is refused on its configuration, a migration
// aimed at one is refused wherever it would be skipped, and the table is created by up,
// only when there is a migration to record in it (see ensureMigrationsTable). The
// migrations OnOwnedDefault wrapped are left out, ahead of both checks on the migrations,
// when `default` cannot take them.
func (thiz MigrateCommand) migrationsFor(datasource string) (*gorm.DB, []core.IMigration, error) {
	gormInstance, err := ResolveOwnedGorm(thiz.dbContext, datasource, thiz.GetName())
	if err != nil {
		return nil, nil, err
	}

	matching, skipped, err := partitionOwned(
		thiz.placedMigrations(),
		datasource,
		thiz.dbContext,
		func(m core.IMigration) string { return fmt.Sprintf("migration %q", m.Name()) },
	)
	if err != nil {
		return nil, nil, err
	}

	for _, m := range skipped {
		log.Log().Infof("Skipping migration %s: it targets datasource %q, not %q",
			m.Name(), TargetDatasourceOf(m), datasource)
	}

	return gormInstance, matching, nil
}

// OnOwnedDefault wraps a migration that runs on `default` only while `default` is configured
// without external_schema or read_only. db:migrate leaves it out of the run otherwise, with a
// line saying why, rather than failing the run over it. The wrapped migration keeps its
// Name, Up and Down, and like any migration that declares no datasource it targets
// `default`.
//
// DbProvider.Boot wraps the framework's two sessions migrations with it. Boot adds them to
// every app, and not every app has a `default` they may run on: with none,
// PartitionByDatasource rejects them as aimed at a datasource that is not configured, which
// failed every db:migrate whichever --datasource it named, and on an external_schema or
// read_only `default` refuseUnownedTargets would. The question is asked when db:migrate
// runs, of the datasources it runs with, rather than at Boot, because an app may register
// its `default` on the DBContext from a provider whose Boot runs after DbProvider's.
func OnOwnedDefault(migration core.IMigration) core.IMigration {
	return ownedDefaultMigration{migration}
}

// ownedDefaultMigration is a migration OnOwnedDefault wrapped.
type ownedDefaultMigration struct{ core.IMigration }

// placedMigrations returns the registered migrations less those OnOwnedDefault wrapped,
// when `default` cannot take them, and logs each one it leaves out.
func (thiz MigrateCommand) placedMigrations() []core.IMigration {
	migrations := thiz.dataContext.Migrations()

	reason := ownedDefaultRefusal(thiz.dbContext)
	if reason == "" {
		return migrations
	}

	placed := make([]core.IMigration, 0, len(migrations))
	for _, migration := range migrations {
		if _, ok := migration.(ownedDefaultMigration); ok {
			log.Log().Infof("Skipping migration %s: %s", migration.Name(), reason)
			continue
		}
		placed = append(placed, migration)
	}
	return placed
}

// ownedDefaultRefusal reports why the migrations OnOwnedDefault wrapped cannot run on
// dbContext's `default`, or "" when they can.
func ownedDefaultRefusal(dbContext core.IDBContext) string {
	if !IsConfigured(dbContext)(core.DefaultKeyInRegistrar) {
		return fmt.Sprintf("no %q datasource is configured, and it runs only there", core.DefaultKeyInRegistrar)
	}
	if refusal := dbCore.PolicyOf(dbContext.GetDataSource(core.DefaultKeyInRegistrar)).Refusal(); refusal != nil {
		return fmt.Sprintf("datasource %q refuses it: %v", core.DefaultKeyInRegistrar, refusal)
	}
	return ""
}

// ensureMigrationsTable creates the bookkeeping table on the datasource if it is missing.
func ensureMigrationsTable(gormInstance *gorm.DB, datasource string) error {
	if err := gormInstance.AutoMigrate(&db.Migration{}); err != nil {
		return fmt.Errorf("unable to migrate table `migrations` on datasource %q: %w", datasource, err)
	}
	return nil
}

func (thiz MigrateCommand) up(ctx context.Context) {
	datasource := SelectedDatasource()

	gormInstance, migrations, err := thiz.migrationsFor(datasource)
	if err != nil {
		panic(err)
	}

	if len(migrations) == 0 {
		log.Log().Infof("No migrations target datasource %q", datasource)
		return
	}

	if err := ensureMigrationsTable(gormInstance, datasource); err != nil {
		panic(err)
	}

	log.Log().Infof("Migrating datasource %q", datasource)

	if err := thiz.applyPending(gormInstance, migrations); err != nil {
		log.Log().Errorf("Error while migration is executing: %v", err)
		log.Log().Warn("Migration has finished with error")
		os.Exit(1)
	}
	log.Log().Infof("Success")
}

// applyPending applies, in order, each migration that is not yet recorded, and stops at
// the first failure.
func (thiz MigrateCommand) applyPending(gormInstance *gorm.DB, migrations []core.IMigration) error {
	for _, migration := range migrations {
		// A failed read used to count as "not applied", so the migration ran again.
		var migrationDomain db.Migration
		err := gormInstance.First(&migrationDomain, "name = ?", migration.Name()).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("cannot read whether migration %s is applied: %w", migration.Name(), err)
		}
		if thiz.isMigrationExists(migrationDomain) {
			continue
		}

		log.Log().Infof("Migration %s is executing\n", migration.Name())
		if err := applyMigration(gormInstance, migration); err != nil {
			return err
		}
		log.Log().Infof("Migration %s finished\n", migration.Name())
	}
	return nil
}

// applyMigration runs one migration's Up() and records it in the same transaction.
//
// The row used to be written after the commit, on the pool rather than the transaction,
// and neither the commit's error nor the insert's was checked. A migration whose COMMIT
// failed (a deferred constraint, or a transaction a swallowed error had aborted) was
// recorded as applied and the run exited 0; one whose row could not be written stayed
// applied but unrecorded, and ran again on the next deploy. Now the row commits with the
// schema change or not at all, and either failure fails the run.
//
// MySQL commits every DDL statement implicitly, so there the transaction cannot hold a
// schema change back: a migration that fails part-way leaves its earlier DDL applied, and
// the row, written after that implicit commit, commits on its own. A failure still fails
// the run, and the migration is recorded only if everything before the row succeeded.
func applyMigration(gormInstance *gorm.DB, migration core.IMigration) error {
	tx := gormInstance.Begin()
	if tx.Error != nil {
		return fmt.Errorf("cannot begin a transaction for migration %s: %w", migration.Name(), tx.Error)
	}

	if err := migration.Up()(tx); err != nil {
		tx.Rollback()
		return fmt.Errorf("migration %s failed: %w", migration.Name(), err)
	}

	if err := tx.Create(&db.Migration{Name: migration.Name(), Date: time.Now()}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("migration %s ran but could not be recorded: %w", migration.Name(), err)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("cannot commit migration %s: %w", migration.Name(), err)
	}
	return nil
}

// down rolls back the most recently applied migrations, newest first.
//
// It used to be an empty stub — `func (thiz MigrateCommand) down() {}` — which
// reported success and did nothing at all, the worst of the available options.
//
// The number of migrations to roll back comes from --steps and defaults to 1. A
// recorded migration whose code is no longer registered is refused rather than
// skipped: there is no Down() to run, so silently dropping the row would leave the
// schema and the bookkeeping table disagreeing.
//
// With no migration targeting the datasource, or no `migrations` table in it, there is
// nothing to roll back and down sends nothing more. It checks the table rather than
// creating it: down used to create it, as up does, and since a failed read of the table is
// an error, reading one that is not there would fail a run that has nothing to do. A check
// that fails is an error too (see hasMigrationsTable).
func (thiz MigrateCommand) down(ctx context.Context) {
	datasource := SelectedDatasource()

	steps, err := SelectedSteps()
	if err != nil {
		panic(err)
	}

	gormInstance, migrations, err := thiz.migrationsFor(datasource)
	if err != nil {
		panic(err)
	}

	if len(migrations) == 0 {
		log.Log().Infof("Nothing to roll back on datasource %q: no migrations target it", datasource)
		return
	}
	exists, err := hasMigrationsTable(gormInstance)
	if err != nil {
		panic(fmt.Errorf("cannot tell whether datasource %q has a `migrations` table: %w", datasource, err))
	}
	if !exists {
		log.Log().Infof("Nothing to roll back on datasource %q: it has no `migrations` table", datasource)
		return
	}

	registered := make(map[string]core.IMigration, len(migrations))
	for _, migration := range migrations {
		registered[migration.Name()] = migration
	}

	// Newest first. Ordering by name as well keeps the order total when several
	// migrations share a timestamp.
	var applied []db.Migration
	if err := gormInstance.
		Order("date DESC, name DESC").
		Limit(steps).
		Find(&applied).Error; err != nil {
		panic(fmt.Errorf("cannot read applied migrations from datasource %q: %w", datasource, err))
	}

	if len(applied) == 0 {
		log.Log().Infof("Nothing to roll back on datasource %q", datasource)
		return
	}

	log.Log().Infof("Rolling back %d migration(s) on datasource %q", len(applied), datasource)

	for _, record := range applied {
		migration, ok := registered[record.Name]
		if !ok {
			panic(fmt.Errorf(
				"migration %q is recorded as applied on datasource %q but is not registered, "+
					"so there is no Down() to run; register it (or restore it from history) before rolling back",
				record.Name, datasource))
		}

		log.Log().Infof("Rolling back %s", record.Name)

		tx := gormInstance.Begin()
		if tx.Error != nil {
			panic(fmt.Errorf("cannot begin transaction on datasource %q: %w", datasource, tx.Error))
		}

		if err := migration.Down()(tx); err != nil {
			tx.Rollback()
			log.Log().Errorf("Error while rolling back %s: %v", record.Name, err)
			log.Log().Warn("Rollback has finished with error")
			os.Exit(1)
		}

		// The bookkeeping row is deleted inside the same transaction as the schema
		// change, so a failure cannot leave the two disagreeing.
		if err := tx.Where("name = ?", record.Name).Delete(&db.Migration{}).Error; err != nil {
			tx.Rollback()
			log.Log().Errorf("Rolled back %s but could not clear its bookkeeping row: %v", record.Name, err)
			os.Exit(1)
		}

		if err := tx.Commit().Error; err != nil {
			log.Log().Errorf("Cannot commit rollback of %s: %v", record.Name, err)
			os.Exit(1)
		}

		log.Log().Infof("Rolled back %s", record.Name)
	}

	log.Log().Infof("Success")
}

// hasMigrationsTable reports whether the datasource has the `migrations` table, or why it
// cannot tell.
//
// It does not ask gorm's Migrator().HasTable, whose Postgres and MySQL implementations
// discard the error of the query they run and answer false. A lookup that failed — an
// unreachable server or a bad credential, which lazy_connect leaves to the first query, or
// a connection dropped mid-deploy — read as "no table", and down exited 0 having rolled back
// nothing. GetTables runs the same lookup in the dialect's own SQL, and returns its error.
func hasMigrationsTable(gormInstance *gorm.DB) (bool, error) {
	statement := &gorm.Statement{DB: gormInstance}
	if err := statement.Parse(&db.Migration{}); err != nil {
		return false, err
	}

	tables, err := gormInstance.Migrator().GetTables()
	if err != nil {
		return false, err
	}
	return slices.Contains(tables, statement.Table), nil
}

func (thiz MigrateCommand) isMigrationExists(migration db.Migration) bool {
	return !migration.Date.IsZero() && migration.Name != ""
}
