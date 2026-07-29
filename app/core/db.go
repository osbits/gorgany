package core

import (
	"context"
	dbCore "github.com/osbits/gorgany/db/sql/core"

	"gorm.io/gorm"
)

// DbType represents the type of database connection
type DbType string

// Driver names as written under `databases.<name>.driver`. They are the keys of
// the driver registry in db/sql/driver; the framework's own drivers are registered
// by db/sql/driver/builtin.
//
// A `MongoDb DbType = "mongo"` constant used to sit here with no driver behind it. It
// was never registered, so `driver: mongo` failed at boot with "unknown driver" while
// the exported constant advertised support — and a Mongo driver cannot be added by
// registering one either, because dbCore.IDataSource is a SQL seam (it hands out a
// query builder and an executor that speak SQL). Implementing Mongo means a second
// datasource abstraction, not a registry entry, so the name is gone rather than
// promising something the seam cannot deliver. Register a driver through
// db/sql/driver.Register to add a SQL engine.
const (
	GormPostgreSQL DbType = "postgres_gorm"
	GormMySQL      DbType = "mysql_gorm"
)

// IDBContext defines the interface for database context management
type IDBContext interface {
	// RegisterDataSource registers a new database connection
	RegisterDataSource(name string, dataSource dbCore.IDataSource)
	// GetDataSource retrieves a database connection by name
	GetDataSource(name string) dbCore.IDataSource
}

// IDataSource defines the interface for database connections
// Deprecated, need to use dbCore.IDataSource
type IDataSource interface {
	// Driver returns the underlying database driver
	Driver() any
	// Builder returns a new query builder instance
	Builder() IQueryBuilder
	// WithContext creates a new connection with the given context
	WithContext(ctx context.Context) IDataSource
}

// IQueryBuilder defines the interface for building database queries
// Deprecated, need to use dbCore.IQueryBuilder
type IQueryBuilder interface {
	// Select specifies the fields to select
	Select(fields ...string) IQueryBuilder
	// From specifies the table to query
	From(table string) IQueryBuilder
	// FromSubquery uses a subquery as the data source
	FromSubquery(table any) IQueryBuilder
	// FromModel uses a model as the data source
	FromModel(model any) IQueryBuilder
	// Join adds an inner join clause
	Join(table any, left, operator, right string) IQueryBuilder
	// LeftJoin adds a left join clause
	LeftJoin(table any, left, operator, right string) IQueryBuilder
	// RightJoin adds a right join clause
	RightJoin(table any, left, operator, right string) IQueryBuilder
	// FullJoin adds a full join clause
	FullJoin(table any, left, operator, right string) IQueryBuilder
	// WhereEqual adds an equality condition
	WhereEqual(field string, value interface{}) IQueryBuilder
	// Where adds a custom condition
	Where(field string, operator string, value interface{}) IQueryBuilder
	// WhereClosure adds a complex condition using a closure
	// Deprecated. Use WhereAnd instead
	WhereClosure(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	// WhereIn adds an IN condition
	WhereIn(field string, values ...interface{}) IQueryBuilder
	// WhereNotIn adds a NOT IN condition
	WhereNotIn(field string, values ...interface{}) IQueryBuilder
	// WhereAnd adds an AND condition using a closure
	WhereAnd(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	// WhereOr adds an OR condition using a closure
	WhereOr(closure func(builder IQueryBuilder) IQueryBuilder) IQueryBuilder
	// Between adds a BETWEEN condition
	Between(field string, firstValue any, secondValue any) IQueryBuilder
	// OrderBy adds an ORDER BY clause
	OrderBy(field string, direction string) IQueryBuilder
	// Limit adds a LIMIT clause
	Limit(limit int) IQueryBuilder
	// Offset adds an OFFSET clause
	Offset(offset int) IQueryBuilder
	// GroupBy adds a GROUP BY clause
	GroupBy(field string) IQueryBuilder
	// Having adds a HAVING clause
	Having(rawStatement string, operator string, value any) IQueryBuilder
	// BuildSelect builds the SELECT part of the query
	BuildSelect() string
	// BuildJoin builds the JOIN part of the query
	BuildJoin() (string, []any)
	// BuildWhere builds the WHERE part of the query
	BuildWhere() (string, []any)
	// BuildOrder builds the ORDER BY part of the query
	BuildOrder() string
	// BuildLimit builds the LIMIT part of the query
	BuildLimit() string
	// BuildOffset builds the OFFSET part of the query
	BuildOffset() string
	// DeleteQuery builds a DELETE query
	DeleteQuery() (string, []any)
	// ToQuery builds the complete query
	ToQuery() (string, []any)
	// ToProcessedQuery returns the processed query string
	ToProcessedQuery() string
	// Get executes the query and retrieves a single result
	Get(dest any) error
	// Count executes the query and returns the count
	Count(dest *int64) error
	// List executes the query and retrieves multiple results
	List(dest any) error
	// Insert inserts a new record
	Insert(model any) error
	// Save saves or updates a record
	Save(model any) error
	// Delete deletes records matching the query
	Delete() error
	// DeleteModel deletes a specific model
	DeleteModel(model any) error
	// StartTransaction starts a new transaction
	StartTransaction() IQueryBuilder
	// CommitTransaction commits the current transaction
	CommitTransaction() IQueryBuilder
	// RollbackTransaction rolls back the current transaction
	RollbackTransaction() IQueryBuilder
	// Relation specifies a relation to load
	Relation(relation string) IQueryBuilder
	// Raw executes a raw SQL query
	Raw(sql string, values ...any) IQueryBuilder
	// Exec executes the query
	Exec() error
	// GetConnection returns the database connection
	GetConnection() IDataSource
	// CountRelation counts related records
	CountRelation(relation string) (int64, error)
	// ReplaceRelation replaces related records
	ReplaceRelation(relation string, values ...any) error
	// DeleteRelation deletes related records
	DeleteRelation(relation string, values ...any) error
	// ClearRelation clears all related records
	ClearRelation(relation string) error
	// AppendRelation appends related records
	AppendRelation(relation string, values ...any) error
	// LoadRelations loads specified relations
	LoadRelations(relation ...string) error
	// GetWhere returns the WHERE clause builder
	GetWhere() IWhere
	// SetAlias sets the table alias
	SetAlias(alias string) IQueryBuilder
	// AddMetaToModel adds metadata to the model
	AddMetaToModel(dest any, tableName string)
	// GetAlias returns the current table alias
	GetAlias() string
	// MergeBuilder merges another query builder
	MergeBuilder(builder IQueryBuilder) IQueryBuilder
}

// GormAssociation defines the interface for GORM associations
type GormAssociation interface {
	// Association returns a GORM association
	Association(association string) *gorm.Association
}

// IOrm defines the interface for ORM operations with a specific type
// Deprecated, need to use dbCore.IOrm
type IOrm[T any] interface {
	// Select specifies the fields to select
	Select(fields ...string) IOrm[T]
	// Join adds an inner join clause
	Join(table string, left, operator, right string) IOrm[T]
	// LeftJoin adds a left join clause
	LeftJoin(table string, left, operator, right string) IOrm[T]
	// RightJoin adds a right join clause
	RightJoin(table string, left, operator, right string) IOrm[T]
	// FullJoin adds a full join clause
	FullJoin(table string, left, operator, right string) IOrm[T]
	// WhereEqual adds an equality condition
	WhereEqual(field string, value interface{}) IOrm[T]
	// Where adds a custom condition
	Where(field string, operator string, value interface{}) IOrm[T]
	// WhereClosure adds a complex condition using a closure
	WhereClosure(closure func(builder IQueryBuilder) IQueryBuilder) IOrm[T]
	// WhereIn adds an IN condition
	WhereIn(field string, values ...interface{}) IOrm[T]
	// WhereAnd adds an AND condition using a closure
	WhereAnd(closure func(builder IQueryBuilder) IQueryBuilder) IOrm[T]
	// WhereOr adds an OR condition using a closure
	WhereOr(closure func(builder IQueryBuilder) IQueryBuilder) IOrm[T]
	// Between adds a BETWEEN condition
	Between(field string, firstValue any, secondValue any) IOrm[T]
	// OrderBy adds an ORDER BY clause
	OrderBy(field string, direction string) IOrm[T]
	// Relation specifies a relation to load
	Relation(relation string) IOrm[T]
	// Limit adds a LIMIT clause
	Limit(limit int) IOrm[T]
	// Offset adds an OFFSET clause
	Offset(offset int) IOrm[T]
	// GroupBy adds a GROUP BY clause
	GroupBy(field string) IOrm[T]
	// Having adds a HAVING clause
	Having(rawStatement string, operator string, value any) IOrm[T]
	// Get executes the query and retrieves a single result
	Get() (*T, error)
	// Count executes the query and returns the count
	Count() (int64, error)
	// List executes the query and retrieves multiple results
	List() ([]*T, error)
	// Save saves or updates a record
	Save() error
	// CountRelation counts related records
	CountRelation(relation string) (int64, error)
	// ReplaceRelation replaces related records
	ReplaceRelation(relation string, values ...any) error
	// DeleteRelation deletes related records
	DeleteRelation(relation string, values ...any) error
	// ClearRelation clears all related records
	ClearRelation(relation string) error
	// AppendRelation appends related records
	AppendRelation(relation string, values ...any) error
	// LoadRelations loads specified relations
	LoadRelations(relations ...string) error
	// MergeBuilder merges another query builder
	MergeBuilder(builder IQueryBuilder) IOrm[T]
	// Delete deletes records matching the query
	Delete() error
	// ToQuery builds the complete query
	ToQuery() string
}

// DbConnectionNamer defines the interface for entities that specify their database connection
type DbConnectionNamer interface {
	// DbConnectionName returns the name of the database connection to use
	DbConnectionName() string
}

// IFrom defines the interface for building FROM clauses
type IFrom interface {
	// From specifies the table and alias
	From(table any, alias string)
	// ToQuery builds the FROM clause
	ToQuery() (string, []any)
}

// IJoin defines the interface for building JOIN clauses
type IJoin interface {
	// InnerJoin adds an inner join
	InnerJoin(table any, left, operator, right string)
	// LeftJoin adds a left join
	LeftJoin(table any, left, operator, right string)
	// RightJoin adds a right join
	RightJoin(table any, left, operator, right string)
	// FullJoin adds a full join
	FullJoin(table any, left, operator, right string)
	// ToQuery builds the JOIN clause
	ToQuery() (string, []any)
}

// IWhere defines the interface for building WHERE clauses
type IWhere interface {
	// AddCondition adds a simple condition
	AddCondition(column string, operator string, value any)
	// AddNestedCondition adds a nested condition
	AddNestedCondition(connectorOperator string, nestedWhere IWhere)
	// ToQuery builds the WHERE clause
	ToQuery() (string, []any)
}

// IHaving defines the interface for building HAVING clauses
type IHaving interface {
	// AddItem adds a HAVING condition
	AddItem(rawStatement string, operator string, value any)
	// ToQuery builds the HAVING clause
	ToQuery() (string, []any)
}
