package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	v2 "git.qix.sx/gorgany/gorgany.git/db/sql/gorm/postgres/v2"
	"git.qix.sx/gorgany/gorgany.git/util"
	"gorm.io/gorm/schema"
)

// ORM provides generic ORM operations for any entity type
type ORM[T EntityWithMeta] struct {
	db dbCore.ISession `container:"inject"`
}

// New creates a new ORM instance for the given entity type
func New[T EntityWithMeta](db dbCore.ISession) *ORM[T] {
	return &ORM[T]{
		db: db,
	}
}

// Find finds an entity by its ID and returns it
func (o *ORM[T]) Find(id interface{}) (T, error) {
	// Create a new entity
	var entity T

	meta := &EntityMeta{
		PrimaryKey:    "id", // Default, will be overridden by schema if available
		LoadedColumns: make(map[string]bool),
		RelationMeta:  make(map[string]*RelationMeta),
	}

	// Try to use schema.Parse to get primary key information
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err == nil && len(entitySchema.PrimaryFieldDBNames) > 0 {
		// Update primary key in meta
		meta.PrimaryKey = entitySchema.PrimaryFieldDBNames[0]
	}

	rEntity := reflect.ValueOf(entity)
	indirectEntityType := util.IndirectType(rEntity.Type())

	tableName := ""
	if entitySchema != nil {
		tableName = entitySchema.Table
	} else {
		tableName = GetTableName(entity)
	}
	meta.TableName = tableName

	// Create a new builder or use the existing one
	if meta.QueryBuilder == nil {
		meta.QueryBuilder = o.db.Query()
	}

	// Build the query
	builder := meta.QueryBuilder
	primaryKey := meta.PrimaryKey
	if primaryKey == "" {
		primaryKey = "id"
		meta.PrimaryKey = primaryKey
	}

	sql, args := builder.From(tableName).Eq(primaryKey, id).Limit(1).ToSQL()

	meta.LastQuery = sql
	meta.LastArgs = args

	// Execute the query
	queryResult := o.db.Executor().FindRaw(context.Background(), &entity, sql, args...)
	if err != nil {
		return entity, fmt.Errorf("failed to execute Find in ORM for %s: %w", indirectEntityType.Name(), err)
	}

	// Only set metadata if the entity is not nil
	if !isNilValue(entity) {
		// Store the DB connection for later use
		meta.DataSource = o.db.DataSource()
		meta.QueryResult = &queryResult
		meta.IsLoaded = queryResult.Found

		// If rows were found, update other metadata
		if meta.IsLoaded {
			meta.IsDirty = false
		}

		entity.SetMeta(meta)
	}

	return entity, nil
}

// All retrieves all entities that match the query builder conditions
func (o *ORM[T]) All() ([]T, error) {
	// Create a slice to hold the results
	var entities []T

	// Create a sample entity to get metadata
	var sample T

	rSample := reflect.ValueOf(sample)
	indirectSampleType := util.IndirectType(rSample.Type())

	// Get table name
	tableName := GetTableName(sample)

	builder := o.db.Query()
	if tableName != "" {
		builder = builder.From(tableName)
	}

	// Convert to SQL
	sql, args := builder.ToSQL()

	// Execute the query
	queryResult := o.db.Executor().FindRaw(context.Background(), &entities, sql, args...)
	if queryResult.Error != nil {
		return entities, fmt.Errorf("failed to execute All in ORM for %s: %w", indirectSampleType.Name(), queryResult.Error)
	}

	// Check if any results were found
	if len(entities) == 0 || !queryResult.Found {
		// No results found, return empty slice
		return entities, nil
	}

	// Try to use schema.Parse to get primary key information
	primaryKey := "id"
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(sample, schemaCache, schema.NamingStrategy{})
	if err == nil && len(entitySchema.PrimaryFieldDBNames) > 0 {
		// Update primary key in meta
		primaryKey = entitySchema.PrimaryFieldDBNames[0]
	}

	// Update metadata for each entity
	for i := range entities {
		// Skip nil entities
		if isNilValue(entities[i]) {
			continue
		}

		entityMeta := entities[i].GetMeta()
		if entityMeta == nil {
			entityMeta = &EntityMeta{
				TableName:     tableName,
				PrimaryKey:    primaryKey,
				IsLoaded:      true, // We know entities were found if we're here
				IsDirty:       false,
				LoadedColumns: make(map[string]bool),
				RelationMeta:  make(map[string]*RelationMeta),
				DataSource:    o.db.DataSource(),
				LastQuery:     sql,
				LastArgs:      args,
				QueryResult:   &queryResult,
			}
			entities[i].SetMeta(entityMeta)
		} else {
			entityMeta.IsLoaded = true // We know entities were found if we're here
			entityMeta.IsDirty = false
			entityMeta.DataSource = o.db.DataSource()
			entityMeta.LastQuery = sql
			entityMeta.LastArgs = args
			entityMeta.QueryResult = &queryResult
		}
	}

	return entities, nil
}

// RawQuery executes a raw SQL query and returns the first result
// WARNING: To prevent SQL injection, never construct the query string with user input.
// Always use parameterized queries with the args parameter for user input.
func (o *ORM[T]) RawQuery(query string, args ...interface{}) (T, error) {
	// Create a new entity
	var entity T

	// Initialize metadata
	meta := &EntityMeta{
		PrimaryKey:    "id",
		LoadedColumns: make(map[string]bool),
		RelationMeta:  make(map[string]*RelationMeta),
	}

	// Store query info
	meta.LastQuery = query
	meta.LastArgs = args

	// Execute the query
	queryResult := o.db.Executor().FindRaw(context.Background(), &entity, query, args...)
	if queryResult.Error != nil {
		return entity, fmt.Errorf("failed to execute RawQuery in ORM: %w", queryResult.Error)
	}

	// Only set metadata if the entity is not nil
	if !isNilValue(entity) {
		// Store the DB connection for later use
		meta.DataSource = o.db.DataSource()
		meta.QueryResult = &queryResult
		meta.IsLoaded = queryResult.Found

		// If rows were found, update other metadata
		if meta.IsLoaded {
			meta.IsDirty = false
		}

		// If table name not set, try to get it
		if meta.TableName == "" {
			meta.TableName = GetTableName(entity)
		}

		entity.SetMeta(meta)
	}

	return entity, nil
}

// RawQueryAll executes a raw SQL query and returns all results
// WARNING: To prevent SQL injection, never construct the query string with user input.
// Always use parameterized queries with the args parameter for user input.
func (o *ORM[T]) RawQueryAll(query string, args ...interface{}) ([]T, error) {
	// Create a slice to hold the results
	var entities []T

	// Execute the query
	queryResult := o.db.Executor().FindRaw(context.Background(), &entities, query, args...)
	if queryResult.Error != nil {
		return entities, fmt.Errorf("failed to execute RawQueryAll in ORM: %w", queryResult.Error)
	}

	// Check if any results were found
	if len(entities) == 0 || !queryResult.Found {
		// No results found, return empty slice
		return entities, nil
	}

	// Update metadata for each entity
	for i := range entities {
		// Skip nil entities
		if isNilValue(entities[i]) {
			continue
		}

		meta := entities[i].GetMeta()
		if meta == nil {
			meta = &EntityMeta{
				IsLoaded:      true, // We know entities were found if we're here
				IsDirty:       false,
				PrimaryKey:    "id",
				LoadedColumns: make(map[string]bool),
				RelationMeta:  make(map[string]*RelationMeta),
				DataSource:    o.db.DataSource(),
				LastQuery:     query,
				LastArgs:      args,
				QueryResult:   &queryResult,
			}
			entities[i].SetMeta(meta)
		} else {
			meta.IsLoaded = true // We know entities were found if we're here
			meta.IsDirty = false
			meta.DataSource = o.db.DataSource()
			meta.LastQuery = query
			meta.LastArgs = args
			meta.QueryResult = &queryResult
		}

		// If table name not set, try to get it
		if meta.TableName == "" {
			meta.TableName = GetTableName(entities[i])
		}
	}

	return entities, nil
}

// Count returns the count of entities that match the query builder conditions
func (o *ORM[T]) Count() (int64, error) {
	// Create a sample entity to get metadata
	var sample T

	tableName := GetTableName(sample)

	builder := v2.NewBuilder()
	if tableName != "" {
		builder.From(tableName)
	}

	// Create a copy of the builder for the count query
	sql, args := builder.Select("COUNT(*)").ToSQL()

	// Execute the query
	count, err := o.db.Executor().CountRaw(context.Background(), sql, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute Count in ORM: %w", err)
	}

	return count, nil
}

// Refresh reloads the entity from the database
func (o *ORM[T]) Refresh(entity T) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}

	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:    "id", // Default, will be overridden by schema if available
			LoadedColumns: make(map[string]bool),
			RelationMeta:  make(map[string]*RelationMeta),
		}
		entity.SetMeta(meta)
	}

	// Try to use schema.Parse to get primary key information
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err == nil && len(entitySchema.PrimaryFieldDBNames) > 0 {
		// Update primary key in meta
		meta.PrimaryKey = entitySchema.PrimaryFieldDBNames[0]
	}

	// Get the table name
	tableName := meta.TableName
	if tableName == "" {
		if entitySchema != nil {
			tableName = entitySchema.Table
		} else {
			tableName = GetTableName(entity)
		}
		meta.TableName = tableName
	}

	// Get the primary key and its value
	primaryKey := meta.PrimaryKey
	if primaryKey == "" {
		primaryKey = "id"
		meta.PrimaryKey = primaryKey
	}

	// Extract the primary key value using reflection
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// Find the field corresponding to the primary key
	field := val.FieldByNameFunc(func(name string) bool {
		// This is a simplified approach - in practice you'd use struct tags or other means
		return strings.ToLower(name) == strings.ToLower(primaryKey)
	})

	if !field.IsValid() {
		return fmt.Errorf("primary key field '%s' not found", primaryKey)
	}

	// Get the primary key value
	pkValue := field.Interface()

	// Find the entity by ID
	refreshedEntity, err := o.Find(pkValue)
	if err != nil {
		return err
	}

	// Copy the refreshed entity's data to the original entity
	refreshedVal := reflect.ValueOf(refreshedEntity)
	entityVal := reflect.ValueOf(entity)

	if refreshedVal.Kind() == reflect.Ptr {
		refreshedVal = refreshedVal.Elem()
	}
	if entityVal.Kind() == reflect.Ptr {
		entityVal = entityVal.Elem()
	}

	// Copy all fields except Meta
	for i := 0; i < refreshedVal.NumField(); i++ {
		field := refreshedVal.Type().Field(i)
		if field.Name != "Meta" && field.Name != "BaseEntity" {
			entityVal.FieldByName(field.Name).Set(refreshedVal.Field(i))
		}
	}

	// Update metadata
	meta.IsLoaded = true
	meta.IsDirty = false
	meta.DataSource = o.db.DataSource()

	return nil
}

// AllByQuery executes the given query builder and returns all results
func (o *ORM[T]) AllByQuery(qb dbCore.IQueryBuilder) ([]T, error) {
	var entities []T

	sql, args := qb.ToSQL()

	queryResult := o.db.Executor().FindRaw(context.Background(), &entities, sql, args...)
	if queryResult.Error != nil {
		return entities, fmt.Errorf("failed to execute AllByQuery in ORM: %w", queryResult.Error)
	}

	// Update metadata for each entity
	for i := range entities {
		if isNilValue(entities[i]) {
			continue
		}
		meta := entities[i].GetMeta()
		if meta == nil {
			meta = &EntityMeta{
				IsLoaded:      true,
				IsDirty:       false,
				PrimaryKey:    "id",
				LoadedColumns: make(map[string]bool),
				RelationMeta:  make(map[string]*RelationMeta),
				DataSource:    o.db.DataSource(),
				LastQuery:     sql,
				LastArgs:      args,
				QueryResult:   &queryResult,
			}
			entities[i].SetMeta(meta)
		} else {
			meta.IsLoaded = true
			meta.IsDirty = false
			meta.DataSource = o.db.DataSource()
			meta.LastQuery = sql
			meta.LastArgs = args
			meta.QueryResult = &queryResult
		}
	}

	return entities, nil
}

// FirstByQuery executes the given query builder and returns the first result
func (o *ORM[T]) FirstByQuery(qb dbCore.IQueryBuilder) (T, error) {
	var entity T

	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err != nil {
		return entity, fmt.Errorf("failed to parse entity schema in ORM: %w", err)
	}

	tableName := ""
	if entitySchema != nil {
		tableName = entitySchema.Table
	} else {
		tableName = GetTableName(entity)
	}

	qb = qb.Limit(1).From(tableName)
	sql, args := qb.ToSQL()

	queryResult := o.db.Executor().FindRaw(context.Background(), &entity, sql, args...)
	if queryResult.Error != nil {
		return entity, fmt.Errorf("failed to execute FirstByQuery in ORM: %w", queryResult.Error)
	}

	if !isNilValue(entity) {
		meta := entity.GetMeta()
		if meta == nil {
			meta = &EntityMeta{
				IsLoaded:      queryResult.Found,
				IsDirty:       false,
				PrimaryKey:    "id",
				LoadedColumns: make(map[string]bool),
				RelationMeta:  make(map[string]*RelationMeta),
				DataSource:    o.db.DataSource(),
				LastQuery:     sql,
				LastArgs:      args,
				QueryResult:   &queryResult,
			}
			entity.SetMeta(meta)
		} else {
			meta.IsLoaded = queryResult.Found
			meta.IsDirty = false
			meta.DataSource = o.db.DataSource()
			meta.LastQuery = sql
			meta.LastArgs = args
			meta.QueryResult = &queryResult
		}
	}

	return entity, nil
}

// CountByQuery executes the given query builder and returns the count
func (o *ORM[T]) CountByQuery(qb dbCore.IQueryBuilder) (int64, error) {
	// Set the builder to select COUNT(*)
	var entity T

	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err != nil {
		return 0, fmt.Errorf("failed to parse entity schema in ORM: %w", err)
	}

	tableName := ""
	if entitySchema != nil {
		tableName = entitySchema.Table
	} else {
		tableName = GetTableName(entity)
	}

	qb = qb.Select("COUNT(*)").From(tableName)
	sql, args := qb.ToSQL()

	count, err := o.db.Executor().CountRaw(context.Background(), sql, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute CountByQuery in ORM: %w", err)
	}

	return count, nil
}
