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

	dbCore "github.com/osbits/gorgany/db/sql/core"
	v2 "github.com/osbits/gorgany/db/sql/gorm/postgres/v2"
	"gorm.io/gorm/schema"
)

// SaveRelations saves all relations of the domain
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
					// Set foreign key on related domain
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
					// Save related domain
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
							// Set foreign key on related domain
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
					// Save related domain first
					orm := New[EntityWithMeta](o.db)
					if err := orm.Save(relEntity); err != nil {
						return err
					}
					// Set foreign key on main domain
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
			if relField.Kind() != reflect.Slice {
				continue
			}
			if relField.IsNil() && !isRelationExplicitlyLoaded(entity, relName) {
				// Leave untouched many-to-many relations that were never loaded or set.
				continue
			}

			// Resolve join table info
			if rel.JoinTable == nil || len(rel.References) == 0 {
				continue
			}
			joinTable := rel.JoinTable.Name

			var ownerFKCol, relatedFKCol string
			var ownerPKField string
			for _, ref := range rel.References {
				if ref.OwnPrimaryKey {
					ownerFKCol = ref.ForeignKey.DBName
					ownerPKField = ref.PrimaryKey.Name
				} else {
					relatedFKCol = ref.ForeignKey.DBName
				}
			}
			if ownerFKCol == "" || relatedFKCol == "" {
				continue
			}

			// Get the owner's PK value once
			ownerPKValue := entityValue.FieldByName(ownerPKField)
			if !ownerPKValue.IsValid() || isZeroValue(ownerPKValue.Interface()) {
				continue
			}

			// Collect PKs of related entities still present in the slice
			var keptRelatedPKs []interface{}

			for i := 0; i < relField.Len(); i++ {
				item := relField.Index(i)

				// Normalise: accept both *T and T elements
				var relEntity EntityWithMeta
				switch item.Kind() {
				case reflect.Ptr:
					if item.IsNil() {
						continue
					}
					var ok bool
					relEntity, ok = item.Interface().(EntityWithMeta)
					if !ok {
						continue
					}
				case reflect.Struct:
					// Value element: get an addressable copy so we can call pointer receivers
					ptr := reflect.New(item.Type())
					ptr.Elem().Set(item)
					var ok bool
					relEntity, ok = ptr.Interface().(EntityWithMeta)
					if !ok {
						continue
					}
				default:
					continue
				}

				// Reuse existing related rows when a PK is already set to avoid
				// duplicate inserts for many-to-many links.
				if err := o.saveManyToManyRelatedEntity(relEntity); err != nil {
					return err
				}

				// Resolve related entity's PK via schema
				relEntityValue := reflect.ValueOf(relEntity)
				if relEntityValue.Kind() == reflect.Ptr {
					relEntityValue = relEntityValue.Elem()
				}
				relSchemaCache := &sync.Map{}
				relSchema, err := schema.Parse(relEntity, relSchemaCache, schema.NamingStrategy{})
				if err != nil || len(relSchema.PrimaryFields) == 0 {
					continue
				}
				relPKField := relEntityValue.FieldByName(relSchema.PrimaryFields[0].Name)
				if !relPKField.IsValid() || isZeroValue(relPKField.Interface()) {
					continue
				}

				relPK := relPKField.Interface()
				keptRelatedPKs = append(keptRelatedPKs, relPK)

				// Upsert a row in the join table (ignore if already exists)
				joinBuilder := v2.NewBuilder().
					Insert(joinTable).
					Columns(ownerFKCol, relatedFKCol).
					Values(ownerPKValue.Interface(), relPK).
					OnConflict(ownerFKCol, relatedFKCol).
					DoNothing()

				queryRes := o.db.Executor().Exec(context.Background(), joinBuilder)
				if queryRes.Error != nil {
					return fmt.Errorf("failed to upsert join table %s: %w", joinTable, queryRes.Error)
				}
			}

			// Delete stale join rows: those belonging to this owner but not in keptRelatedPKs
			deleteBuilder := v2.NewBuilder().
				Delete(joinTable).
				Where(&dbCore.BinaryCondition{
					Left:     ownerFKCol,
					Operator: "=",
					Right:    ownerPKValue.Interface(),
				})
			if len(keptRelatedPKs) > 0 {
				deleteBuilder = deleteBuilder.Where(&dbCore.InCondition{
					Field:  relatedFKCol,
					Values: keptRelatedPKs,
					Not:    true,
				})
			}
			// If keptRelatedPKs is empty, no NOT IN clause is added and all rows for
			// this owner are removed — which is the correct behaviour for a cleared slice.
			deleteRes := o.db.Executor().Exec(context.Background(), deleteBuilder)
			if deleteRes.Error != nil {
				return fmt.Errorf("failed to clean up join table %s: %w", joinTable, deleteRes.Error)
			}
		}
	}
	return nil
}

// LoadRelation loads a specific relation for an domain
// It supports both direct relations and nested relations using dot notation (e.g., "User.Roles")
func (o *ORM[T]) LoadRelation(entity T, relationPath string) error {
	if isNilValue(entity) {
		return errors.New("domain cannot be nil")
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

		// Handle different types of relations (single domain or slice)
		if relationField.Kind() == reflect.Slice {
			// This is a slice relation (hasMany or many2many)
			// We need to load the nested relation for each item in the slice
			for i := 0; i < relationField.Len(); i++ {
				item := relationField.Index(i)

				// If it's a pointer, get the element
				if item.Kind() == reflect.Ptr && !item.IsNil() {
					// Get the domain from the item
					relatedEntity := item.Interface()

					// Check if the related domain implements EntityWithMeta
					if entityWithMeta, ok := relatedEntity.(EntityWithMeta); ok {
						// Create a new ORM for the related domain type
						relatedORM := New[EntityWithMeta](o.db)

						// Load the nested relation on the related domain
						err := relatedORM.LoadRelation(entityWithMeta, strings.Join(relationParts[1:], "."))
						if err != nil {
							return fmt.Errorf("failed to load nested relation '%s' on item %d: %w",
								strings.Join(relationParts[1:], "."), i, err)
						}
					} else {
						return fmt.Errorf("related domain for '%s' at index %d does not implement EntityWithMeta", firstRelation, i)
					}
				}
			}
		} else {
			// This is a single domain relation (hasOne or belongsTo)
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

				// Check if the related domain implements EntityWithMeta
				if entityWithMeta, ok := relatedEntity.(EntityWithMeta); ok {
					// Create a new ORM for the related domain type
					relatedORM := New[EntityWithMeta](o.db)

					// Load the nested relation on the related domain
					err := relatedORM.LoadRelation(entityWithMeta, strings.Join(relationParts[1:], "."))
					if err != nil {
						return fmt.Errorf("failed to load nested relation '%s': %w",
							strings.Join(relationParts[1:], "."), err)
					}
				} else {
					return fmt.Errorf("related domain for '%s' does not implement EntityWithMeta", firstRelation)
				}
			}
		}
	}

	return nil
}

// loadDirectRelation loads a direct (non-nested) relation for an domain
func (o *ORM[T]) loadDirectRelation(entity T, relationName string) error {
	meta := entity.GetMeta()
	if meta == nil {
		return errors.New("domain meta cannot be nil")
	}

	// Check if relation is already loaded
	if meta.IsRelationLoaded(relationName) {
		return nil // Already loaded
	}

	// Get domain value for reflection
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
		return fmt.Errorf("failed to parse domain schema: %w", err)
	}

	// Check if the relationship exists in the schema
	relationship, exists := entitySchema.Relationships.Relations[relationName]
	if !exists {
		return fmt.Errorf("relation '%s' not found in domain schema", relationName)
	}

	// Get primary key value
	var pkValue interface{}
	var foreignKey, references string

	// Get primary key field and value
	if len(entitySchema.PrimaryFieldDBNames) == 0 {
		return fmt.Errorf("domain has no primary key fields defined")
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
	// Determine if it's a slice (hasMany) or single domain (hasOne)
	isSlice := relationField.Kind() == reflect.Slice

	// Get the related domain type
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

		// Copy elements to the result slice and add metadata to each domain
		for i := 0; i < destSliceVal.Len(); i++ {
			item := destSliceVal.Index(i)

			// Add metadata to the domain
			if item.Kind() == reflect.Ptr && !item.IsNil() {
				if entityWithMeta, ok := item.Interface().(EntityWithMeta); ok {
					// Create relation metadata for this domain
					relationMeta := &RelationMeta{
						Type:       relationType,
						ForeignKey: foreignKey,
						LoadedAt:   time.Now(),
					}

					// Set metadata on the domain
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

		// Execute query to get the related domain
		queryRes := o.db.Executor().Find(context.Background(), builder, elem.Interface())
		if queryRes.Error != nil {
			if queryRes.Error == sql.ErrNoRows {
				// No related domain found, leave the field as is
				return nil
			}
			return queryRes.Error
		}

		// Add metadata to the domain
		if entityWithMeta, ok := elem.Interface().(EntityWithMeta); ok {
			// Create relation metadata for this domain
			relationMeta := &RelationMeta{
				Type:       relationType,
				ForeignKey: foreignKey,
				LoadedAt:   time.Now(),
			}

			// Set metadata on the domain
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
	// Get the related domain type
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

	// Build query to load related domain
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

	// Execute query to get the related domain
	queryRes := o.db.Executor().Find(context.Background(), builder, elem.Interface())
	if queryRes.Error != nil {
		if queryRes.Error == sql.ErrNoRows {
			// No related domain found, leave the field as is
			return nil
		}
		return queryRes.Error
	}

	// Add metadata to the domain
	if entityWithMeta, ok := elem.Interface().(EntityWithMeta); ok {
		// Create relation metadata for this domain
		relationMeta := &RelationMeta{
			Type:       "BelongsTo",
			ForeignKey: foreignKey,
			LoadedAt:   time.Now(),
		}

		// Set metadata on the domain
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

	// Get domain value for reflection
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

	// Get the related domain type (should be a slice)
	if relationField.Kind() != reflect.Slice {
		return fmt.Errorf("many-to-many relation %s must be a slice", relationName)
	}

	// Get element type
	elemType := relationField.Type().Elem()
	if elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}

	// Get table name for related domain from relationship
	if relationship == nil || relationship.FieldSchema == nil {
		return fmt.Errorf("cannot load many-to-many relation '%s': relationship field schema information is missing", relationName)
	}

	relatedTableName := relationship.FieldSchema.Table

	// Build query to load related entities through join table
	builder := v2.NewBuilder().
		Select(fmt.Sprintf("%s.*", relatedTableName)).
		From(relatedTableName).
		InnerJoin(joinTable, &dbCore.RawCondition{
			SQL:  "?.id = ?.?",
			Args: []any{relatedTableName, joinTable, referenceFKName},
		}).
		Where(&dbCore.BinaryCondition{
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

	// Copy elements to the result slice and add metadata to each domain
	for i := 0; i < destSliceVal.Len(); i++ {
		item := destSliceVal.Index(i)

		// Add metadata to the domain
		if item.Kind() == reflect.Ptr && !item.IsNil() {
			if entityWithMeta, ok := item.Interface().(EntityWithMeta); ok {
				// Create relation metadata for this domain
				relationMeta := &RelationMeta{
					Type:       "Many2Many",
					JoinTable:  joinTable,
					ForeignKey: referenceFKName,
					LoadedAt:   time.Now(),
				}

				// Set metadata on the domain
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

func isRelationExplicitlyLoaded(entity EntityWithMeta, relationName string) bool {
	meta := entity.GetMeta()
	return meta != nil && meta.IsRelationLoaded(relationName)
}

func (o *ORM[T]) saveManyToManyRelatedEntity(relEntity EntityWithMeta) error {
	meta := relEntity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			LoadedColumns: make(map[string]bool),
			RelationMeta:  make(map[string]*RelationMeta),
		}
		relEntity.SetMeta(meta)
	}

	schemaCache := &sync.Map{}
	relSchema, err := schema.Parse(relEntity, schemaCache, schema.NamingStrategy{})
	if err == nil && len(relSchema.PrimaryFields) > 0 {
		if meta.TableName == "" {
			meta.TableName = relSchema.Table
		}
		relEntityValue := reflect.ValueOf(relEntity)
		if relEntityValue.Kind() == reflect.Ptr {
			relEntityValue = relEntityValue.Elem()
		}

		if meta.PrimaryKey == "" || !relEntityValue.FieldByName(meta.PrimaryKey).IsValid() {
			meta.PrimaryKey = relSchema.PrimaryFields[0].Name
		}

		pkField := relEntityValue.FieldByName(relSchema.PrimaryFields[0].Name)
		if pkField.IsValid() && !isZeroValue(pkField.Interface()) && !meta.IsLoaded {
			exists, err := o.relatedEntityExists(relSchema.Table, relSchema.PrimaryFields[0].DBName, pkField.Interface())
			if err != nil {
				return err
			}
			if exists {
				meta.IsLoaded = true
				meta.DataSource = o.db.DataSource()
				return nil
			}
		}
	}

	relOrm := New[EntityWithMeta](o.db)
	if err := relOrm.Save(relEntity); err != nil {
		return err
	}

	if err == nil && len(relSchema.PrimaryFields) > 0 {
		meta.PrimaryKey = relSchema.PrimaryFields[0].Name
	}

	return nil
}

func (o *ORM[T]) relatedEntityExists(tableName, primaryKey string, primaryKeyValue interface{}) (bool, error) {
	builder := v2.NewBuilder().
		Select("COUNT(*)").
		From(tableName).
		Where(&dbCore.BinaryCondition{
			Left:     primaryKey,
			Operator: "=",
			Right:    primaryKeyValue,
		})

	asSql, args := builder.ToSQL()
	count, err := o.db.Executor().CountRaw(context.Background(), asSql, args...)
	if err != nil {
		return false, fmt.Errorf("failed to check existing related entity in %s: %w", tableName, err)
	}

	return count > 0, nil
}

// PreloadBuilder provides a fluent interface for building preload queries
type PreloadBuilder[T EntityWithMeta] struct {
	orm        *ORM[T]
	relations  []string
	conditions map[string]interface{}
}

// Preload creates a new PreloadBuilder for the given ORM
func (o *ORM[T]) Preload() *PreloadBuilder[T] {
	return &PreloadBuilder[T]{
		orm:        o,
		relations:  make([]string, 0),
		conditions: make(map[string]interface{}),
	}
}

// With adds a relation to preload
func (pb *PreloadBuilder[T]) With(relation string) *PreloadBuilder[T] {
	pb.relations = append(pb.relations, relation)
	return pb
}

// WithCondition adds a condition for a specific relation
func (pb *PreloadBuilder[T]) WithCondition(relation string, condition interface{}) *PreloadBuilder[T] {
	pb.conditions[relation] = condition
	return pb
}

// All executes the query with preloaded relations
func (pb *PreloadBuilder[T]) All() ([]T, error) {
	entities, err := pb.orm.All()
	if err != nil {
		return nil, err
	}

	if len(entities) > 0 {
		err = pb.orm.preloadRelations(entities, pb.relations, pb.conditions)
		if err != nil {
			return nil, err
		}
	}

	return entities, nil
}

// First executes the query with preloaded relations and returns first result
func (pb *PreloadBuilder[T]) First() (T, error) {
	entities, err := pb.orm.All()
	if err != nil {
		var empty T
		return empty, err
	}

	if len(entities) > 0 {
		err = pb.orm.preloadRelations(entities[:1], pb.relations, pb.conditions)
		if err != nil {
			var empty T
			return empty, err
		}
		return entities[0], nil
	}

	var empty T
	return empty, nil
}

// preloadRelations efficiently loads relations for a slice of entities
func (o *ORM[T]) preloadRelations(entities []T, relations []string, conditions map[string]interface{}) error {
	if len(entities) == 0 || len(relations) == 0 {
		return nil
	}

	// Group entities by their primary key values for efficient batch loading
	entityMap := make(map[interface{}]T)
	var primaryKeys []interface{}

	for _, entity := range entities {
		if isNilValue(entity) {
			continue
		}

		pkValue := o.getPrimaryKeyValue(entity)
		if pkValue != nil {
			entityMap[pkValue] = entity
			primaryKeys = append(primaryKeys, pkValue)
		}
	}

	// Load each relation type for all entities
	for _, relation := range relations {
		err := o.batchLoadRelation(entityMap, primaryKeys, relation, conditions[relation])
		if err != nil {
			return fmt.Errorf("failed to preload relation '%s': %w", relation, err)
		}
	}

	return nil
}

// batchLoadRelation loads a specific relation for multiple entities efficiently
func (o *ORM[T]) batchLoadRelation(entityMap map[interface{}]T, primaryKeys []interface{}, relationName string, condition interface{}) error {
	if len(primaryKeys) == 0 {
		return nil
	}

	// Get the first entity to analyze the relation structure
	var sampleEntity T
	for _, entity := range entityMap {
		sampleEntity = entity
		break
	}

	// Parse the relation path (support nested relations like "User.Roles")
	relationParts := strings.Split(relationName, ".")
	firstRelation := relationParts[0]

	// Get entity schema to understand the relation
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(sampleEntity, schemaCache, schema.NamingStrategy{})
	if err != nil {
		return fmt.Errorf("failed to parse entity schema: %w", err)
	}

	// Check if the relationship exists
	relationship, exists := entitySchema.Relationships.Relations[firstRelation]
	if !exists {
		return fmt.Errorf("relation '%s' not found in entity schema", firstRelation)
	}

	// Load the relation based on its type
	switch relationship.Type {
	case schema.HasOne, schema.HasMany:
		return o.batchLoadHasRelation(entityMap, primaryKeys, firstRelation, relationship, condition)
	case schema.BelongsTo:
		return o.batchLoadBelongsToRelation(entityMap, primaryKeys, firstRelation, relationship, condition)
	case schema.Many2Many:
		return o.batchLoadManyToManyRelation(entityMap, primaryKeys, firstRelation, relationship, condition)
	default:
		return fmt.Errorf("unsupported relation type for '%s'", firstRelation)
	}
}

// batchLoadHasRelation efficiently loads hasOne/hasMany relations for multiple entities
func (o *ORM[T]) batchLoadHasRelation(entityMap map[interface{}]T, primaryKeys []interface{}, relationName string, relationship *schema.Relationship, condition interface{}) error {
	if len(primaryKeys) == 0 {
		return nil
	}

	// Get table name and foreign key from relationship
	if relationship == nil || relationship.FieldSchema == nil {
		return fmt.Errorf("cannot load hasOne/hasMany relation '%s': relationship schema information is missing", relationName)
	}

	tableName := relationship.FieldSchema.Table
	if len(relationship.References) == 0 {
		return fmt.Errorf("cannot load hasOne/hasMany relation '%s': foreign key reference information is missing", relationName)
	}

	foreignKey := relationship.References[0].ForeignKey.DBName

	// Build query to load all related entities at once
	var builder dbCore.IQueryBuilder = v2.NewBuilder()
	builder = builder.
		Select("*").
		From(tableName).
		Where(&dbCore.InCondition{
			Field:  foreignKey,
			Values: primaryKeys,
		})

	// Add custom condition if provided
	if condition != nil {
		// This is a simplified condition handling - you might want to expand this
		if condMap, ok := condition.(map[string]interface{}); ok {
			for key, value := range condMap {
				builder = builder.Where(&dbCore.BinaryCondition{
					Left:     key,
					Operator: "=",
					Right:    value,
				})
			}
		}
	}

	// Execute query to get all related entities
	var relatedEntities []interface{}
	queryRes := o.db.Executor().Find(context.Background(), builder, &relatedEntities)
	if queryRes.Error != nil {
		return queryRes.Error
	}

	// Group related entities by foreign key
	relatedByFK := make(map[interface{}][]interface{})
	for _, relatedEntity := range relatedEntities {
		if relatedEntity == nil {
			continue
		}

		// Extract foreign key value from related entity
		fkValue := o.extractFieldValue(relatedEntity, relationship.References[0].ForeignKey.Name)
		if fkValue != nil {
			relatedByFK[fkValue] = append(relatedByFK[fkValue], relatedEntity)
		}
	}

	// Assign related entities to their parent entities
	for pkValue, entity := range entityMap {
		relatedList := relatedByFK[pkValue]
		if len(relatedList) > 0 {
			err := o.assignRelatedEntities(entity, relationName, relatedList, relationship)
			if err != nil {
				return fmt.Errorf("failed to assign related entities for entity with PK %v: %w", pkValue, err)
			}
		}
	}

	return nil
}

// batchLoadBelongsToRelation efficiently loads belongsTo relations for multiple entities
func (o *ORM[T]) batchLoadBelongsToRelation(entityMap map[interface{}]T, primaryKeys []interface{}, relationName string, relationship *schema.Relationship, condition interface{}) error {
	if len(primaryKeys) == 0 {
		return nil
	}

	// Get table name and foreign key from relationship
	if relationship == nil || relationship.FieldSchema == nil {
		return fmt.Errorf("cannot load belongsTo relation '%s': relationship schema information is missing", relationName)
	}

	tableName := relationship.FieldSchema.Table
	if len(relationship.References) == 0 {
		return fmt.Errorf("cannot load belongsTo relation '%s': foreign key reference information is missing", relationName)
	}

	// Collect all foreign key values from entities
	var foreignKeyValues []interface{}
	entityByFK := make(map[interface{}]T)

	for _, entity := range entityMap {
		if isNilValue(entity) {
			continue
		}

		fkValue := o.extractFieldValue(entity, relationship.References[0].ForeignKey.Name)
		if fkValue != nil {
			foreignKeyValues = append(foreignKeyValues, fkValue)
			entityByFK[fkValue] = entity
		}
	}

	if len(foreignKeyValues) == 0 {
		return nil
	}

	// Build query to load all related entities at once
	var builder dbCore.IQueryBuilder = v2.NewBuilder()
	builder = builder.
		Select("*").
		From(tableName).
		Where(&dbCore.InCondition{
			Field:  relationship.References[0].PrimaryKey.DBName,
			Values: foreignKeyValues,
		})

	// Add custom condition if provided
	if condition != nil {
		if condMap, ok := condition.(map[string]interface{}); ok {
			for key, value := range condMap {
				builder = builder.Where(&dbCore.BinaryCondition{
					Left:     key,
					Operator: "=",
					Right:    value,
				})
			}
		}
	}

	// Execute query to get all related entities
	var relatedEntities []interface{}
	queryRes := o.db.Executor().Find(context.Background(), builder, &relatedEntities)
	if queryRes.Error != nil {
		return queryRes.Error
	}

	// Create a map of related entities by their primary key
	relatedByPK := make(map[interface{}]interface{})
	for _, relatedEntity := range relatedEntities {
		if relatedEntity == nil {
			continue
		}

		pkValue := o.extractFieldValue(relatedEntity, relationship.References[0].PrimaryKey.Name)
		if pkValue != nil {
			relatedByPK[pkValue] = relatedEntity
		}
	}

	// Assign related entities to their parent entities
	for fkValue, entity := range entityByFK {
		if relatedEntity, exists := relatedByPK[fkValue]; exists {
			err := o.assignRelatedEntity(entity, relationName, relatedEntity, relationship)
			if err != nil {
				return fmt.Errorf("failed to assign related entity for entity with FK %v: %w", fkValue, err)
			}
		}
	}

	return nil
}

// batchLoadManyToManyRelation efficiently loads many-to-many relations for multiple entities
func (o *ORM[T]) batchLoadManyToManyRelation(entityMap map[interface{}]T, primaryKeys []interface{}, relationName string, relationship *schema.Relationship, condition interface{}) error {
	if len(primaryKeys) == 0 {
		return nil
	}

	// Get join table and field information
	if relationship == nil || relationship.JoinTable == nil {
		return fmt.Errorf("cannot load many-to-many relation '%s': join table information is missing", relationName)
	}

	joinTable := relationship.JoinTable.Name
	if len(relationship.References) == 0 {
		return fmt.Errorf("cannot load many-to-many relation '%s': relationship reference information is missing", relationName)
	}

	// Extract join field names from relationship
	var joinFKName, referenceFKName string
	for _, ref := range relationship.References {
		if ref.OwnPrimaryKey {
			joinFKName = ref.ForeignKey.DBName
		} else {
			referenceFKName = ref.ForeignKey.DBName
		}
	}

	if joinFKName == "" || referenceFKName == "" {
		return fmt.Errorf("cannot load many-to-many relation '%s': join foreign key information is missing", relationName)
	}

	// Get table name for related entity
	if relationship.FieldSchema == nil {
		return fmt.Errorf("cannot load many-to-many relation '%s': relationship field schema information is missing", relationName)
	}
	relatedTableName := relationship.FieldSchema.Table

	// Build query to load related entities through join table
	var builder dbCore.IQueryBuilder = v2.NewBuilder()
	builder = builder.Select(fmt.Sprintf("%s.*, %s.%s as _join_fk", relatedTableName, joinTable, joinFKName))
	builder = builder.From(relatedTableName)
	builder = builder.InnerJoin(joinTable, &dbCore.RawCondition{
		SQL:  "?.id = ?.?",
		Args: []any{relatedTableName, joinTable, referenceFKName},
	})
	builder = builder.Where(&dbCore.InCondition{
		Field:  fmt.Sprintf("%s.%s", joinTable, joinFKName),
		Values: primaryKeys,
	})

	// Add custom condition if provided
	if condition != nil {
		if condMap, ok := condition.(map[string]interface{}); ok {
			for key, value := range condMap {
				builder = builder.Where(&dbCore.BinaryCondition{
					Left:     key,
					Operator: "=",
					Right:    value,
				})
			}
		}
	}

	// Execute query to get all related entities with join information
	var results []map[string]interface{}
	queryRes := o.db.Executor().Find(context.Background(), builder, &results)
	if queryRes.Error != nil {
		return queryRes.Error
	}

	// Group related entities by the join foreign key
	relatedByJoinFK := make(map[interface{}][]interface{})
	for _, result := range results {
		if joinFK, exists := result["_join_fk"]; exists {
			// Remove the join field from the result
			delete(result, "_join_fk")

			// Convert the result back to the related entity type
			relatedEntity := o.convertMapToEntity(result, relationship.FieldSchema)
			if relatedEntity != nil {
				relatedByJoinFK[joinFK] = append(relatedByJoinFK[joinFK], relatedEntity)
			}
		}
	}

	// Assign related entities to their parent entities
	for pkValue, entity := range entityMap {
		relatedList := relatedByJoinFK[pkValue]
		if len(relatedList) > 0 {
			err := o.assignRelatedEntities(entity, relationName, relatedList, relationship)
			if err != nil {
				return fmt.Errorf("failed to assign related entities for entity with PK %v: %w", pkValue, err)
			}
		}
	}

	return nil
}

// assignRelatedEntities assigns a list of related entities to a parent entity
func (o *ORM[T]) assignRelatedEntities(entity T, relationName string, relatedEntities []interface{}, relationship *schema.Relationship) error {
	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	relationField := entityValue.FieldByName(relationName)
	if !relationField.IsValid() {
		return fmt.Errorf("relation field '%s' not found", relationName)
	}

	// Determine if it's a slice (hasMany/many2many) or single entity (hasOne)
	isSlice := relationField.Kind() == reflect.Slice

	if isSlice {
		// Create a new slice to hold results
		sliceType := relationField.Type()
		resultSlice := reflect.New(sliceType).Elem()

		// Add each related entity to the slice
		for _, relatedEntity := range relatedEntities {
			if relatedEntity == nil {
				continue
			}

			// Convert to the expected type
			relatedValue := reflect.ValueOf(relatedEntity)
			if relatedValue.Kind() == reflect.Ptr {
				relatedValue = relatedValue.Elem()
			}

			// Append to result slice based on whether the relation field expects pointers or values
			if relationField.Type().Elem().Kind() == reflect.Ptr {
				resultSlice = reflect.Append(resultSlice, relatedValue.Addr())
			} else {
				resultSlice = reflect.Append(resultSlice, relatedValue)
			}
		}

		// Set the result slice to the relation field
		relationField.Set(resultSlice)
	} else {
		// Single entity relation (hasOne)
		if len(relatedEntities) > 0 {
			relatedEntity := relatedEntities[0]
			if relatedEntity != nil {
				err := o.assignRelatedEntity(entity, relationName, relatedEntity, relationship)
				if err != nil {
					return err
				}
			}
		}
	}

	// Mark relation as loaded
	meta := entity.GetMeta()
	if meta != nil {
		relationMeta := &RelationMeta{
			Type:       getRelationType(relationship.Type),
			ForeignKey: getForeignKey(relationship),
			JoinTable:  getJoinTable(relationship),
			LoadedAt:   time.Now(),
			Count:      len(relatedEntities),
		}
		meta.SetRelationLoaded(relationName, relationMeta)
	}

	return nil
}

// assignRelatedEntity assigns a single related entity to a parent entity
func (o *ORM[T]) assignRelatedEntity(entity T, relationName string, relatedEntity interface{}, relationship *schema.Relationship) error {
	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	relationField := entityValue.FieldByName(relationName)
	if !relationField.IsValid() {
		return fmt.Errorf("relation field '%s' not found", relationName)
	}

	// Convert related entity to the expected type
	relatedValue := reflect.ValueOf(relatedEntity)
	if relatedValue.Kind() == reflect.Ptr {
		relatedValue = relatedValue.Elem()
	}

	// Set the result to the relation field
	if relationField.Type().Kind() == reflect.Ptr {
		relationField.Set(relatedValue.Addr())
	} else {
		relationField.Set(relatedValue)
	}

	// Mark relation as loaded
	meta := entity.GetMeta()
	if meta != nil {
		relationMeta := &RelationMeta{
			Type:       getRelationType(relationship.Type),
			ForeignKey: getForeignKey(relationship),
			JoinTable:  getJoinTable(relationship),
			LoadedAt:   time.Now(),
			Count:      1,
		}
		meta.SetRelationLoaded(relationName, relationMeta)
	}

	return nil
}

// Helper methods
func (o *ORM[T]) getPrimaryKeyValue(entity T) interface{} {
	if isNilValue(entity) {
		return nil
	}

	meta := entity.GetMeta()
	if meta == nil {
		return nil
	}

	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	pkField := entityValue.FieldByName(meta.PrimaryKey)
	if !pkField.IsValid() {
		return nil
	}

	return pkField.Interface()
}

func (o *ORM[T]) extractFieldValue(entity interface{}, fieldName string) interface{} {
	if entity == nil {
		return nil
	}

	entityValue := reflect.ValueOf(entity)
	if entityValue.Kind() == reflect.Ptr {
		entityValue = entityValue.Elem()
	}

	field := entityValue.FieldByName(fieldName)
	if !field.IsValid() {
		return nil
	}

	return field.Interface()
}

func (o *ORM[T]) convertMapToEntity(data map[string]interface{}, schema *schema.Schema) interface{} {
	// This is a simplified conversion - you might want to implement a more robust version
	// For now, we'll return the map as-is and let the caller handle the conversion
	return data
}

func getRelationType(relType schema.RelationshipType) string {
	switch relType {
	case schema.HasOne:
		return "HasOne"
	case schema.HasMany:
		return "HasMany"
	case schema.BelongsTo:
		return "BelongsTo"
	case schema.Many2Many:
		return "Many2Many"
	default:
		return "Unknown"
	}
}

func getForeignKey(relationship *schema.Relationship) string {
	if relationship != nil && len(relationship.References) > 0 {
		return relationship.References[0].ForeignKey.DBName
	}
	return ""
}

func getJoinTable(relationship *schema.Relationship) string {
	if relationship != nil && relationship.JoinTable != nil {
		return relationship.JoinTable.Name
	}
	return ""
}

// Convenience methods for preloading

// PreloadWith is a convenience method to preload relations and return all entities
func (o *ORM[T]) PreloadWith(relations ...string) ([]T, error) {
	builder := o.Preload()
	for _, relation := range relations {
		builder = builder.With(relation)
	}
	return builder.All()
}

// PreloadWithCondition is a convenience method to preload relations with conditions and return all entities
func (o *ORM[T]) PreloadWithCondition(relations map[string]interface{}) ([]T, error) {
	builder := o.Preload()
	for relation, condition := range relations {
		builder = builder.WithCondition(relation, condition)
	}
	return builder.All()
}

// PreloadFirst is a convenience method to preload relations and return the first entity
func (o *ORM[T]) PreloadFirst(relations ...string) (T, error) {
	builder := o.Preload()
	for _, relation := range relations {
		builder = builder.With(relation)
	}
	return builder.First()
}

// PreloadFirstWithCondition is a convenience method to preload relations with conditions and return the first entity
func (o *ORM[T]) PreloadFirstWithCondition(relations map[string]interface{}) (T, error) {
	builder := o.Preload()
	for relation, condition := range relations {
		builder = builder.WithCondition(relation, condition)
	}
	return builder.First()
}

// With is a convenience method that returns a PreloadBuilder with the given relations
func (o *ORM[T]) With(relations ...string) *PreloadBuilder[T] {
	builder := o.Preload()
	for _, relation := range relations {
		builder = builder.With(relation)
	}
	return builder
}
