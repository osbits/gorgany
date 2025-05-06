package core

import (
	"context"
	"gorm.io/gorm"
)

type IConsoleContext interface {
	RegisterCommand(command ICommand)
	GetCommand(name string) ICommand
}

type ICommand interface {
	Execute(ctx context.Context)
	GetName() string
}

type ICommands []ICommand

type MigrationClosure func(db *gorm.DB) error

type IDataContext interface {
	Migrations() []IMigration
	AddMigration(migration IMigration)
	Seeders() []ISeeder
	AddSeeder(seeder ISeeder)
}

type IMigration interface {
	Up() MigrationClosure
	Down() MigrationClosure
	Name() string
}

type ISeeder interface {
	CollectInsertModels() []any
	Name() string
}
