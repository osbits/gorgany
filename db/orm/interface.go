// Package orm provides an Object-Relational Mapping (ORM) implementation
package orm

import (
	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
)

// Saver defines methods for saving, creating, updating, and deleting entities in the ORM.
type Saver[T EntityWithMeta] interface {
	// Save persists the given domain to the database, creating or updating as needed.
	Save(entity T) error
	// Create inserts a new domain into the database.
	Create(entity T) error
	// Update modifies an existing domain in the database.
	Update(entity T) error
	// Delete removes the given domain from the database.
	Delete(entity T) error
}

// Finder defines methods for retrieving entities from the database.
type Finder[T EntityWithMeta] interface {
	// Find retrieves an domain by its primary key.
	Find(id interface{}) (T, error)
	// All retrieves all entities of the given type.
	All() ([]T, error)
	// RawQuery executes a raw SQL query and returns the first result.
	RawQuery(query string, args ...interface{}) (T, error)
	// RawQueryAll executes a raw SQL query and returns all results.
	RawQueryAll(query string, args ...interface{}) ([]T, error)
	// Count returns the number of entities of the given type.
	Count() (int64, error)
	// AllByQuery executes the given query builder and returns all results.
	AllByQuery(qb dbCore.IQueryBuilder) ([]T, error)
	// FirstByQuery executes the given query builder and returns the first result.
	FirstByQuery(qb dbCore.IQueryBuilder) (T, error)
	// CountByQuery executes the given query builder and returns the count.
	CountByQuery(qb dbCore.IQueryBuilder) (int64, error)
}

// RelationLoader defines methods for loading and saving domain relations.
type RelationLoader[T EntityWithMeta] interface {
	// LoadRelation loads a specific relation (or nested relation) for the given domain.
	LoadRelation(entity T, relationPath string) error
	// SaveRelations saves all relations of the given domain.
	SaveRelations(entity T) error
}

// Preloader defines methods for preloading relations before executing queries.
type Preloader[T EntityWithMeta] interface {
	// Preload creates a new PreloadBuilder for building preload queries.
	Preload() *PreloadBuilder[T]
}

// IORM is the main ORM interface, embedding Saver, Finder, RelationLoader, and Preloader.
// It provides a unified API for all ORM operations.
type IORM[T EntityWithMeta] interface {
	Saver[T]
	Finder[T]
	RelationLoader[T]
	Preloader[T]
}
