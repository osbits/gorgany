package db

import "git.qix.sx/gorgany/gorgany.git/app/core"

type DataContext struct {
	migrations []core.IMigration
	seeders    []core.ISeeder
}

func (thiz *DataContext) Init() {
	thiz.migrations = make([]core.IMigration, 0)
	thiz.seeders = make([]core.ISeeder, 0)
}

func (thiz *DataContext) Migrations() []core.IMigration {
	return thiz.migrations
}

func (thiz *DataContext) AddMigration(migration core.IMigration) {
	thiz.migrations = append(thiz.migrations, migration)
}

func (thiz *DataContext) Seeders() []core.ISeeder {
	return thiz.seeders
}

func (thiz *DataContext) AddSeeder(seeder core.ISeeder) {
	thiz.seeders = append(thiz.seeders, seeder)
}
