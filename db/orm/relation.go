package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"gorm.io/gorm/schema"
)

// SaveRelations saves all relations of the domain.
//
// It refuses, before writing anything, what checkCascade refuses: on an external_schema
// datasource, a relation that holds something it would write, unless the entity implements
// CascadingSaves and returns true (see CascadingSaves for why), and on any datasource a
// many-to-many relation whose join table links a composite key, or that keeps more related
// rows than the dialect lets one statement bind (see keptJoinRows).
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

	if err := o.checkCascade(entity, entitySchema, entityValue); err != nil {
		return err
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

			ownerRef, relatedRef, err := joinReferences(relName, rel)
			if err != nil {
				return err
			}
			if ownerRef == nil || relatedRef == nil {
				continue
			}
			ownerFKCol, relatedFKCol := ownerRef.ForeignKey.DBName, relatedRef.ForeignKey.DBName
			ownerPKField := ownerRef.PrimaryKey.Name

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

				// The join row stores the column the relation references, which is the related
				// entity's key unless the relation names another column. It used to store the
				// related entity's first key column whatever the join column referenced.
				relEntityValue := reflect.ValueOf(relEntity)
				if relEntityValue.Kind() == reflect.Ptr {
					relEntityValue = relEntityValue.Elem()
				}
				relPKField := relEntityValue.FieldByName(relatedRef.PrimaryKey.Name)
				if !relPKField.IsValid() || isZeroValue(relPKField.Interface()) {
					continue
				}

				relPK := relPKField.Interface()
				keptRelatedPKs = append(keptRelatedPKs, relPK)

				// Upsert a row in the join table (ignore if already exists)
				joinBuilder := o.newBuilder().
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
			deleteBuilder := o.newBuilder().
				Delete(joinTable).
				Where(&dbCore.BinaryCondition{
					Left:     ownerFKCol,
					Operator: "=",
					Right:    ownerPKValue.Interface(),
				})
			if len(keptRelatedPKs) > 0 {
				keep, err := o.keptJoinRows(relName, joinTable, relatedFKCol, keptRelatedPKs, 1)
				if err != nil {
					return err
				}
				deleteBuilder = deleteBuilder.Where(keep)
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

// cascadeRefusedError is the refusal SaveRelations returns on an external_schema datasource.
//
// It is a type rather than fmt.Errorf so that its text is the sentence an operator reads and
// errors.Is still finds core.ErrExternalSchema behind it, as it does behind every other
// external_schema refusal. relation is the path from the saved entity, such as "Items" or,
// for a relation of a related entity, "Items.Tags".
type cascadeRefusedError struct {
	relation string
}

func (e *cascadeRefusedError) Error() string {
	return fmt.Sprintf("orm: refusing to cascade Save into relation %q on an external_schema "+
		"datasource; save related entities explicitly or implement CascadeSaves()", e.relation)
}

func (e *cascadeRefusedError) Unwrap() error { return dbCore.ErrExternalSchema }

// checkCascade walks what Save would write through entity's relations and returns the first
// refusal, before anything is written.
//
// createEntity and updateEntity ask it before sending the entity's own statement, and
// SaveRelations asks it again for a caller that calls SaveRelations directly. The cascade
// itself writes as it goes: the entity's row, then each related entity, whose own relations
// are checked only when the cascade reaches it. Checking only there would leave the rows
// written before a refusal in place while the Save reports failure, and a retry would write
// them twice. So the walk descends into every related entity the cascade would save, however
// deep, and asks each one:
//   - on an external_schema datasource, whether it opts in with CascadingSaves, if any of its
//     relations would be written (see CascadingSaves);
//   - on any datasource, whether a many-to-many relation that would be written is linked by a
//     composite key (see joinReferences), and whether it keeps more related rows than the one
//     DELETE of its stale join rows can bind under the dialect's limit (see keptJoinRows).
//
// Relations are walked in name order, so the refusal names the same relation on every run. A
// related entity is visited once, so a cycle ends the walk rather than looping. The walk is
// conservative where the cascade decides at run time: a many-to-many item that the cascade
// would find already stored, and therefore not save, is checked all the same.
func (o *ORM[T]) checkCascade(entity EntityWithMeta, entitySchema *schema.Schema, entityValue reflect.Value) error {
	external := o.db != nil && dbCore.IsExternalSchema(o.db.DataSource())
	limit := 0
	if o.db != nil {
		limit = dbCore.BindParameterLimit(o.dialect())
	}
	return checkCascadeFrom(entity, entitySchema, reflect.Indirect(entityValue), external, limit, "", map[visitedEntity]bool{})
}

// visitedEntity identifies an entity the walk has reached. The type is part of it because a
// struct and its first field share an address.
type visitedEntity struct {
	entityType reflect.Type
	address    uintptr
}

// checkCascadeFrom is checkCascade for one entity of the walk. path is the relation path
// that reached it, ending in a dot, or "" for the entity Save was called on. limit is the
// dialect's bind-parameter limit, 0 for none (see core.BindParameterLimit).
func checkCascadeFrom(entity EntityWithMeta, entitySchema *schema.Schema, entityValue reflect.Value,
	external bool, limit int, path string, visited map[visitedEntity]bool) error {
	if entityValue.CanAddr() {
		key := visitedEntity{entityValue.Type(), entityValue.Addr().Pointer()}
		if visited[key] {
			return nil
		}
		visited[key] = true
	}

	optedIn := false
	if opted, ok := any(entity).(CascadingSaves); ok {
		optedIn = opted.CascadeSaves()
	}

	names := make([]string, 0, len(entitySchema.Relationships.Relations))
	for name := range entitySchema.Relationships.Relations {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		rel := entitySchema.Relationships.Relations[name]
		field := entityValue.FieldByName(name)
		if !field.IsValid() || !relationWouldBeWritten(entity, name, rel, field) {
			continue
		}
		relationPath := path + name
		if external && !optedIn {
			return &cascadeRefusedError{relation: relationPath}
		}
		if rel.Type == schema.Many2Many {
			if _, _, err := joinReferences(relationPath, rel); err != nil {
				return err
			}
			// The join rows it keeps are named in the one statement that deletes the others,
			// so a relation holding more of them than that statement can bind is refused here,
			// before the join rows are written, rather than half way (see keptJoinRows).
			if kept := len(relatedEntities(rel, field)); kept > 0 && !fitsOneStatement(kept, limit, 1) {
				joinTable := ""
				if rel.JoinTable != nil {
					joinTable = rel.JoinTable.Name
				}
				return tooManyKeptJoinRows(relationPath, joinTable, kept, limit)
			}
		}
		for _, related := range relatedEntities(rel, field) {
			relatedSchema, err := schema.Parse(related, &sync.Map{}, schema.NamingStrategy{})
			if err != nil {
				// SaveRelations does not walk the relations of an entity whose schema it
				// cannot parse either.
				continue
			}
			relatedValue := reflect.Indirect(reflect.ValueOf(related))
			if err := checkCascadeFrom(related, relatedSchema, relatedValue, external, limit, relationPath+".", visited); err != nil {
				return err
			}
		}
	}
	return nil
}

// relationWouldBeWritten reports whether SaveRelations writes anything through field.
//
// It follows SaveRelations case by case, so a nil or empty relation never refuses a Save
// that would not have touched it. A many-to-many relation is the exception to "empty": a
// slice that is set but empty, or one that was loaded and then set to nil, is how a caller
// asks SaveRelations to delete the entity's join rows, so it is a write and is refused like
// one.
func relationWouldBeWritten(entity EntityWithMeta, name string, rel *schema.Relationship, field reflect.Value) bool {
	switch rel.Type {
	case schema.HasOne, schema.BelongsTo, schema.HasMany:
		return len(relatedEntities(rel, field)) > 0
	case schema.Many2Many:
		if field.Kind() != reflect.Slice {
			return false
		}
		return !field.IsNil() || isRelationExplicitlyLoaded(entity, name)
	}
	return false
}

// relatedEntities returns the entities SaveRelations saves through field, in order.
//
// A many-to-many element held by value is returned as a pointer to a copy, which is what
// SaveRelations saves for it.
func relatedEntities(rel *schema.Relationship, field reflect.Value) []EntityWithMeta {
	var related []EntityWithMeta
	add := func(v reflect.Value) {
		if v.Kind() == reflect.Ptr && !v.IsNil() {
			if entity, ok := v.Interface().(EntityWithMeta); ok {
				related = append(related, entity)
			}
		}
	}

	switch rel.Type {
	case schema.HasOne, schema.BelongsTo:
		add(field)
	case schema.HasMany, schema.Many2Many:
		if field.Kind() != reflect.Slice {
			return nil
		}
		for i := 0; i < field.Len(); i++ {
			item := field.Index(i)
			if rel.Type == schema.Many2Many && item.Kind() == reflect.Struct {
				copied := reflect.New(item.Type())
				copied.Elem().Set(item)
				item = copied
			}
			add(item)
		}
	}
	return related
}

// joinReferences returns the reference that links a many-to-many join row to its owner and
// the one that links it to the related entity, or nils when the relation has no such pair, in
// which case SaveRelations leaves it alone.
//
// Each side must be one column. SaveRelations writes and deletes join rows by one owner
// column and one related column, so a composite key on either side was addressed by one of
// its parts: an owner keyed by (tenant, id) deleted its join rows in every tenant, and a
// related entity keyed by (tenant, code) had its tenant written into the code column, and
// every join row of the owner deleted, the one being kept included. Such a relation is
// refused, before anything is written, until join rows are addressed by every column.
func joinReferences(relation string, rel *schema.Relationship) (owner, related *schema.Reference, err error) {
	var owners, relateds []*schema.Reference
	for _, ref := range rel.References {
		if ref.PrimaryKey == nil || ref.ForeignKey == nil {
			continue
		}
		if ref.OwnPrimaryKey {
			owners = append(owners, ref)
		} else {
			relateds = append(relateds, ref)
		}
	}
	if len(owners) > 1 || len(relateds) > 1 {
		columns := make([]string, 0, len(owners)+len(relateds))
		for _, ref := range append(owners, relateds...) {
			columns = append(columns, ref.ForeignKey.DBName)
		}
		table := ""
		if rel.JoinTable != nil {
			table = rel.JoinTable.Name
		}
		return nil, nil, fmt.Errorf("orm: Save cannot write many-to-many relation %q: its join table %s "+
			"links a composite key (%s), which Save would address by one column; write its join rows "+
			"explicitly", relation, table, strings.Join(columns, ", "))
	}
	if len(owners) == 0 || len(relateds) == 0 {
		return nil, nil, nil
	}
	return owners[0], relateds[0], nil
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
	builder = o.newBuilder()
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
	builder = o.newBuilder()
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

	// Extract join field names from relationship. relatedKeyName is the related table's column
	// the join row's reference points at: its primary key, or the column a references: tag
	// names.
	var relatedKeyName string
	for _, ref := range relationship.References {
		if ref.OwnPrimaryKey {
			joinFKName = ref.ForeignKey.DBName
		} else {
			referenceFKName = ref.ForeignKey.DBName
			if ref.PrimaryKey != nil {
				relatedKeyName = ref.PrimaryKey.DBName
			}
		}
	}

	// Ensure both foreign keys are available
	if joinFKName == "" {
		return fmt.Errorf("cannot load many-to-many relation '%s': join foreign key information is missing", relationName)
	}

	if referenceFKName == "" || relatedKeyName == "" {
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
	builder := o.newBuilder().
		Select(fmt.Sprintf("%s.*", relatedTableName)).
		From(relatedTableName).
		InnerJoin(joinTable, manyToManyJoinCondition(relatedTableName, relatedKeyName, joinTable, referenceFKName)).
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
	builder := o.newBuilder().
		Select("COUNT(*)").
		From(tableName).
		Where(&dbCore.BinaryCondition{
			Left:     primaryKey,
			Operator: "=",
			Right:    primaryKeyValue,
		})

	asSql, args, err := builder.ToSQL()
	if err != nil {
		return false, fmt.Errorf("failed to render existence check for %s: %w", tableName, err)
	}
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

	// Load all related entities, in as few queries as the dialect's bind-parameter limit
	// allows (see chunkValues): one, unless it declares a limit the keys exceed.
	extra := preloadConditions(condition)
	var relatedEntities []interface{}
	for _, chunk := range chunkValues(primaryKeys, dbCore.BindParameterLimit(o.dialect()), len(extra)) {
		builder := whereAll(o.newBuilder().
			Select("*").
			From(tableName).
			Where(&dbCore.InCondition{
				Field:  foreignKey,
				Values: chunk,
			}), extra)

		var loaded []interface{}
		queryRes := o.db.Executor().Find(context.Background(), builder, &loaded)
		if queryRes.Error != nil {
			return queryRes.Error
		}
		relatedEntities = append(relatedEntities, loaded...)
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

	// Load all related entities, in as few queries as the dialect's bind-parameter limit
	// allows (see chunkValues).
	extra := preloadConditions(condition)
	var relatedEntities []interface{}
	for _, chunk := range chunkValues(foreignKeyValues, dbCore.BindParameterLimit(o.dialect()), len(extra)) {
		builder := whereAll(o.newBuilder().
			Select("*").
			From(tableName).
			Where(&dbCore.InCondition{
				Field:  relationship.References[0].PrimaryKey.DBName,
				Values: chunk,
			}), extra)

		var loaded []interface{}
		queryRes := o.db.Executor().Find(context.Background(), builder, &loaded)
		if queryRes.Error != nil {
			return queryRes.Error
		}
		relatedEntities = append(relatedEntities, loaded...)
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

	// Extract join field names from relationship, and the related table's column the join
	// row's reference points at (see loadManyToManyRelation).
	var joinFKName, referenceFKName, relatedKeyName string
	for _, ref := range relationship.References {
		if ref.OwnPrimaryKey {
			joinFKName = ref.ForeignKey.DBName
		} else {
			referenceFKName = ref.ForeignKey.DBName
			if ref.PrimaryKey != nil {
				relatedKeyName = ref.PrimaryKey.DBName
			}
		}
	}

	if joinFKName == "" || referenceFKName == "" || relatedKeyName == "" {
		return fmt.Errorf("cannot load many-to-many relation '%s': join foreign key information is missing", relationName)
	}

	// Get table name for related entity
	if relationship.FieldSchema == nil {
		return fmt.Errorf("cannot load many-to-many relation '%s': relationship field schema information is missing", relationName)
	}
	relatedTableName := relationship.FieldSchema.Table

	// Load related entities through the join table, in as few queries as the dialect's
	// bind-parameter limit allows (see chunkValues). The select list is two items, not one
	// string holding both: a dialect that quotes the names it recognises sees each of them,
	// where a combined "t.*, j.c as _join_fk" is an expression it can only emit as written.
	extra := preloadConditions(condition)
	var results []map[string]interface{}
	for _, chunk := range chunkValues(primaryKeys, dbCore.BindParameterLimit(o.dialect()), len(extra)) {
		builder := whereAll(o.newBuilder().
			Select(fmt.Sprintf("%s.*", relatedTableName), fmt.Sprintf("%s.%s as _join_fk", joinTable, joinFKName)).
			From(relatedTableName).
			InnerJoin(joinTable, manyToManyJoinCondition(relatedTableName, relatedKeyName, joinTable, referenceFKName)).
			Where(&dbCore.InCondition{
				Field:  fmt.Sprintf("%s.%s", joinTable, joinFKName),
				Values: chunk,
			}), extra)

		var loaded []map[string]interface{}
		queryRes := o.db.Executor().Find(context.Background(), builder, &loaded)
		if queryRes.Error != nil {
			return queryRes.Error
		}
		results = append(results, loaded...)
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

// manyToManyJoinCondition is the ON of a many-to-many load: the related table's referenced
// column equals the join table's column that references it.
//
// It used to be RawCondition{"?.id = ?.?"}, which compared the join column with a column
// called id whatever the related table's key was, so a relation to a table keyed by anything
// else, or one whose references: tag names another column, joined on a column that does not
// exist or on the wrong one. It is still a RawCondition, with the key as a fourth "?."
// identifier, because of how each dialect renders one. Postgres and MySQL emit the substituted
// names as written, so for every name, one that starts with a digit or is not ASCII included,
// they send what they did, with the key in place of id. A BinaryCondition would not do: rendered
// without a context, as Postgres and MySQL render a JOIN's ON, an operand that is not a simple
// ASCII identifier is bound as a value, so a join on 2024tags.id compared the join column with
// the text '2024tags.id'. SQL Server's rendering context quotes each substituted name, so it
// sends [2024tags].[code] = [order_tags].[tag_code].
func manyToManyJoinCondition(relatedTable, relatedKey, joinTable, joinColumn string) dbCore.Condition {
	return &dbCore.RawCondition{
		SQL:  "?.? = ?.?",
		Args: []any{relatedTable, relatedKey, joinTable, joinColumn},
	}
}

// preloadConditions turns a preload condition, a map of column to value, into equality
// conditions in column order.
//
// The map used to be ranged over in place, in Go's random order, which was harmless in one
// statement; a load split into several statements (see chunkValues) now renders the same
// conditions in each, so they are ordered once here.
func preloadConditions(condition interface{}) []dbCore.Condition {
	condMap, ok := condition.(map[string]interface{})
	if !ok || len(condMap) == 0 {
		return nil
	}
	columns := make([]string, 0, len(condMap))
	for column := range condMap {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	conditions := make([]dbCore.Condition, 0, len(columns))
	for _, column := range columns {
		conditions = append(conditions, &dbCore.BinaryCondition{
			Left:     column,
			Operator: "=",
			Right:    condMap[column],
		})
	}
	return conditions
}

// chunkValues splits the values of an IN list into lists that each fit in one statement
// beside reserved other bind parameters, under a dialect limit of limit parameters per
// statement.
//
// SQL Server refuses a request with more than 2100 parameters, and a relation load over a
// large parent set reaches that with one IN list; its dialect reports the limit (see
// core.BindParameterLimit). The loads run one query per chunk and merge what they return,
// which is the same rows the one query would have: each chunk's query differs only in which
// keys its IN names. A limit of 0 or less means the dialect declares none, and gives one chunk
// holding every value, so on Postgres and MySQL, which declare none, a load is the one query
// it always was. When reserved alone takes the whole limit a chunk still holds one value, and
// the statement is refused by the parameter cap rather than never sent.
func chunkValues(values []any, limit, reserved int) [][]any {
	if limit <= 0 || len(values) <= limit-reserved {
		return [][]any{values}
	}
	size := limit - reserved
	if size < 1 {
		size = 1
	}
	chunks := make([][]any, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := min(start+size, len(values))
		chunks = append(chunks, values[start:end:end])
	}
	return chunks
}

// fitsOneStatement reports whether n values and reserved other bind parameters fit in one
// statement under limit, 0 meaning no limit.
func fitsOneStatement(n, limit, reserved int) bool {
	return limit <= 0 || n+reserved <= limit
}

// keptJoinRows returns the condition that keeps the given related keys when Save deletes the
// stale join rows of a many-to-many relation: column NOT IN values, for the one DELETE.
//
// Unlike a load, this cannot be split into several statements. Each DELETE would keep only its
// own share of the keys and remove the rows every other share keeps, and splitting the list
// into several NOT IN lists ANDed into one statement binds as many parameters as one list. So a
// relation that keeps more rows than the statement can bind beside its reserved parameters is
// refused. checkCascade, which SaveRelations and the Save that calls it ask first, refuses the
// same before anything is written, so this refusal is a defensive second check.
func (o *ORM[T]) keptJoinRows(relation, joinTable, column string, values []any, reserved int) (dbCore.Condition, error) {
	limit := dbCore.BindParameterLimit(o.dialect())
	if !fitsOneStatement(len(values), limit, reserved) {
		return nil, tooManyKeptJoinRows(relation, joinTable, len(values), limit)
	}
	return &dbCore.InCondition{Field: column, Values: values, Not: true}, nil
}

// tooManyKeptJoinRows is the refusal of a many-to-many save whose kept join rows do not fit in
// the one DELETE that removes the others.
func tooManyKeptJoinRows(relation, joinTable string, kept, limit int) error {
	return fmt.Errorf("orm: Save cannot rewrite the join rows of many-to-many relation %q: the %d "+
		"related rows it keeps and the owner's key need %d bind parameters in the one DELETE of %s that "+
		"removes the others, and the dialect allows %d per statement; write the join rows explicitly",
		relation, kept, kept+1, joinTable, limit)
}
