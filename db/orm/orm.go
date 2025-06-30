package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	v2 "git.qix.sx/gorgany/gorgany.git/db/sql/gorm/postgres/v2"
	"git.qix.sx/gorgany/gorgany.git/util"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// EntityMeta stores metadata about an entity
type EntityMeta struct {
	// Basic info
	TableName    string // Table name in the database
	PrimaryKey   string // Primary key column name, determined from schema if available, defaults to "id"
	DatabaseName string

	// State tracking
	IsLoaded      bool // True if the entity was loaded from the database and found
	IsNew         bool
	IsDirty       bool
	LoadedColumns map[string]bool

	// Query info
	LastQuery    string
	LastArgs     []interface{}
	QueryBuilder dbCore.IQueryBuilder
	QueryResult  *dbCore.QueryResult // Stores metadata about the last query execution

	// Relationships
	LoadedRelations map[string]bool

	// Gorm internals
	DataSource dbCore.IDataSource
}

// SetQueryResult sets the query result metadata
func (e *EntityMeta) SetQueryResult(result *dbCore.QueryResult) {
	e.QueryResult = result
	// Update IsLoaded based on whether any rows were found
	e.IsLoaded = result.Found
}

// GetQueryResult gets the query result metadata
func (e *EntityMeta) GetQueryResult() *dbCore.QueryResult {
	return e.QueryResult
}

// EntityWithMeta is an interface that all entities with metadata must implement
type EntityWithMeta interface {
	GetMeta() *EntityMeta
	SetMeta(*EntityMeta)
}

// BaseEntity provides the Meta field and implementation of EntityWithMeta
type BaseEntity struct {
	Meta *EntityMeta `gorm:"-"` // Exclude from database operations
}

// GetMeta returns the entity's metadata
func (e *BaseEntity) GetMeta() *EntityMeta {
	if e.Meta == nil {
		e.Meta = &EntityMeta{
			PrimaryKey:      "id",
			IsNew:           true,
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
		}
	}
	return e.Meta
}

// SetMeta sets the entity's metadata
func (e *BaseEntity) SetMeta(meta *EntityMeta) {
	e.Meta = meta
}

// Hook types
const (
	BeforeSave   = "before_save"
	AfterSave    = "after_save"
	BeforeCreate = "before_create"
	AfterCreate  = "after_create"
	BeforeUpdate = "before_update"
	AfterUpdate  = "after_update"
	BeforeDelete = "before_delete"
	AfterDelete  = "after_delete"
)

// EntityNotFound is returned when an entity can't be found
var EntityNotFound = errors.New("entity not found")

// ORM provides generic ORM operations for any entity type
type ORM[T EntityWithMeta] struct {
	db dbCore.ISession
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
		PrimaryKey:      "id", // Default, will be overridden by schema if available
		LoadedColumns:   make(map[string]bool),
		LoadedRelations: make(map[string]bool),
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

	// Get table name - either from meta or by asking GORM
	tableName := meta.TableName
	if tableName == "" {
		if entitySchema != nil {
			tableName = entitySchema.Table
		} else {
			namer := schema.NamingStrategy{}
			tableName = namer.TableName(indirectEntityType.Name())
		}
		meta.TableName = tableName
	}

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
			meta.IsNew = false
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
	meta := sample.GetMeta()

	rSample := reflect.ValueOf(sample)
	indirectSampleType := util.IndirectType(rSample.Type())

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		namer := schema.NamingStrategy{}
		tableName = namer.TableName(indirectSampleType.Name())
		meta.TableName = tableName
	}

	// Create builder if needed
	builder := meta.QueryBuilder
	if builder == nil {
		builder = o.db.Query()
		if tableName != "" {
			builder.From(tableName)
		}
	}

	// Convert to SQL
	sql, args := builder.ToSQL()
	meta.LastQuery = sql
	meta.LastArgs = args

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

	// Update metadata for each entity
	for i := range entities {
		// Skip nil entities
		if isNilValue(entities[i]) {
			continue
		}

		entityMeta := entities[i].GetMeta()
		if entityMeta == nil {
			entityMeta = &EntityMeta{
				TableName:       tableName,
				PrimaryKey:      meta.PrimaryKey,
				IsLoaded:        true, // We know entities were found if we're here
				IsNew:           false,
				IsDirty:         false,
				LoadedColumns:   make(map[string]bool),
				LoadedRelations: make(map[string]bool),
				DataSource:      o.db.DataSource(),
				LastQuery:       sql,
				LastArgs:        args,
				QueryResult:     &queryResult,
			}
			entities[i].SetMeta(entityMeta)
		} else {
			entityMeta.IsLoaded = true // We know entities were found if we're here
			entityMeta.IsNew = false
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
		PrimaryKey:      "id",
		LoadedColumns:   make(map[string]bool),
		LoadedRelations: make(map[string]bool),
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
			meta.IsNew = false
			meta.IsDirty = false
		}

		// If table name not set, try to get it
		if meta.TableName == "" {
			rEntity := reflect.ValueOf(entity)
			indirectEntityType := util.IndirectType(rEntity.Type())
			namer := schema.NamingStrategy{}
			meta.TableName = namer.TableName(indirectEntityType.Name())
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
				IsLoaded:        true, // We know entities were found if we're here
				IsNew:           false,
				IsDirty:         false,
				PrimaryKey:      "id",
				LoadedColumns:   make(map[string]bool),
				LoadedRelations: make(map[string]bool),
				DataSource:      o.db.DataSource(),
				LastQuery:       query,
				LastArgs:        args,
				QueryResult:     &queryResult,
			}
			entities[i].SetMeta(meta)
		} else {
			meta.IsLoaded = true // We know entities were found if we're here
			meta.IsNew = false
			meta.IsDirty = false
			meta.DataSource = o.db.DataSource()
			meta.LastQuery = query
			meta.LastArgs = args
			meta.QueryResult = &queryResult
		}

		// If table name not set, try to get it
		if meta.TableName == "" {
			rEntity := reflect.ValueOf(entities[i])
			indirectEntityType := util.IndirectType(rEntity.Type())
			namer := schema.NamingStrategy{}
			meta.TableName = namer.TableName(indirectEntityType.Name())
		}
	}

	return entities, nil
}

// Save saves an entity (creates if new, updates if existing)
func (o *ORM[T]) Save(entity T) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}

	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:      "id",
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
			IsNew:           true,
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

	// Execute hooks if entity implements them
	if hook, ok := any(entity).(interface{ BeforeSave(*gorm.DB) error }); ok {
		if err := hook.BeforeSave(nil); err != nil {
			return err
		}
	}

	// Check if this is a new entity
	if meta.IsNew {
		// This is a create operation
		if hook, ok := any(entity).(interface{ BeforeCreate(*gorm.DB) error }); ok {
			if err := hook.BeforeCreate(nil); err != nil {
				return err
			}
		}

		// Execute create operation
		queryRes := o.db.Executor().Find(context.Background(), buildInsertQuery(entity, meta), entity)
		if queryRes.Error != nil {
			return fmt.Errorf("failed to create entity: %w", queryRes.Error)
		}

		// Update metadata
		meta.IsNew = false
		meta.IsLoaded = true
		meta.IsDirty = false
		meta.DataSource = o.db.DataSource()

		// Execute after hooks
		if hook, ok := any(entity).(interface{ AfterCreate(*gorm.DB) error }); ok {
			if err := hook.AfterCreate(nil); err != nil {
				return err
			}
		}
	} else {
		// This is an update operation
		if hook, ok := any(entity).(interface{ BeforeUpdate(*gorm.DB) error }); ok {
			if err := hook.BeforeUpdate(nil); err != nil {
				return err
			}
		}

		// Execute update operation
		queryResult := o.db.Executor().Exec(context.Background(), buildUpdateQuery(entity, meta))
		if queryResult.Error != nil {
			return fmt.Errorf("failed to update entity: %w", queryResult.Error)
		}

		// Update metadata
		meta.IsDirty = false

		// Execute after hooks
		if hook, ok := any(entity).(interface{ AfterUpdate(*gorm.DB) error }); ok {
			if err := hook.AfterUpdate(nil); err != nil {
				return err
			}
		}
	}

	// Execute after hooks
	if hook, ok := any(entity).(interface{ AfterSave(*gorm.DB) error }); ok {
		if err := hook.AfterSave(nil); err != nil {
			return err
		}
	}

	return nil
}

// Create creates a new entity
func (o *ORM[T]) Create(entity T) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}

	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:      "id",
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
			IsNew:           true,
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

	// Always set IsNew for Create operation
	meta.IsNew = true

	// Execute hooks if entity implements them
	if hook, ok := any(entity).(interface{ BeforeSave(*gorm.DB) error }); ok {
		if err := hook.BeforeSave(nil); err != nil {
			return err
		}
	}

	if hook, ok := any(entity).(interface{ BeforeCreate(*gorm.DB) error }); ok {
		if err := hook.BeforeCreate(nil); err != nil {
			return err
		}
	}

	// Extract entity information using reflection
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	namer := schema.NamingStrategy{}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		rEntity := reflect.ValueOf(entity)
		indirectEntityType := util.IndirectType(rEntity.Type())
		tableName = namer.TableName(indirectEntityType.Name())
		meta.TableName = tableName
	}

	// Create a builder for the INSERT query
	var builder dbCore.IQueryBuilder
	builder = v2.NewBuilder()

	builder = builder.Insert(tableName)

	// Extract field values using our new function that supports embedded structs
	columns, values := extractFieldsForInsert(val, meta)

	builder = builder.Columns(columns...).Values(values...)

	var returningCols []string
	seen := make(map[string]struct{})

	for _, f := range entitySchema.PrimaryFields {
		if _, ok := seen[f.DBName]; !ok {
			returningCols = append(returningCols, f.DBName)
			seen[f.DBName] = struct{}{}
		}
	}
	for _, f := range entitySchema.Fields {
		if f.AutoIncrement || f.HasDefaultValue {
			if _, ok := seen[f.DBName]; !ok {
				returningCols = append(returningCols, f.DBName)
				seen[f.DBName] = struct{}{}
			}
		}
	}

	builder = builder.Returning(returningCols...)

	// Execute the query
	result := map[string]interface{}{}
	queryRes := o.db.Executor().Find(context.Background(), builder, &result)
	if queryRes.Error != nil {
		return fmt.Errorf("failed to create entity: %w", queryRes.Error)
	}

	destVal := reflect.Indirect(reflect.ValueOf(entity))
	for _, col := range returningCols {
		if val, ok := result[col]; ok {
			if sf := entitySchema.LookUpField(col); sf != nil {
				// Set — это schema.Field.Set, он правильно обходит вложенные поля
				sf.Set(context.Background(), destVal, val)
			}
		}
	}

	// Update metadata
	meta.IsNew = false
	meta.IsLoaded = true
	meta.IsDirty = false
	meta.DataSource = o.db.DataSource()

	// Execute after hooks
	if hook, ok := any(entity).(interface{ AfterCreate(*gorm.DB) error }); ok {
		if err := hook.AfterCreate(nil); err != nil {
			return err
		}
	}

	if hook, ok := any(entity).(interface{ AfterSave(*gorm.DB) error }); ok {
		if err := hook.AfterSave(nil); err != nil {
			return err
		}
	}

	return nil
}

// Update updates an existing entity
func (o *ORM[T]) Update(entity T) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}

	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:      "id",
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
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

	// Mark as not new for update operation
	meta.IsNew = false

	// Execute hooks if entity implements them
	if hook, ok := any(entity).(interface{ BeforeSave(*gorm.DB) error }); ok {
		if err := hook.BeforeSave(nil); err != nil {
			return err
		}
	}

	if hook, ok := any(entity).(interface{ BeforeUpdate(*gorm.DB) error }); ok {
		if err := hook.BeforeUpdate(nil); err != nil {
			return err
		}
	}

	// Extract entity information using reflection
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		rEntity := reflect.ValueOf(entity)
		indirectEntityType := util.IndirectType(rEntity.Type())
		namer := schema.NamingStrategy{}
		tableName = namer.TableName(indirectEntityType.Name())
		meta.TableName = tableName
	}

	// Create a builder for the UPDATE query
	builder := o.db.Query().Update(tableName)

	// Extract field values using our new function that supports embedded structs
	pkValue, updateFields := extractFieldsForUpdate(val, meta)

	// Add fields to SET clause
	for columnName, value := range updateFields {
		builder = builder.Set(columnName, value)
	}

	// Check primary key
	if pkValue == nil || isZeroValue(pkValue) {
		return fmt.Errorf("cannot update entity with zero primary key value")
	}

	// Add WHERE clause for primary key
	builder = builder.Where(&dbCore.BinaryCondition{
		Left:     meta.PrimaryKey,
		Operator: "=",
		Right:    pkValue,
	})

	// Execute the query
	queryRes := o.db.Executor().Exec(context.Background(), builder)
	if queryRes.Error != nil {
		return fmt.Errorf("failed to update entity: %w", queryRes.Error)
	}

	// Update metadata
	meta.IsDirty = false
	meta.DataSource = o.db.DataSource()

	// Execute after hooks
	if hook, ok := any(entity).(interface{ AfterUpdate(*gorm.DB) error }); ok {
		if err := hook.AfterUpdate(nil); err != nil {
			return err
		}
	}

	if hook, ok := any(entity).(interface{ AfterSave(*gorm.DB) error }); ok {
		if err := hook.AfterSave(nil); err != nil {
			return err
		}
	}

	return nil
}

// Delete deletes an entity
func (o *ORM[T]) Delete(entity T) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}

	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:      "id", // Default, will be overridden by schema if available
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
		}
		entity.SetMeta(meta)
	}

	pkValueFieldName := ""
	// Try to use schema.Parse to get primary key information
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err == nil && len(entitySchema.PrimaryFieldDBNames) > 0 {
		// Update primary key in meta
		meta.PrimaryKey = entitySchema.PrimaryFields[0].Name
		pkValueFieldName = entitySchema.PrimaryFields[0].Name
	}

	// Execute hooks if entity implements them
	if hook, ok := any(entity).(interface{ BeforeDelete(*gorm.DB) error }); ok {
		if err := hook.BeforeDelete(nil); err != nil {
			return err
		}
	}

	// Extract entity information using reflection
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		rEntity := reflect.ValueOf(entity)
		indirectEntityType := util.IndirectType(rEntity.Type())
		namer := schema.NamingStrategy{}
		tableName = namer.TableName(indirectEntityType.Name())
		meta.TableName = tableName
	}

	// Find the primary key value, checking both the main struct and embedded structs
	var pkValue any

	// First try to find the field directly
	pkField := val.FieldByName(pkValueFieldName)
	if pkField.IsValid() {
		pkValue = pkField.Interface()
	} else {
		// If not found directly, search in embedded structs
		pkValue = findFieldValueInEmbedded(val, pkValueFieldName)
	}

	if pkValue == nil || isZeroValue(pkValue) {
		return fmt.Errorf("cannot delete entity with zero primary key value")
	}

	// Create a builder for the DELETE query
	builder := o.db.Query().Delete(tableName)

	// Add WHERE clause for primary key
	builder = builder.Where(&dbCore.BinaryCondition{
		Left:     meta.PrimaryKey,
		Operator: "=",
		Right:    pkValue,
	})

	// Execute the query
	queryRes := o.db.Executor().Exec(context.Background(), builder)
	if queryRes.Error != nil {
		return fmt.Errorf("failed to delete entity: %w", queryRes.Error)
	}

	// Execute after hooks
	if hook, ok := any(entity).(interface{ AfterDelete(*gorm.DB) error }); ok {
		if err := hook.AfterDelete(nil); err != nil {
			return err
		}
	}

	return nil
}

// Count returns the count of entities that match the query builder conditions
func (o *ORM[T]) Count() (int64, error) {
	// Create a sample entity to get metadata
	var sample T

	rSample := reflect.ValueOf(sample)
	indirectSampleType := util.IndirectType(rSample.Type())
	namer := schema.NamingStrategy{}
	tableName := namer.TableName(indirectSampleType.Name())

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
			PrimaryKey:      "id", // Default, will be overridden by schema if available
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
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
			rEntity := reflect.ValueOf(entity)
			indirectEntityType := util.IndirectType(rEntity.Type())
			namer := schema.NamingStrategy{}
			tableName = namer.TableName(indirectEntityType.Name())
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
	meta.IsNew = false
	meta.IsDirty = false
	meta.DataSource = o.db.DataSource()

	return nil
}

// isPrimaryKeyAutoIncrement checks if a field is an auto-incrementing primary key
// by examining the struct field tags
func isPrimaryKeyAutoIncrement(field reflect.StructField) bool {
	gormTag := field.Tag.Get("gorm")
	if gormTag == "" {
		return false
	}

	parts := strings.Split(gormTag, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "autoIncrement" || part == "auto_increment" {
			return true
		}
		if part == "primaryKey" || part == "primary_key" {
			// Check field type - integer types are typically auto-incremented
			switch field.Type.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				return true
			}
		}
	}

	return false
}

// Helper function to build an INSERT query
func buildInsertQuery(entity interface{}, meta *EntityMeta) dbCore.IQueryBuilder {
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		rEntity := reflect.ValueOf(entity)
		indirectEntityType := util.IndirectType(rEntity.Type())
		namer := schema.NamingStrategy{}
		tableName = namer.TableName(indirectEntityType.Name())
		meta.TableName = tableName
	}

	// Create a builder for the INSERT query
	var builder dbCore.IQueryBuilder
	builder = v2.NewBuilder()

	builder = builder.Insert(tableName)

	// Extract field values
	columns, values := extractFieldsForInsert(val, meta)

	builder = builder.
		Columns(columns...).
		Values(values...).
		Returning(meta.PrimaryKey)

	return builder
}

// extractFieldsForInsert extracts fields from a struct and its embedded structs for INSERT operations
// This function supports embedded structs by recursively extracting fields from them.
// Fields from embedded structs are only included if they are not overridden in the main struct.
func extractFieldsForInsert(val reflect.Value, meta *EntityMeta) ([]string, []interface{}) {
	var columns []string
	var values []interface{}

	// Track column names to avoid duplicates (fields in the main struct override embedded fields)
	columnMap := make(map[string]bool)

	// Parse the entity to get schema information
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(val.Interface(), schemaCache, schema.NamingStrategy{})

	// First process the main struct fields
	extractFieldsFromStruct(val, meta, &columns, &values, columnMap, false, entitySchema, err == nil)

	return columns, values
}

// extractFieldsFromStruct recursively extracts fields from a struct and its embedded structs for INSERT operations
// This function handles both the main struct fields and fields from embedded structs.
// It recursively processes embedded structs and ensures that fields from embedded structs
// are only included if they are not overridden in the main struct.
func extractFieldsFromStruct(val reflect.Value, meta *EntityMeta, columns *[]string, values *[]interface{}, columnMap map[string]bool, isEmbedded bool, entitySchema *schema.Schema, hasSchema bool) {
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := val.Type().Field(i)

		// Skip unexported fields
		if !field.CanInterface() {
			continue
		}

		// Handle embedded structs
		if fieldType.Anonymous && util.IndirectType(fieldType.Type).Kind() == reflect.Struct {
			// Skip the BaseEntity or Meta field
			if fieldType.Name == "BaseEntity" || fieldType.Name == "Meta" || fieldType.Tag.Get("gorm") == "-" {
				continue
			}

			// Recursively process embedded struct
			extractFieldsFromStruct(field, meta, columns, values, columnMap, true, entitySchema, hasSchema)
			continue
		}

		// Skip the Meta field or fields with gorm:"-"
		if fieldType.Name == "Meta" || fieldType.Tag.Get("gorm") == "-" {
			continue
		}

		// Get column name using GORM schema API if available
		var columnName string
		if hasSchema && entitySchema != nil {
			// Try to find the field in the schema
			if field, ok := entitySchema.FieldsByName[fieldType.Name]; ok {
				columnName = field.DBName
			} else {
				// Fallback to the field name if not found in schema
				columnName = fieldType.Name
			}
		} else {
			// Fallback to the old method if schema is not available
			columnName = fieldType.Name
			gormTag := fieldType.Tag.Get("gorm")
			if gormTag != "" {
				parts := strings.Split(gormTag, ";")
				for _, part := range parts {
					if strings.HasPrefix(part, "column:") {
						columnName = strings.TrimPrefix(part, "column:")
						break
					}
				}
			}
		}

		// Skip if this column is already included (main struct fields override embedded fields)
		if columnMap[columnName] {
			continue
		}

		// Skip zero values for auto-incrementing primary keys
		if columnName == meta.PrimaryKey && isPKAutoIncrement(val.Interface(), fieldType.Name) {
			if isZeroValue(field.Interface()) {
				continue
			}
		}

		// Add the field to the columns and values
		*columns = append(*columns, columnName)
		*values = append(*values, field.Interface())

		// Mark this column as included
		columnMap[columnName] = true
	}
}

// Helper function to build an UPDATE query
func buildUpdateQuery(entity interface{}, meta *EntityMeta) dbCore.IQueryBuilder {
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		rEntity := reflect.ValueOf(entity)
		indirectEntityType := util.IndirectType(rEntity.Type())
		namer := schema.NamingStrategy{}
		tableName = namer.TableName(indirectEntityType.Name())
		meta.TableName = tableName
	}

	// Create a builder for the UPDATE query
	builder := v2.NewBuilder().Update(tableName)

	// Extract field values for update
	pkValue, updateFields := extractFieldsForUpdate(val, meta)

	// Check primary key
	if pkValue == nil || isZeroValue(pkValue) {
		return nil // Cannot update without primary key
	}

	// Add fields to SET clause
	for columnName, value := range updateFields {
		builder = builder.Set(columnName, value)
	}

	// Add WHERE clause for primary key
	builder = builder.Where(&dbCore.BinaryCondition{
		Left:     meta.PrimaryKey,
		Operator: "=",
		Right:    pkValue,
	})

	return builder
}

// extractFieldsForUpdate extracts fields from a struct and its embedded structs for UPDATE operations
// This function supports embedded structs by recursively extracting fields from them.
// Fields from embedded structs are only included if they are not overridden in the main struct.
// It returns the primary key value and a map of column names to field values for the UPDATE operation.
func extractFieldsForUpdate(val reflect.Value, meta *EntityMeta) (interface{}, map[string]interface{}) {
	var pkValue interface{}
	updateFields := make(map[string]interface{})

	// Track column names to avoid duplicates (fields in the main struct override embedded fields)
	columnMap := make(map[string]bool)

	// Parse the entity to get schema information
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(val.Interface(), schemaCache, schema.NamingStrategy{})

	// Process the struct and its embedded structs
	extractUpdateFieldsFromStruct(val, meta, &pkValue, updateFields, columnMap, false, entitySchema, err == nil)

	return pkValue, updateFields
}

// extractUpdateFieldsFromStruct recursively extracts fields from a struct and its embedded structs for UPDATE operations
// This function handles both the main struct fields and fields from embedded structs.
// It recursively processes embedded structs and ensures that fields from embedded structs
// are only included if they are not overridden in the main struct.
// It separates the primary key field from other fields, storing the primary key value in pkValue
// and other field values in the updateFields map.
func extractUpdateFieldsFromStruct(val reflect.Value, meta *EntityMeta, pkValue *interface{}, updateFields map[string]interface{}, columnMap map[string]bool, isEmbedded bool, entitySchema *schema.Schema, hasSchema bool) {
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		fieldType := val.Type().Field(i)

		// Skip unexported fields
		if !field.CanInterface() {
			continue
		}

		// Handle embedded structs
		if fieldType.Anonymous && util.IndirectType(fieldType.Type).Kind() == reflect.Struct {
			// Skip the BaseEntity or Meta field
			if fieldType.Name == "BaseEntity" || fieldType.Name == "Meta" || fieldType.Tag.Get("gorm") == "-" {
				continue
			}

			// Recursively process embedded struct
			extractUpdateFieldsFromStruct(field, meta, pkValue, updateFields, columnMap, true, entitySchema, hasSchema)
			continue
		}

		// Skip the Meta field or fields with gorm:"-"
		if fieldType.Name == "Meta" || fieldType.Tag.Get("gorm") == "-" {
			continue
		}

		// Get column name using GORM schema API if available
		var columnName string
		if hasSchema && entitySchema != nil {
			// Try to find the field in the schema
			if field, ok := entitySchema.FieldsByName[fieldType.Name]; ok {
				columnName = field.DBName
			} else {
				// Fallback to the field name if not found in schema
				columnName = fieldType.Name
			}
		} else {
			// Fallback to the old method if schema is not available
			columnName = fieldType.Name
			gormTag := fieldType.Tag.Get("gorm")
			if gormTag != "" {
				parts := strings.Split(gormTag, ";")
				for _, part := range parts {
					if strings.HasPrefix(part, "column:") {
						columnName = strings.TrimPrefix(part, "column:")
						break
					}
				}
			}
		}

		// Skip if this column is already included (main struct fields override embedded fields)
		if columnMap[columnName] {
			continue
		}

		// Save primary key value or add to update fields
		if columnName == meta.PrimaryKey {
			*pkValue = field.Interface()
		} else {
			updateFields[columnName] = field.Interface()
		}

		// Mark this column as included
		columnMap[columnName] = true
	}
}

// Helper function to check if a field is an auto-incrementing primary key
func isPKAutoIncrement(entity interface{}, fieldName string) bool {
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct {
		return false
	}

	// Try to find the field directly first
	field, found := val.Type().FieldByName(fieldName)
	if found {
		return isFieldAutoIncrement(field, val.FieldByName(fieldName))
	}

	// If not found directly, search in embedded structs
	return findAutoIncrementFieldInEmbedded(val, fieldName)
}

// Helper function to check if a field is auto-increment based on its tags and type
func isFieldAutoIncrement(field reflect.StructField, fieldValue reflect.Value) bool {
	gormTag := field.Tag.Get("gorm")
	if gormTag == "" {
		return false
	}

	// Check for explicit auto-increment tag
	parts := strings.Split(gormTag, ";")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "autoIncrement" || part == "auto_increment" {
			return true
		}
	}

	// Check if it's a primary key with integer type (likely auto-increment)
	isPrimary := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "primaryKey" || part == "primary_key" {
			isPrimary = true
			break
		}
	}

	if isPrimary && fieldValue.IsValid() {
		switch fieldValue.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return true
		}
	}

	return false
}

// Helper function to recursively search for a field in embedded structs
func findAutoIncrementFieldInEmbedded(val reflect.Value, fieldName string) bool {
	valType := val.Type()

	// Check all fields for embedded structs
	for i := 0; i < valType.NumField(); i++ {
		field := valType.Field(i)

		// Skip non-embedded fields
		if !field.Anonymous {
			continue
		}

		// Skip unexported fields
		if !val.Field(i).CanInterface() {
			continue
		}

		// Skip the BaseEntity or Meta field
		if field.Name == "BaseEntity" || field.Name == "Meta" || field.Tag.Get("gorm") == "-" {
			continue
		}

		// Check if this embedded struct has the field
		embeddedVal := val.Field(i)
		if embeddedVal.Kind() == reflect.Ptr {
			if embeddedVal.IsNil() {
				continue
			}
			embeddedVal = embeddedVal.Elem()
		}

		if embeddedVal.Kind() != reflect.Struct {
			continue
		}

		// Try to find the field in this embedded struct
		embeddedType := embeddedVal.Type()
		field, found := embeddedType.FieldByName(fieldName)
		if found {
			return isFieldAutoIncrement(field, embeddedVal.FieldByName(fieldName))
		}

		// Recursively search in nested embedded structs
		if findAutoIncrementFieldInEmbedded(embeddedVal, fieldName) {
			return true
		}
	}

	return false
}

// Helper function to check if a value is a zero value
func isZeroValue(v interface{}) bool {
	return reflect.ValueOf(v).IsZero()
}

// Helper function to check if a value is nil using reflection
func isNilValue(v interface{}) bool {
	if v == nil {
		return true
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// Helper function to find a field value by name in embedded structs
// This function recursively searches for a field by name in embedded structs.
// It returns the field value if found, or nil if not found.
// This is used by the Delete method to find the primary key field in embedded structs.
func findFieldValueInEmbedded(val reflect.Value, fieldName string) interface{} {
	valType := val.Type()

	// Check all fields for embedded structs
	for i := 0; i < valType.NumField(); i++ {
		field := valType.Field(i)

		// Skip non-embedded fields
		if !field.Anonymous {
			continue
		}

		// Skip unexported fields
		if !val.Field(i).CanInterface() {
			continue
		}

		// Skip the BaseEntity or Meta field
		if field.Name == "BaseEntity" || field.Name == "Meta" || field.Tag.Get("gorm") == "-" {
			continue
		}

		// Check if this embedded struct has the field
		embeddedVal := val.Field(i)
		if embeddedVal.Kind() == reflect.Ptr {
			if embeddedVal.IsNil() {
				continue
			}
			embeddedVal = embeddedVal.Elem()
		}

		if embeddedVal.Kind() != reflect.Struct {
			continue
		}

		// Try to find the field in this embedded struct
		fieldVal := embeddedVal.FieldByName(fieldName)
		if fieldVal.IsValid() {
			return fieldVal.Interface()
		}

		// Recursively search in nested embedded structs
		if result := findFieldValueInEmbedded(embeddedVal, fieldName); result != nil {
			return result
		}
	}

	return nil
}

// LoadRelation loads a specific relation for an entity
func (o *ORM[T]) LoadRelation(entity T, relationName string) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}

	meta := entity.GetMeta()
	if meta == nil {
		return errors.New("entity meta cannot be nil")
	}

	// Check if relation is already loaded
	if meta.LoadedRelations[relationName] {
		return nil // Already loaded
	}

	// Get entity value for reflection
	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	// Find the relation field
	relationField := entityValue.FieldByName(relationName)
	if !relationField.IsValid() {
		return fmt.Errorf("relation field %s not found", relationName)
	}

	// Try to use schema.Parse to get relation information
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err == nil {
		// If schema parsing succeeded, try to use it
		if relationship, exists := entitySchema.Relationships.Relations[relationName]; exists {
			// Get primary key value
			var pkValue interface{}
			var foreignKey, references string

			// Get primary key field and value
			if len(entitySchema.PrimaryFieldDBNames) > 0 {
				pkField := entityValue.FieldByName(entitySchema.PrimaryFields[0].Name)
				if pkField.IsValid() {
					pkValue = pkField.Interface()

					// Handle different relation types
					switch relationship.Type {
					case schema.HasOne, schema.HasMany:
						if len(relationship.References) > 0 {
							foreignKey = relationship.References[0].ForeignKey.DBName
							err = o.loadHasRelation(entity, relationName, relationField, foreignKey, pkValue, relationship)
							if err == nil {
								// Mark relation as loaded
								meta.LoadedRelations[relationName] = true
								return nil
							}
						}
					case schema.BelongsTo:
						if len(relationship.References) > 0 {
							foreignKey = relationship.References[0].ForeignKey.DBName
							err = o.loadBelongsToRelation(entity, relationName, relationField, foreignKey, pkValue, relationship)
							if err == nil {
								// Mark relation as loaded
								meta.LoadedRelations[relationName] = true
								return nil
							}
						}
					case schema.Many2Many:
						if relationship.JoinTable != nil && len(relationship.References) > 0 {
							joinTable := relationship.JoinTable.Name
							references = relationship.References[0].PrimaryKey.DBName
							err = o.loadManyToManyRelation(entity, relationName, relationField, joinTable, references, relationship.Field.Tag.Get("gorm"), relationship)
							if err == nil {
								// Mark relation as loaded
								meta.LoadedRelations[relationName] = true
								return nil
							}
						}
					}
				}
			}
		}
	}

	// Return error if schema parsing fails or relation not found
	if err != nil {
		return fmt.Errorf("failed to parse entity schema: %w", err)
	}

	// Check if the relationship exists in the schema
	if _, exists := entitySchema.Relationships.Relations[relationName]; !exists {
		return fmt.Errorf("relation '%s' not found in entity schema", relationName)
	}

	// If we reached here, it means the schema was parsed successfully but the relation loading failed
	return fmt.Errorf("failed to load relation '%s': schema information was incomplete or invalid", relationName)
}

// loadHasRelation loads hasOne or hasMany relations
func (o *ORM[T]) loadHasRelation(
	entity T,
	relationName string,
	relationField reflect.Value,
	foreignKey string,
	pkValue interface{},
	relationship *schema.Relationship,
) error {
	// Determine if it's a slice (hasMany) or single entity (hasOne)
	isSlice := relationField.Kind() == reflect.Slice

	// Get the related entity type
	var relatedEntityType reflect.Type
	if isSlice {
		relatedEntityType = relationField.Type().Elem()
		// If it's a slice of pointers, get the element type
		if relatedEntityType.Kind() == reflect.Ptr {
			relatedEntityType = relatedEntityType.Elem()
		}
	} else {
		relatedEntityType = relationField.Type()
		// If it's a pointer, get the element type
		if relatedEntityType.Kind() == reflect.Ptr {
			relatedEntityType = relatedEntityType.Elem()
		}
	}

	// Get table name and foreign key from relationship
	if relationship == nil || relationship.FieldSchema == nil {
		return fmt.Errorf("cannot load hasOne/hasMany relation '%s': relationship schema information is missing", relationName)
	}

	tableName := relationship.FieldSchema.Table
	if len(relationship.References) == 0 {
		return fmt.Errorf("cannot load hasOne/hasMany relation '%s': foreign key reference information is missing", relationName)
	}

	foreignKey = relationship.References[0].ForeignKey.DBName

	// Build query to load related entities
	var builder dbCore.IQueryBuilder
	builder = v2.NewBuilder()
	builder = builder.
		Select("*").
		From(tableName).
		Where(&dbCore.BinaryCondition{
			Left:     foreignKey,
			Operator: "=",
			Right:    pkValue,
		})

	// Execute query
	if isSlice {
		// Create a new slice to hold results
		sliceType := reflect.SliceOf(relationField.Type().Elem())
		resultSlice := reflect.New(sliceType).Elem()

		// Create a new slice to unmarshal into
		destSlice := reflect.New(reflect.SliceOf(reflect.TypeOf(reflect.New(relatedEntityType).Interface())))

		// Execute query to get all related entities
		queryRes := o.db.Executor().Find(context.Background(), builder, destSlice.Interface())
		if queryRes.Error != nil {
			return queryRes.Error
		}

		// Get the slice value
		destSliceVal := destSlice.Elem()

		// Copy elements to the result slice
		for i := 0; i < destSliceVal.Len(); i++ {
			item := destSliceVal.Index(i)

			// Append to result slice based on whether the relation field expects pointers or values
			if relationField.Type().Elem().Kind() == reflect.Ptr {
				resultSlice = reflect.Append(resultSlice, item)
			} else {
				resultSlice = reflect.Append(resultSlice, item.Elem())
			}
		}

		// Set the result slice to the relation field
		relationField.Set(resultSlice)
	} else {
		// Create a new element to unmarshal into
		elemType := relationField.Type()
		if elemType.Kind() == reflect.Ptr {
			elemType = elemType.Elem()
		}
		elem := reflect.New(elemType)

		// Execute query to get the related entity
		queryRes := o.db.Executor().Find(context.Background(), builder, elem.Interface())
		if queryRes.Error != nil {
			if queryRes.Error == sql.ErrNoRows {
				// No related entity found, leave the field as is
				return nil
			}
			return queryRes.Error
		}

		// Set the result to the relation field
		if relationField.Type().Kind() == reflect.Ptr {
			relationField.Set(elem)
		} else {
			relationField.Set(elem.Elem())
		}
	}

	return nil
}

// loadBelongsToRelation loads belongsTo relations
func (o *ORM[T]) loadBelongsToRelation(
	entity T,
	relationName string,
	relationField reflect.Value,
	foreignKey string,
	pkValue interface{},
	relationship *schema.Relationship,
) error {
	// Get the related entity type
	relatedEntityType := relationField.Type()
	if relatedEntityType.Kind() == reflect.Ptr {
		relatedEntityType = relatedEntityType.Elem()
	}

	// Get table name and foreign key from relationship
	if relationship == nil || relationship.FieldSchema == nil {
		return fmt.Errorf("cannot load belongsTo relation '%s': relationship schema information is missing", relationName)
	}

	tableName := relationship.FieldSchema.Table
	if len(relationship.References) == 0 {
		return fmt.Errorf("cannot load belongsTo relation '%s': foreign key reference information is missing", relationName)
	}

	foreignKey = relationship.References[0].ForeignKey.DBName
	primaryKey := relationship.References[0].PrimaryKey.DBName

	// Build query to load related entity
	var builder dbCore.IQueryBuilder
	builder = v2.NewBuilder()
	builder = builder.
		Select("*").
		From(tableName).
		Where(&dbCore.BinaryCondition{
			Left:     primaryKey, // Use the primary key from relationship or default to 'id'
			Operator: "=",
			Right:    pkValue,
		})

	// Create a new element to unmarshal into
	elemType := relationField.Type()
	if elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}
	elem := reflect.New(elemType)

	// Execute query to get the related entity
	queryRes := o.db.Executor().Find(context.Background(), builder, elem.Interface())
	if queryRes.Error != nil {
		if queryRes.Error == sql.ErrNoRows {
			// No related entity found, leave the field as is
			return nil
		}
		return queryRes.Error
	}

	// Set the result to the relation field
	if relationField.Type().Kind() == reflect.Ptr {
		relationField.Set(elem)
	} else {
		relationField.Set(elem.Elem())
	}

	return nil
}

// loadManyToManyRelation loads many-to-many relations
func (o *ORM[T]) loadManyToManyRelation(
	entity T,
	relationName string,
	relationField reflect.Value,
	joinTable string,
	references string,
	gormTag string,
	relationship *schema.Relationship,
) error {
	var joinFKName, referenceFKName string

	// Use schema relationship to get join field names
	if relationship == nil || relationship.JoinTable == nil {
		return fmt.Errorf("cannot load many-to-many relation '%s': join table information is missing", relationName)
	}

	// Extract join field names from relationship
	for _, ref := range relationship.References {
		if ref.OwnPrimaryKey {
			joinFKName = ref.ForeignKey.DBName
		} else {
			referenceFKName = ref.ForeignKey.DBName
		}
	}

	// Ensure both foreign keys are available
	if joinFKName == "" {
		return fmt.Errorf("cannot load many-to-many relation '%s': join foreign key information is missing", relationName)
	}

	if referenceFKName == "" {
		return fmt.Errorf("cannot load many-to-many relation '%s': reference foreign key information is missing", relationName)
	}

	// Get entity value for reflection
	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	// Get primary key value from relationship
	if relationship == nil || len(relationship.References) == 0 {
		return fmt.Errorf("cannot load many-to-many relation '%s': relationship reference information is missing", relationName)
	}

	pkField := entityValue.FieldByName(relationship.References[0].PrimaryKey.Name)
	if !pkField.IsValid() {
		return fmt.Errorf("cannot load many-to-many relation '%s': primary key field '%s' not found",
			relationName, relationship.References[0].PrimaryKey.Name)
	}

	pkValue := pkField.Interface()

	// Get the related entity type (should be a slice)
	if relationField.Kind() != reflect.Slice {
		return fmt.Errorf("many-to-many relation %s must be a slice", relationName)
	}

	// Get element type
	elemType := relationField.Type().Elem()
	if elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}

	// Get table name for related entity from relationship
	if relationship == nil || relationship.FieldSchema == nil {
		return fmt.Errorf("cannot load many-to-many relation '%s': relationship field schema information is missing", relationName)
	}

	relatedTableName := relationship.FieldSchema.Table

	// Build query to load related entities through join table
	builder := v2.NewBuilder()
	builder.Select(fmt.Sprintf("%s.*", relatedTableName))
	builder.From(relatedTableName)
	builder.InnerJoin(joinTable, &dbCore.RawCondition{
		SQL:  "?.id = ?.?",
		Args: []any{relatedTableName, joinTable, referenceFKName},
	})
	builder.Where(&dbCore.BinaryCondition{
		Left:     fmt.Sprintf("%s.%s", joinTable, joinFKName),
		Operator: "=",
		Right:    pkValue,
	})

	// Create a new slice to hold results
	sliceType := reflect.SliceOf(relationField.Type().Elem())
	resultSlice := reflect.New(sliceType).Elem()

	// Create a new slice to unmarshal into
	destSlice := reflect.New(reflect.SliceOf(reflect.TypeOf(reflect.New(elemType).Interface())))

	// Execute query to get all related entities
	queryResult := o.db.Executor().Find(context.Background(), builder, destSlice.Interface())
	if queryResult.Error != nil {
		return queryResult.Error
	}

	// Get the slice value
	destSliceVal := destSlice.Elem()

	// Copy elements to the result slice
	for i := 0; i < destSliceVal.Len(); i++ {
		item := destSliceVal.Index(i)

		// Append to result slice based on whether the relation field expects pointers or values
		if relationField.Type().Elem().Kind() == reflect.Ptr {
			resultSlice = reflect.Append(resultSlice, item)
		} else {
			resultSlice = reflect.Append(resultSlice, item.Elem())
		}
	}

	// Set the result slice to the relation field
	relationField.Set(resultSlice)

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
				IsLoaded:        true,
				IsNew:           false,
				IsDirty:         false,
				PrimaryKey:      "id",
				LoadedColumns:   make(map[string]bool),
				LoadedRelations: make(map[string]bool),
				DataSource:      o.db.DataSource(),
				LastQuery:       sql,
				LastArgs:        args,
				QueryResult:     &queryResult,
			}
			entities[i].SetMeta(meta)
		} else {
			meta.IsLoaded = true
			meta.IsNew = false
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

	qb.Limit(1)
	sql, args := qb.ToSQL()

	queryResult := o.db.Executor().FindRaw(context.Background(), &entity, sql, args...)
	if queryResult.Error != nil {
		return entity, fmt.Errorf("failed to execute FirstByQuery in ORM: %w", queryResult.Error)
	}

	if !isNilValue(entity) {
		meta := entity.GetMeta()
		if meta == nil {
			meta = &EntityMeta{
				IsLoaded:        queryResult.Found,
				IsNew:           false,
				IsDirty:         false,
				PrimaryKey:      "id",
				LoadedColumns:   make(map[string]bool),
				LoadedRelations: make(map[string]bool),
				DataSource:      o.db.DataSource(),
				LastQuery:       sql,
				LastArgs:        args,
				QueryResult:     &queryResult,
			}
			entity.SetMeta(meta)
		} else {
			meta.IsLoaded = queryResult.Found
			meta.IsNew = false
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
	qb.Select("COUNT(*)")
	sql, args := qb.ToSQL()

	count, err := o.db.Executor().CountRaw(context.Background(), sql, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute CountByQuery in ORM: %w", err)
	}

	return count, nil
}
