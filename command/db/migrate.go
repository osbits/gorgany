package db

import (
	"fmt"
	"gorm.io/gorm"
	"graecoFramework/db"
	"os"
	"time"
)

type MigrationClosure func(db *gorm.DB) error

type Migration interface {
	Up() MigrationClosure
	Down() MigrationClosure
	Name() string
}

type MigrateCommand struct {
}

var migrations = make([]Migration, 0)

func AddMigration(migration Migration) {
	migrations = append(migrations, migration)
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

func (thiz MigrateCommand) GetSignature() string {
	return "db:migrate"
}

func (thiz MigrateCommand) up() {
	gormInstance := db.GetWrapper("gorm").GetInstance().(*gorm.DB)

	err := gormInstance.AutoMigrate(&db.Migration{})
	if err != nil {
		panic("Unable to migrate table `migrations`")
	}

	for _, migration := range migrations {
		var migrationDomain db.Migration
		gormInstance.First(&migrationDomain, "name = ?", migration.Name())
		if thiz.isMigrationExists(migrationDomain) {
			continue
		}
		fmt.Println("next")

		fmt.Printf("Migration %s is executing\n", migration.Name())
		tx := gormInstance.Begin()

		closure := migration.Up()
		err = closure(gormInstance)
		if err != nil {
			tx.Rollback()
		}

		tx.Commit()

		gormInstance.Create(&db.Migration{
			Name: migration.Name(),
			Date: time.Now(),
		})
		fmt.Printf("Migration %s finished\n", migration.Name())
	}

	fmt.Println("Success")
}

func (thiz MigrateCommand) down() {

}

func (thiz MigrateCommand) isMigrationExists(migration db.Migration) bool {
	return !migration.Date.IsZero() && migration.Name != ""
}
