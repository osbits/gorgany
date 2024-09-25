package db

import (
	"context"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	"git.qix.sx/gorgany/gorgany.git/log"
	"gorm.io/gorm"
	"time"
)

type SeedCommand struct {
}

func (thiz SeedCommand) GetName() string {
	return "db:seed"
}

func (thiz SeedCommand) Execute(ctx context.Context) {
	gormInstance := db.Builder().GetConnection().Driver().(*gorm.DB)

	err := gormInstance.AutoMigrate(&db.Seeder{})
	if err != nil {
		panic("Unable to migrate table `migrations`")
	}

	total := 0
	tx := gormInstance.Begin()
	for _, seeder := range ctx.Value(core.ApplicationContextKey).(core.IApplicationContext).GetSeeders() {
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
