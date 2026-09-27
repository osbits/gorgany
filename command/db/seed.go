package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db"
	"github.com/osbits/gorgany/v2/log"
	"gorm.io/gorm"
)

type SeedCommand struct {
	// Datasource declares --datasource to command.Resolver, whose flag parser rejects
	// every flag a command does not declare: without it `cli db:seed --datasource=x`
	// exited 2 before Execute ran. Execute reads the value through SelectedDatasource,
	// as db:migrate does.
	Datasource string `command:"flag,name=datasource,default=default,description=datasource to seed (a key under databases)"`

	dataContext core.IDataContext `container:"inject"`
	dbContext   core.IDBContext   `container:"inject"`
}

func (thiz SeedCommand) GetName() string {
	return "db:seed"
}

// Execute seeds the selected datasource.
//
// It used to hard-code core.DefaultKeyInRegistrar, so in a two-datasource app a
// seeder written for the second database silently ran against the first. The
// datasource now comes from --datasource (defaulting to `default`), and a seeder
// can declare its own target via DatasourceScoped so it is skipped rather than
// misapplied.
//
// Each seeder commits with its row in `seeders` or not at all (see applySeeder). The
// first failure stops the run with exit 1, and the seeders before it stay applied.
//
// An external_schema or read_only datasource, and a seeder aimed at one, are refused
// before any SQL, as a configuration error (exit 2). The `seeders` table used to be
// created before the seeders were even partitioned, so every run left one behind, in a
// database gorgany does not own too; it is now created only when a seeder targets the
// datasource.
func (thiz SeedCommand) Execute(ctx context.Context) {
	datasource := SelectedDatasource()

	gormInstance, err := ResolveOwnedGorm(thiz.dbContext, datasource, thiz.GetName())
	if err != nil {
		panic(err)
	}

	seeders, skipped, err := partitionOwned(
		thiz.dataContext.Seeders(),
		datasource,
		thiz.dbContext,
		func(s core.ISeeder) string { return fmt.Sprintf("seeder %q", s.Name()) },
	)
	if err != nil {
		panic(err)
	}
	for _, s := range skipped {
		log.Log().Infof("Skipping seeder %s: it targets datasource %q, not %q",
			s.Name(), TargetDatasourceOf(s), datasource)
	}

	if len(seeders) == 0 {
		log.Log().Infof("No seeders target datasource %q", datasource)
		return
	}

	err = migrateBookkeepingTable(gormInstance, &db.Seeder{}, "seeders")
	if err != nil {
		panic(fmt.Errorf("unable to migrate table `seeders` on datasource %q: %w", datasource, err))
	}

	log.Log().Infof("Seeding datasource %q", datasource)

	if err := thiz.applyPending(gormInstance, seeders); err != nil {
		log.Log().Errorf("Error while seeding: %v", err)
		log.Log().Warn("Seeding has finished with error")
		os.Exit(1)
	}
	log.Log().Info("Seeding finished.")
}

// applyPending runs, in order, each seeder that is not yet recorded, and stops at the
// first failure.
func (thiz SeedCommand) applyPending(gormInstance *gorm.DB, seeders []core.ISeeder) error {
	for _, seeder := range seeders {
		// A failed read used to count as "not seeded", so the seeder ran again.
		var seederDomain db.Seeder
		err := gormInstance.First(&seederDomain, "name = ?", seeder.Name()).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("cannot read whether seeder %s has run: %w", seeder.Name(), err)
		}
		if thiz.isSeederExists(seederDomain) {
			continue
		}

		log.Log().Infof("Executing %s seeder", seeder.Name())
		if err := applySeeder(gormInstance, seeder); err != nil {
			return err
		}
		log.Log().Infof("Seeder %s successfully executed.", seeder.Name())
	}
	return nil
}

// applySeeder saves one seeder's models and records it in the same transaction.
//
// The command used to open a transaction for the whole run and then save every model, and
// write the row, on the pool instead, checking neither the insert's error nor the commit's.
// A model that failed to save left the seeder's earlier models committed and the seeder
// unrecorded; a row that could not be written left the seeder applied but unrecorded, and
// the run exited 0. Either way the next run saved the models again, duplicating them or
// failing on a unique constraint. Now the models commit with the row or not at all, and a
// failure fails the run.
//
// A seeder only saves rows, which PostgreSQL, InnoDB and SQL Server all roll back, so unlike
// a migration on MySQL a failed seeder leaves nothing behind on any of them.
func applySeeder(gormInstance *gorm.DB, seeder core.ISeeder) error {
	models := seeder.CollectInsertModels()

	tx := gormInstance.Begin()
	if tx.Error != nil {
		return fmt.Errorf("cannot begin a transaction for seeder %s: %w", seeder.Name(), tx.Error)
	}

	for _, model := range models {
		if err := tx.Save(model).Error; err != nil {
			tx.Rollback()
			return fmt.Errorf("seeder %s failed: %w", seeder.Name(), err)
		}
	}

	if err := tx.Create(&db.Seeder{Name: seeder.Name(), Date: time.Now()}).Error; err != nil {
		tx.Rollback()
		return fmt.Errorf("seeder %s ran but could not be recorded: %w", seeder.Name(), err)
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("cannot commit seeder %s: %w", seeder.Name(), err)
	}
	return nil
}

func (thiz SeedCommand) isSeederExists(seeder db.Seeder) bool {
	return !seeder.Date.IsZero() && seeder.Name != ""
}
