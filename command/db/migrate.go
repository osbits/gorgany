package db

import (
	"fmt"
	"gorgany/app/core"
	"gorgany/db"
	"gorgany/internal"
	"gorm.io/gorm"
	"os"
	"time"
)

type MigrateCommand struct {
}

func (thiz MigrateCommand) GetName() string {
	return "db:migrate"
}

type MigrationType string

const (
	Up   MigrationType = "up"
	Down               = "down"
)

func (thiz MigrateCommand) Execute() {
	if len(os.Args) < 3 {
		panic("Use 'cli db:migrate up' or 'cli db:migrate down'")
	}
	migrationKind := MigrationType(os.Args[2])

	switch migrationKind {
	case Up:
		thiz.up()
	case Down:
		thiz.down()
	default:
		panic("Can`t resolve type of migration(Up or Down?)")
	}

}

func (thiz MigrateCommand) up() {
	gormInstance := db.Builder(core.GormPostgresQL).GetConnection().Driver().(*gorm.DB)

	err := gormInstance.AutoMigrate(&db.Migration{})
	if err != nil {
		panic("Unable to migrate table `migrations`")
	}

	isError := false
	for _, migration := range internal.GetFrameworkRegistrar().GetMigrations() {
		var migrationDomain db.Migration
		gormInstance.First(&migrationDomain, "name = ?", migration.Name())
		if thiz.isMigrationExists(migrationDomain) {
			continue
		}

		fmt.Printf("Migration %s is executing\n", migration.Name())
		tx := gormInstance.Begin()

		closure := migration.Up()
		err = closure(gormInstance)
		if err != nil {
			fmt.Println(err)
			tx.Rollback()
			isError = true
			break
		}

		tx.Commit()

		gormInstance.Create(&db.Migration{
			Name: migration.Name(),
			Date: time.Now(),
		})
		fmt.Printf("Migration %s finished\n", migration.Name())
	}

	if !isError {
		fmt.Println("Success")
		return
	}
	fmt.Println("Error")
}

func (thiz MigrateCommand) down() {

}

func (thiz MigrateCommand) isMigrationExists(migration db.Migration) bool {
	return !migration.Date.IsZero() && migration.Name != ""
}
