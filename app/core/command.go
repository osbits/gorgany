package core

import (
	"context"

	"gorm.io/gorm"
)

// IConsoleContext defines the interface for console command management
type IConsoleContext interface {
	// RegisterCommand registers a new console command
	RegisterCommand(command ICommand)
	// GetCommand retrieves a command by name
	GetCommand(name string) ICommand
}

// ICommand defines the interface for console commands
type ICommand interface {
	// Execute runs the command with the given context
	Execute(ctx context.Context)
	// GetName returns the name of the command
	GetName() string
}

// ICommands represents a collection of commands
type ICommands []ICommand

// MigrationClosure defines a function type for database migrations
type MigrationClosure func(db *gorm.DB) error

// IDataContext defines the interface for database context management
type IDataContext interface {
	// Migrations returns all registered migrations
	Migrations() []IMigration
	// AddMigration registers a new migration
	AddMigration(migration IMigration)
	// Seeders returns all registered seeders
	Seeders() []ISeeder
	// AddSeeder registers a new seeder
	AddSeeder(seeder ISeeder)
}

// IMigration defines the interface for database migrations
type IMigration interface {
	// Up returns the migration function for upgrading the database
	Up() MigrationClosure
	// Down returns the migration function for downgrading the database
	Down() MigrationClosure
	// Name returns the name of the migration
	Name() string
}

// ISeeder defines the interface for database seeders
type ISeeder interface {
	// CollectInsertModels returns the models to be inserted during seeding
	CollectInsertModels() []any
	// Name returns the name of the seeder
	Name() string
}
