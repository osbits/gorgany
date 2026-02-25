package db

import (
	"context"
	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/db"
	"github.com/osbits/gorgany/log"
	"gorm.io/gorm"
	"os"
	"time"
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
	Down               = "down"
)

func (thiz MigrateCommand) Execute(ctx context.Context) {
	if len(os.Args) < 3 {
		panic("Use 'cli db:migrate up' or 'cli db:migrate down'")
	}
	migrationKind := MigrationType(os.Args[2])

	switch migrationKind {
	case Up:
		thiz.up(ctx)
	case Down:
		thiz.down()
	default:
		panic("Can`t resolve type of migration(Up or Down?)")
	}

}

func (thiz MigrateCommand) up(ctx context.Context) {
	driver, err := thiz.dbContext.GetDataSource(core.DefaultKeyInRegistrar).GetDriver()
	if err != nil {
		panic(err)
	}

	gormInstance, ok := driver.(*gorm.DB)
	if !ok {
		panic("Diff command can`t be executed, because driver is not gorm.DB")
	}

	err = gormInstance.AutoMigrate(&db.Migration{})
	if err != nil {
		panic("Unable to migrate table `migrations`")
	}

	isError := false
	for _, migration := range thiz.dataContext.Migrations() {
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
}

func (thiz MigrateCommand) down() {

}

func (thiz MigrateCommand) isMigrationExists(migration db.Migration) bool {
	return !migration.Date.IsZero() && migration.Name != ""
}
