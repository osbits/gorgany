// Package orm provides an Object-Relational Mapping (ORM) implementation
package orm

import (
	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	v2 "git.qix.sx/gorgany/gorgany.git/db/sql/gorm/postgres/v2"
)

// IORM defines the interface for ORM operations
// T must implement EntityWithMeta
type IORM[T EntityWithMeta] interface {
	// Basic CRUD Operations

	// Find finds an entity by its ID and returns it
	Find(id interface{}) (T, error)

	// First retrieves the first entity that matches the query builder conditions
	First() (T, error)

	// All retrieves all entities that match the query builder conditions
	All() ([]T, error)

	// Save saves an entity (creates if new, updates if existing)
	Save(entity T) (T, error)

	// Create creates a new entity
	Create(entity T) (T, error)

	// Update updates an existing entity
	Update(entity T) (T, error)

	// Delete deletes an entity
	Delete(entity T) (T, error)

	// Refresh reloads an entity from the database
	Refresh(entity T) error

	// Query Building

	// Where adds a condition to the query builder
	Where(field interface{}, operator string, value interface{}) *ORM[T]

	// WithBuilder sets a custom query builder
	WithBuilder(builder *v2.Builder) *ORM[T]

	// Count returns the number of entities that match the query builder conditions
	Count() (int64, error)

	// Raw Query Execution

	// RawQuery executes a raw SQL query and returns the first result
	RawQuery(query string, args ...interface{}) (T, error)

	// RawQueryAll executes a raw SQL query and returns all results
	RawQueryAll(query string, args ...interface{}) ([]T, error)

	// Relation Loading

	// LoadRelation loads a specific relation for an entity
	LoadRelation(entity T, relationName string) error
}

// NewORM creates a new ORM instance for the given entity type
func NewORM[T EntityWithMeta](db dbCore.IDataSource) IORM[T] {
	return New[T](db)
}
