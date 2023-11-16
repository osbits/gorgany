package db

import (
	"fmt"
	"gorgany/db"
	"gorgany/internal"
	"gorm.io/gorm"
	"time"
)

type SeedCommand struct {
}

func (thiz SeedCommand) GetName() string {
	return "db:seed"
}

func (thiz SeedCommand) Execute() {
	gormInstance := db.Builder().GetConnection().Driver().(*gorm.DB)

	err := gormInstance.AutoMigrate(&db.Seeder{})
	if err != nil {
		panic("Unable to migrate table `migrations`")
	}

	total := 0
	tx := gormInstance.Begin()
	for _, seeder := range internal.GetFrameworkRegistrar().GetSeeders() {
		var seederDomain db.Seeder
		gormInstance.First(&seederDomain, "name = ?", seeder.Name())

		if thiz.isSeederExists(seederDomain) {
			continue
		}

		fmt.Printf("Executing %s seeder\n", seeder.Name())
		seederCount := 0
		for _, model := range seeder.CollectInsertModels() {
			res := gormInstance.Create(model)
			if res.Error != nil {
				tx.Rollback()
				panic(res.Error)
			}
			seederCount++
		}
		fmt.Printf("Seeder %s successfully executed. Number of inserted records: %d\n", seeder.Name(), seederCount)
		total += seederCount

		gormInstance.Create(&db.Seeder{
			Name: seeder.Name(),
			Date: time.Now(),
		})
	}
	tx.Commit()
	fmt.Printf("Seeding finished. Total inserted records: %d\n", total)
}

func (thiz SeedCommand) isSeederExists(seeder db.Seeder) bool {
	return !seeder.Date.IsZero() && seeder.Name != ""
}
