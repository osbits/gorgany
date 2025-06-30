// Package orm provides an Object-Relational Mapping (ORM) implementation
package orm

import (
	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
)

// IORM defines the interface for ORM operations
// T must implement EntityWithMeta
type IORM[T EntityWithMeta] interface {
	// Basic CRUD Operations

	// Find finds an entity by its ID and returns it
	Find(id interface{}) (T, error)

	// Save saves an entity (creates if new, updates if existing)
	Save(entity T) error

	// Create creates a new entity
	Create(entity T) error

	// Update updates an existing entity
	Update(entity T) error

	// Delete deletes an entity
	Delete(entity T) error

	// Refresh reloads an entity from the database
	Refresh(entity T) error

	// Stateless Query Execution

	// AllByQuery executes the given query builder and returns all results
	AllByQuery(qb dbCore.IQueryBuilder) ([]T, error)

	// FirstByQuery executes the given query builder and returns the first result
	FirstByQuery(qb dbCore.IQueryBuilder) (T, error)

	// CountByQuery executes the given query builder and returns the count
	CountByQuery(qb dbCore.IQueryBuilder) (int64, error)

	// Raw Query Execution

	// RawQuery executes a raw SQL query and returns the first result
	RawQuery(query string, args ...interface{}) (T, error)

	// RawQueryAll executes a raw SQL query and returns all results
	RawQueryAll(query string, args ...interface{}) ([]T, error)

	// Relation Loading

	// LoadRelation loads a specific relation for an entity
	LoadRelation(entity T, relationName string) error
}
