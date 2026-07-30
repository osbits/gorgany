package db

import (
	"context"
	"fmt"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db"
	"github.com/osbits/gorgany/v2/log"
)

type SeedCommand struct {
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
func (thiz SeedCommand) Execute(ctx context.Context) {
	datasource := SelectedDatasource()

	gormInstance, err := ResolveGorm(thiz.dbContext, datasource)
	if err != nil {
		panic(err)
	}

	err = gormInstance.AutoMigrate(&db.Seeder{})
	if err != nil {
		panic(fmt.Errorf("unable to migrate table `seeders` on datasource %q: %w", datasource, err))
	}

	seeders, skipped, err := PartitionByDatasource(
		thiz.dataContext.Seeders(),
		datasource,
		IsConfigured(thiz.dbContext),
		func(s core.ISeeder) string { return fmt.Sprintf("seeder %q", s.Name()) },
	)
	if err != nil {
		panic(err)
	}
	for _, s := range skipped {
		log.Log().Infof("Skipping seeder %s: it targets datasource %q, not %q",
			s.Name(), TargetDatasourceOf(s), datasource)
	}

	log.Log().Infof("Seeding datasource %q", datasource)

	total := 0
	tx := gormInstance.Begin()
	for _, seeder := range seeders {
		var seederDomain db.Seeder
		gormInstance.First(&seederDomain, "name = ?", seeder.Name())

		if thiz.isSeederExists(seederDomain) {
			continue
		}

		log.Log().Infof("Executing %s seeder", seeder.Name())
		seederCount := 0
		for _, model := range seeder.CollectInsertModels() {
			res := gormInstance.Save(model)
			if res.Error != nil {
				tx.Rollback()
				panic(res.Error)
			}
		}
		log.Log().Infof("Seeder %s successfully executed.", seeder.Name())
		total += seederCount

		gormInstance.Create(&db.Seeder{
			Name: seeder.Name(),
			Date: time.Now(),
		})
	}
	tx.Commit()
	log.Log().Info("Seeding finished.")
}

func (thiz SeedCommand) isSeederExists(seeder db.Seeder) bool {
	return !seeder.Date.IsZero() && seeder.Name != ""
}
