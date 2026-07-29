package db

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/db"
	"github.com/osbits/gorgany/log"
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
// the migrations that target it, having ensured the bookkeeping table exists.
//
// The bookkeeping table lives in the selected database, so each datasource tracks
// its own applied set independently.
func (thiz MigrateCommand) migrationsFor(datasource string) (*gorm.DB, []core.IMigration, error) {
	gormInstance, err := ResolveGorm(thiz.dbContext, datasource)
	if err != nil {
		return nil, nil, err
	}

	if err := gormInstance.AutoMigrate(&db.Migration{}); err != nil {
		return nil, nil, fmt.Errorf("unable to migrate table `migrations` on datasource %q: %w", datasource, err)
	}

	matching, skipped, err := PartitionByDatasource(
		thiz.dataContext.Migrations(),
		datasource,
		IsConfigured(thiz.dbContext),
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

func (thiz MigrateCommand) up(ctx context.Context) {
	datasource := SelectedDatasource()

	gormInstance, migrations, err := thiz.migrationsFor(datasource)
	if err != nil {
		panic(err)
	}

	log.Log().Infof("Migrating datasource %q", datasource)

	isError := false
	for _, migration := range migrations {
		var migrationDomain db.Migration
		gormInstance.First(&migrationDomain, "name = ?", migration.Name())
		if thiz.isMigrationExists(migrationDomain) {
			continue
		}

		log.Log().Infof("Migration %s is executing\n", migration.Name())
		tx := gormInstance.Begin()

		closure := migration.Up()
		err = closure(tx)
		if err != nil {
			log.Log().Errorf("Error while migration is executing: %v", err)
			tx.Rollback()
			isError = true
			break
		}

		tx.Commit()

		gormInstance.Create(&db.Migration{
			Name: migration.Name(),
			Date: time.Now(),
		})
		log.Log().Infof("Migration %s finished\n", migration.Name())
	}

	if !isError {
		log.Log().Infof("Success")
		return
	}
	log.Log().Warn("Migration has finished with error")
	os.Exit(1)
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

func (thiz MigrateCommand) isMigrationExists(migration db.Migration) bool {
	return !migration.Date.IsZero() && migration.Name != ""
}
