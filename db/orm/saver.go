package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	v2 "git.qix.sx/gorgany/gorgany.git/db/sql/gorm/postgres/v2"
	"git.qix.sx/gorgany/gorgany.git/util"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Save saves an entity (creates if new, updates if existing)
func (o *ORM[T]) Save(entity T) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}
	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:    "id",
			LoadedColumns: make(map[string]bool),
			RelationMeta:  make(map[string]*RelationMeta),
			IsNew:         true,
		}
		entity.SetMeta(meta)
	}
	if meta.IsNew {
		return o.createEntity(entity)
	}
	return o.updateEntity(entity)
}

// Create inserts a new entity into the database.
func (o *ORM[T]) Create(entity T) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}
	return o.createEntity(entity)
}

// Update modifies an existing entity in the database.
func (o *ORM[T]) Update(entity T) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}
	return o.updateEntity(entity)
}

// Delete deletes an entity
func (o *ORM[T]) Delete(entity T) error {
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

// createEntity handles the actual creation logic for an entity
func (o *ORM[T]) createEntity(entity T) error {
	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:    "id",
			LoadedColumns: make(map[string]bool),
			RelationMeta:  make(map[string]*RelationMeta),
			IsNew:         true,
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

	// Auto-save relations after creating the main entity
	if err := o.SaveRelations(entity); err != nil {
		return fmt.Errorf("failed to auto-save relations: %w", err)
	}

	return nil
}

// updateEntity handles the actual update logic for an entity
func (o *ORM[T]) updateEntity(entity T) error {
	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:    "id",
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

	// Auto-save relations after updating the main entity
	if err := o.SaveRelations(entity); err != nil {
		return fmt.Errorf("failed to auto-save relations: %w", err)
	}

	return nil
}
