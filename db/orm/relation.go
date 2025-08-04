package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	v2 "git.qix.sx/gorgany/gorgany.git/db/sql/gorm/postgres/v2"
	"gorm.io/gorm/schema"
)

// SaveRelations saves all relations of the entity
func (o *ORM[T]) SaveRelations(entity T) error {
	if isNilValue(entity) {
		return nil
	}

	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err != nil {
		return nil // If schema can't be parsed, skip
	}

	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	for relName, rel := range entitySchema.Relationships.Relations {
		relField := entityValue.FieldByName(relName)
		if !relField.IsValid() {
			continue
		}

		switch rel.Type {
		case schema.HasOne:
			if relField.Kind() == reflect.Ptr && !relField.IsNil() {
				relEntity, ok := relField.Interface().(EntityWithMeta)
				if ok {
					// Set foreign key on related entity
					if len(rel.References) > 0 {
						fkField := rel.References[0].ForeignKey.Name
						pkField := rel.References[0].PrimaryKey.Name
						pkValue := entityValue.FieldByName(pkField)
						if pkValue.IsValid() {
							relEntityValue := reflect.ValueOf(relEntity)
							if relEntityValue.Kind() == reflect.Ptr {
								relEntityValue = relEntityValue.Elem()
							}
							fkFieldVal := relEntityValue.FieldByName(fkField)
							if fkFieldVal.CanSet() {
								setForeignKeyValue(fkFieldVal, pkValue)
							}
						}
					}
					// Save related entity
					orm := New[EntityWithMeta](o.db)
					if err := orm.Save(relEntity); err != nil {
						return err
					}
				}
			}
		case schema.HasMany:
			if relField.Kind() == reflect.Slice {
				for i := 0; i < relField.Len(); i++ {
					item := relField.Index(i)
					if item.Kind() == reflect.Ptr && !item.IsNil() {
						relEntity, ok := item.Interface().(EntityWithMeta)
						if ok {
							// Set foreign key on related entity
							if len(rel.References) > 0 {
								fkField := rel.References[0].ForeignKey.Name
								pkField := rel.References[0].PrimaryKey.Name
								pkValue := entityValue.FieldByName(pkField)
								if pkValue.IsValid() {
									relEntityValue := reflect.ValueOf(relEntity)
									if relEntityValue.Kind() == reflect.Ptr {
										relEntityValue = relEntityValue.Elem()
									}
									fkFieldVal := relEntityValue.FieldByName(fkField)
									if fkFieldVal.CanSet() {
										setForeignKeyValue(fkFieldVal, pkValue)
									}
								}
							}
							orm := New[EntityWithMeta](o.db)
							if err := orm.Save(relEntity); err != nil {
								return err
							}
						}
					}
				}
			}
		case schema.BelongsTo:
			if relField.Kind() == reflect.Ptr && !relField.IsNil() {
				relEntity, ok := relField.Interface().(EntityWithMeta)
				if ok {
					// Save related entity first
					orm := New[EntityWithMeta](o.db)
					if err := orm.Save(relEntity); err != nil {
						return err
					}
					// Set foreign key on main entity
					if len(rel.References) > 0 {
						fkField := rel.References[0].ForeignKey.Name
						pkField := rel.References[0].PrimaryKey.Name
						relEntityValue := reflect.ValueOf(relEntity)
						if relEntityValue.Kind() == reflect.Ptr {
							relEntityValue = relEntityValue.Elem()
						}
						pkValue := relEntityValue.FieldByName(pkField)
						if pkValue.IsValid() {
							mainEntityValue := entityValue
							fkFieldVal := mainEntityValue.FieldByName(fkField)
							if fkFieldVal.CanSet() {
								setForeignKeyValue(fkFieldVal, pkValue)
							}
						}
					}
				}
			}
		case schema.Many2Many:
			// For now, just save related entities (not join table)
			if relField.Kind() == reflect.Slice {
				for i := 0; i < relField.Len(); i++ {
					item := relField.Index(i)
					if item.Kind() == reflect.Ptr && !item.IsNil() {
						relEntity, ok := item.Interface().(EntityWithMeta)
						if ok {
							orm := New[EntityWithMeta](o.db)
							if err := orm.Save(relEntity); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// LoadRelation loads a specific relation for an entity
// It supports both direct relations and nested relations using dot notation (e.g., "User.Roles")
func (o *ORM[T]) LoadRelation(entity T, relationPath string) error {
	if isNilValue(entity) {
		return errors.New("entity cannot be nil")
	}

	// Check if the relation path contains nested relations
	parts := strings.Split(relationPath, ".")
	if len(parts) > 1 {
		// This is a nested relation, handle it recursively
		return o.loadNestedRelation(entity, parts)
	}

	// This is a direct relation, load it normally
	return o.loadDirectRelation(entity, relationPath)
}

// loadNestedRelation loads a nested relation path (e.g., "User.Roles")
func (o *ORM[T]) loadNestedRelation(entity T, relationParts []string) error {
	if len(relationParts) == 0 {
		return nil // Nothing to load
	}

	// Load the first level relation
	firstRelation := relationParts[0]
	err := o.loadDirectRelation(entity, firstRelation)
	if err != nil {
		return fmt.Errorf("failed to load first-level relation '%s': %w", firstRelation, err)
	}

	// If there are more parts, we need to load nested relations
	if len(relationParts) > 1 {
		// Get the loaded relation value
		entityValue := reflect.ValueOf(entity)
		if entityValue.Kind() == reflect.Ptr {
			entityValue = entityValue.Elem()
		}

		relationField := entityValue.FieldByName(firstRelation)
		if !relationField.IsValid() {
			return fmt.Errorf("relation field '%s' not found after loading", firstRelation)
		}

		// Handle different types of relations (single entity or slice)
		if relationField.Kind() == reflect.Slice {
			// This is a slice relation (hasMany or many2many)
			// We need to load the nested relation for each item in the slice
			for i := 0; i < relationField.Len(); i++ {
				item := relationField.Index(i)

				// If it's a pointer, get the element
				if item.Kind() == reflect.Ptr && !item.IsNil() {
					// Get the entity from the item
					relatedEntity := item.Interface()

					// Check if the related entity implements EntityWithMeta
					if entityWithMeta, ok := relatedEntity.(EntityWithMeta); ok {
						// Create a new ORM for the related entity type
						relatedORM := New[EntityWithMeta](o.db)

						// Load the nested relation on the related entity
						err := relatedORM.LoadRelation(entityWithMeta, strings.Join(relationParts[1:], "."))
						if err != nil {
							return fmt.Errorf("failed to load nested relation '%s' on item %d: %w",
								strings.Join(relationParts[1:], "."), i, err)
						}
					} else {
						return fmt.Errorf("related entity for '%s' at index %d does not implement EntityWithMeta", firstRelation, i)
					}
				}
			}
		} else {
			// This is a single entity relation (hasOne or belongsTo)
			// If it's a pointer and not nil, load the nested relation
			if (relationField.Kind() == reflect.Ptr && !relationField.IsNil()) ||
				(relationField.Kind() == reflect.Struct) {

				var relatedEntity interface{}
				if relationField.Kind() == reflect.Ptr {
					relatedEntity = relationField.Interface()
				} else {
					// If it's a struct, we need to get a pointer to it
					relatedEntity = relationField.Addr().Interface()
				}

				// Check if the related entity implements EntityWithMeta
				if entityWithMeta, ok := relatedEntity.(EntityWithMeta); ok {
					// Create a new ORM for the related entity type
					relatedORM := New[EntityWithMeta](o.db)

					// Load the nested relation on the related entity
					err := relatedORM.LoadRelation(entityWithMeta, strings.Join(relationParts[1:], "."))
					if err != nil {
						return fmt.Errorf("failed to load nested relation '%s': %w",
							strings.Join(relationParts[1:], "."), err)
					}
				} else {
					return fmt.Errorf("related entity for '%s' does not implement EntityWithMeta", firstRelation)
				}
			}
		}
	}

	return nil
}

// loadDirectRelation loads a direct (non-nested) relation for an entity
func (o *ORM[T]) loadDirectRelation(entity T, relationName string) error {
	meta := entity.GetMeta()
	if meta == nil {
		return errors.New("entity meta cannot be nil")
	}

	// Check if relation is already loaded
	if meta.IsRelationLoaded(relationName) {
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
	if err != nil {
		return fmt.Errorf("failed to parse entity schema: %w", err)
	}

	// Check if the relationship exists in the schema
	relationship, exists := entitySchema.Relationships.Relations[relationName]
	if !exists {
		return fmt.Errorf("relation '%s' not found in entity schema", relationName)
	}

	// Get primary key value
	var pkValue interface{}
	var foreignKey, references string

	// Get primary key field and value
	if len(entitySchema.PrimaryFieldDBNames) == 0 {
		return fmt.Errorf("entity has no primary key fields defined")
	}

	pkField := entityValue.FieldByName(entitySchema.PrimaryFields[0].Name)
	if !pkField.IsValid() {
		return fmt.Errorf("primary key field '%s' not found", entitySchema.PrimaryFields[0].Name)
	}

	pkValue = pkField.Interface()

	// Handle different relation types
	switch relationship.Type {
	case schema.HasOne, schema.HasMany:
		if len(relationship.References) == 0 {
			return fmt.Errorf("hasOne/hasMany relation '%s' has no references defined", relationName)
		}

		foreignKey = relationship.References[0].ForeignKey.DBName
		err = o.loadHasRelation(entity, relationName, relationField, foreignKey, pkValue, relationship)
		if err != nil {
			return fmt.Errorf("failed to load hasOne/hasMany relation '%s': %w", relationName, err)
		}

	case schema.BelongsTo:
		if len(relationship.References) == 0 {
			return fmt.Errorf("belongsTo relation '%s' has no references defined", relationName)
		}

		foreignKey = relationship.References[0].ForeignKey.DBName
		foreignKeyField := entityValue.FieldByName(relationship.References[0].ForeignKey.Name)
		if !foreignKeyField.IsValid() {
			return fmt.Errorf("foreign key field '%s' not found for belongsTo relation '%s'",
				relationship.References[0].ForeignKey.Name, relationName)
		}

		err = o.loadBelongsToRelation(entity, relationName, relationField, foreignKeyField.Interface(), relationship)
		if err != nil {
			return fmt.Errorf("failed to load belongsTo relation '%s': %w", relationName, err)
		}

	case schema.Many2Many:
		if relationship.JoinTable == nil {
			return fmt.Errorf("many2many relation '%s' has no join table defined", relationName)
		}

		if len(relationship.References) == 0 {
			return fmt.Errorf("many2many relation '%s' has no references defined", relationName)
		}

		joinTable := relationship.JoinTable.Name
		references = relationship.References[0].PrimaryKey.DBName
		err = o.loadManyToManyRelation(entity, relationName, relationField, joinTable, references,
			relationship.Field.Tag.Get("gorm"), relationship)
		if err != nil {
			return fmt.Errorf("failed to load many2many relation '%s': %w", relationName, err)
		}

	default:
		return fmt.Errorf("unsupported relation type for '%s'", relationName)
	}

	// Create relation metadata
	relationMeta := &RelationMeta{
		LoadedAt: time.Now(),
	}

	// Set relation type
	switch relationship.Type {
	case schema.HasOne:
		relationMeta.Type = "HasOne"
	case schema.HasMany:
		relationMeta.Type = "HasMany"
		// Count the number of related entities if it's a slice
		if relationField.Kind() == reflect.Slice {
			relationMeta.Count = relationField.Len()
		}
	case schema.BelongsTo:
		relationMeta.Type = "BelongsTo"
	case schema.Many2Many:
		relationMeta.Type = "Many2Many"
		// Count the number of related entities if it's a slice
		if relationField.Kind() == reflect.Slice {
			relationMeta.Count = relationField.Len()
		}
		// Set join table for Many2Many relations
		if relationship.JoinTable != nil {
			relationMeta.JoinTable = relationship.JoinTable.Name
		}
	}

	// Set foreign key
	if len(relationship.References) > 0 {
		relationMeta.ForeignKey = relationship.References[0].ForeignKey.DBName
	}

	// Mark relation as loaded with metadata
	meta.SetRelationLoaded(relationName, relationMeta)
	return nil
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

	// Create relation metadata
	relationType := "HasMany"
	if !isSlice {
		relationType = "HasOne"
	}

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

		// Copy elements to the result slice and add metadata to each entity
		for i := 0; i < destSliceVal.Len(); i++ {
			item := destSliceVal.Index(i)

			// Add metadata to the entity
			if item.Kind() == reflect.Ptr && !item.IsNil() {
				if entityWithMeta, ok := item.Interface().(EntityWithMeta); ok {
					// Create relation metadata for this entity
					relationMeta := &RelationMeta{
						Type:       relationType,
						ForeignKey: foreignKey,
						LoadedAt:   time.Now(),
					}

					// Set metadata on the entity
					meta := entityWithMeta.GetMeta()
					if meta == nil {
						meta = &EntityMeta{
							TableName:     tableName,
							PrimaryKey:    "id", // Default
							IsLoaded:      true,
							LoadedColumns: make(map[string]bool),
							RelationMeta:  make(map[string]*RelationMeta),
						}
						entityWithMeta.SetMeta(meta)
					}

					// Set the relation metadata
					meta.SetRelationLoaded("Parent", relationMeta)
				}
			}

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

		// Add metadata to the entity
		if entityWithMeta, ok := elem.Interface().(EntityWithMeta); ok {
			// Create relation metadata for this entity
			relationMeta := &RelationMeta{
				Type:       relationType,
				ForeignKey: foreignKey,
				LoadedAt:   time.Now(),
			}

			// Set metadata on the entity
			meta := entityWithMeta.GetMeta()
			if meta == nil {
				meta = &EntityMeta{
					TableName:     tableName,
					PrimaryKey:    "id", // Default
					IsLoaded:      true,
					LoadedColumns: make(map[string]bool),
					RelationMeta:  make(map[string]*RelationMeta),
				}
				entityWithMeta.SetMeta(meta)
			}

			// Set the relation metadata
			meta.SetRelationLoaded("Parent", relationMeta)
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

	primaryKey := relationship.References[0].PrimaryKey.DBName
	foreignKey := relationship.References[0].ForeignKey.DBName

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

	// Add metadata to the entity
	if entityWithMeta, ok := elem.Interface().(EntityWithMeta); ok {
		// Create relation metadata for this entity
		relationMeta := &RelationMeta{
			Type:       "BelongsTo",
			ForeignKey: foreignKey,
			LoadedAt:   time.Now(),
		}

		// Set metadata on the entity
		meta := entityWithMeta.GetMeta()
		if meta == nil {
			meta = &EntityMeta{
				TableName:     tableName,
				PrimaryKey:    primaryKey, // Use the primary key from relationship
				IsLoaded:      true,
				LoadedColumns: make(map[string]bool),
				RelationMeta:  make(map[string]*RelationMeta),
			}
			entityWithMeta.SetMeta(meta)
		}

		// Set the relation metadata
		meta.SetRelationLoaded("Parent", relationMeta)
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

	// Copy elements to the result slice and add metadata to each entity
	for i := 0; i < destSliceVal.Len(); i++ {
		item := destSliceVal.Index(i)

		// Add metadata to the entity
		if item.Kind() == reflect.Ptr && !item.IsNil() {
			if entityWithMeta, ok := item.Interface().(EntityWithMeta); ok {
				// Create relation metadata for this entity
				relationMeta := &RelationMeta{
					Type:       "Many2Many",
					JoinTable:  joinTable,
					ForeignKey: referenceFKName,
					LoadedAt:   time.Now(),
				}

				// Set metadata on the entity
				meta := entityWithMeta.GetMeta()
				if meta == nil {
					meta = &EntityMeta{
						TableName:     relatedTableName,
						PrimaryKey:    "id", // Default
						IsLoaded:      true,
						LoadedColumns: make(map[string]bool),
						RelationMeta:  make(map[string]*RelationMeta),
					}
					entityWithMeta.SetMeta(meta)
				}

				// Set the relation metadata
				meta.SetRelationLoaded("Parent", relationMeta)
			}
		}

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

// Helper to set a value with pointer/value conversion
func setForeignKeyValue(dest reflect.Value, src reflect.Value) {
	if !dest.CanSet() {
		return
	}
	destType := dest.Type()
	srcType := src.Type()
	if destType == srcType {
		dest.Set(src)
		return
	}
	if destType.Kind() == reflect.Ptr && srcType.Kind() != reflect.Ptr {
		// e.g., dest is *int, src is int
		ptr := reflect.New(destType.Elem())
		ptr.Elem().Set(src)
		dest.Set(ptr)
		return
	}
	if destType.Kind() != reflect.Ptr && srcType.Kind() == reflect.Ptr {
		// e.g., dest is int, src is *int
		if !src.IsNil() {
			dest.Set(src.Elem())
		}
		return
	}
	// fallback: try to convert if assignable
	if src.Type().AssignableTo(dest.Type()) {
		dest.Set(src)
	}
}
