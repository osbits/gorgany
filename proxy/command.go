package proxy

import "gorm.io/gorm"

type ICommand interface {
	Execute()
	GetName() string
}

type ICommands []ICommand

type MigrationClosure func(db *gorm.DB) error

type IMigration interface {
	Up() MigrationClosure
	Down() MigrationClosure
	Name() string
}

type ISeeder interface {
	CollectInsertModels() []any
	Name() string
}
