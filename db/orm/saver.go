package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/log"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// ErrRowGone reports an update whose row no longer exists.
//
// Only UpdateExisting returns it. Save and Update deliberately do not: see updateEntity for
// why zero matched rows is not, on its own, evidence of anything.
var ErrRowGone = errors.New("orm: the row this entity was loaded from no longer exists")

// Save saves an domain (creates if new, updates if existing)
func (o *ORM[T]) Save(entity T) error {
	if isNilValue(entity) {
		return errors.New("domain cannot be nil")
	}
	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:    "id",
			LoadedColumns: make(map[string]bool),
			RelationMeta:  make(map[string]*RelationMeta),
		}
		entity.SetMeta(meta)
	}
	if !meta.IsLoaded {
		return o.createEntity(entity)
	}
	return o.updateEntity(entity, false)
}

// Create inserts a new domain into the database.
func (o *ORM[T]) Create(entity T) error {
	if isNilValue(entity) {
		return errors.New("domain cannot be nil")
	}
	return o.createEntity(entity)
}

// Update modifies an existing domain in the database.
func (o *ORM[T]) Update(entity T) error {
	if isNilValue(entity) {
		return errors.New("domain cannot be nil")
	}
	return o.updateEntity(entity, false)
}

// UpdateExisting modifies an existing domain and fails with ErrRowGone when its row is not
// there any more.
//
// Update on its own cannot tell the difference: the statement it runs matches no rows and
// succeeds, which is what let a caller believe state had been persisted after something
// else had deleted the row underneath it. This method resolves it by asking whether the row
// exists — the extra read happens only when nothing was matched, so a normal update costs
// nothing. It is a separate method rather than a change to Update because zero matched rows
// is genuinely ambiguous (see updateEntity) and most callers neither need nor want the
// stricter contract.
func (o *ORM[T]) UpdateExisting(entity T) error {
	if isNilValue(entity) {
		return errors.New("domain cannot be nil")
	}
	return o.updateEntity(entity, true)
}

// Delete deletes an domain
func (o *ORM[T]) Delete(entity T) error {
	if isNilValue(entity) {
		return errors.New("domain cannot be nil")
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

	// Execute hooks if domain implements them
	if hook, ok := any(entity).(interface{ BeforeDelete(*gorm.DB) error }); ok {
		if err := hook.BeforeDelete(nil); err != nil {
			return err
		}
	}

	// Extract domain information using reflection
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		tableName = GetTableName(entity)
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
		return fmt.Errorf("cannot delete domain with zero primary key value")
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
		return fmt.Errorf("failed to delete domain: %w", queryRes.Error)
	}

	// Execute after hooks
	if hook, ok := any(entity).(interface{ AfterDelete(*gorm.DB) error }); ok {
		if err := hook.AfterDelete(nil); err != nil {
			return err
		}
	}

	return nil
}

// createEntity handles the actual creation logic for an domain
func (o *ORM[T]) createEntity(entity T) error {
	meta := entity.GetMeta()
	if meta == nil {
		meta = &EntityMeta{
			PrimaryKey:    "id",
			LoadedColumns: make(map[string]bool),
			RelationMeta:  make(map[string]*RelationMeta),
			IsLoaded:      false,
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

	// Execute hooks if domain implements them
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

	// Extract domain information using reflection
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		tableName = GetTableName(entity)
		meta.TableName = tableName
	}

	// Create a builder for the INSERT query
	var builder dbCore.IQueryBuilder
	builder = o.newBuilder()

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

	// Read the server-generated columns back.
	//
	// RETURNING is the efficient way and it is what Postgres supports, but MySQL has
	// no such clause: appending it unconditionally is what made `orm.Create` fail
	// with MySQL error 1064 for the whole of v2. Which path to take is decided by
	// asking the dialect, not by naming an engine, so a third dialect gets the right
	// behaviour without touching this code.
	generated := map[string]interface{}{}
	if dbCore.SupportsReturning(o.dialect()) {
		builder = builder.Returning(returningCols...)

		queryRes := o.db.Executor().Find(context.Background(), builder, &generated)
		if queryRes.Error != nil {
			return fmt.Errorf("failed to create domain: %w", queryRes.Error)
		}
	} else {
		var err error
		generated, err = o.insertAndReadBack(builder, entitySchema, returningCols)
		if err != nil {
			return err
		}
	}

	destVal := reflect.Indirect(reflect.ValueOf(entity))
	for _, col := range returningCols {
		if val, ok := generated[col]; ok {
			if sf := entitySchema.LookUpField(col); sf != nil {
				// Set — это schema.Field.Set, он правильно обходит вложенные поля
				sf.Set(context.Background(), destVal, val)
			}
		}
	}

	// Update metadata
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

	// Auto-save relations after creating the main domain
	if err := o.SaveRelations(entity); err != nil {
		return fmt.Errorf("failed to auto-save relations: %w", err)
	}

	return nil
}

// updateEntity handles the actual update logic for an domain.
//
// requireRow asks it to treat a statement that matched nothing as a failure. That is not the
// default, and the reason is that the row count alone does not say what happened. Postgres
// reports matched rows, so zero there does mean the row is gone; MySQL reports *changed*
// rows unless the DSN carries clientFoundRows, and this framework's MySQL DSN does not set
// it, so an update that writes the values a row already holds legitimately reports zero.
// Turning zero into an error unconditionally would therefore break correct code on one of
// the two engines the ORM supports.
//
// What is unconditional is recording the result on the entity's meta. Before, the statement
// result was thrown away entirely: an update against a deleted row was indistinguishable
// from one that landed, for every caller, with no way to find out.
func (o *ORM[T]) updateEntity(entity T, requireRow bool) error {
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

	// Execute hooks if domain implements them
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

	// Extract domain information using reflection
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		tableName = GetTableName(entity)
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
		return fmt.Errorf("cannot update domain with zero primary key value")
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
		return fmt.Errorf("failed to update domain: %w", queryRes.Error)
	}
	meta.QueryResult = &queryRes

	if requireRow && queryRes.RowsAffected == 0 {
		exists, existsErr := o.rowExists(tableName, meta.PrimaryKey, pkValue)
		if existsErr != nil {
			return existsErr
		}
		if !exists {
			return fmt.Errorf("%w: %s where %s = %v", ErrRowGone, tableName, meta.PrimaryKey, pkValue)
		}
	}

	// Update metadata
	meta.IsDirty = false
	meta.IsLoaded = true

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

	// Auto-save relations after updating the main domain
	if err := o.SaveRelations(entity); err != nil {
		return fmt.Errorf("failed to auto-save relations: %w", err)
	}

	return nil
}

// rowExists reports whether a row with this primary key is still in the table.
//
// Keyed and limited to one row, and read through Find rather than a COUNT so it goes through
// the same path createEntity's read-back uses on every engine.
func (o *ORM[T]) rowExists(tableName, primaryKey string, pkValue any) (bool, error) {
	sql, args, err := o.newBuilder().
		Select(primaryKey).
		From(tableName).
		Eq(primaryKey, pkValue).
		Limit(1).
		ToSQL()
	if err != nil {
		return false, fmt.Errorf("orm: cannot render the existence check for %s: %w", tableName, err)
	}

	row := map[string]interface{}{}
	queryRes := o.db.Executor().FindRaw(context.Background(), &row, sql, args...)
	if queryRes.Error != nil {
		return false, fmt.Errorf("orm: cannot check whether the %s row still exists: %w",
			tableName, queryRes.Error)
	}

	return queryRes.Found, nil
}

// insertAndReadBack performs an INSERT on an engine that has no RETURNING clause,
// then reads the server-generated columns back.
//
// The generated key comes from the driver's own sql.Result for the INSERT — see
// core.LastInsertIDExecutor for why a separate `SELECT LAST_INSERT_ID()` would be
// unsafe against a connection pool. Any remaining generated columns (defaults,
// computed values) are then fetched with one SELECT keyed on the primary key, so
// the cost on MySQL is one extra round trip and none on Postgres.
func (o *ORM[T]) insertAndReadBack(
	builder dbCore.IQueryBuilder,
	entitySchema *schema.Schema,
	returningCols []string,
) (map[string]interface{}, error) {
	generated := map[string]interface{}{}

	executor, ok := o.db.Executor().(dbCore.LastInsertIDExecutor)
	if !ok {
		// Without a generated-key channel there is no correct way to learn an
		// auto-increment value, and silently returning a zero id would corrupt every
		// relation saved against this entity.
		return nil, fmt.Errorf(
			"orm: %s does not support RETURNING and its executor (%T) cannot report a "+
				"generated key, so a created row's primary key cannot be read back",
			o.dialect().Name(), o.db.Executor())
	}

	res := executor.ExecInsert(context.Background(), builder)
	if res.Error != nil {
		return nil, fmt.Errorf("failed to create domain: %w", res.Error)
	}

	// Identify the single auto-increment primary key, if there is one.
	autoIncrementPK := ""
	for _, f := range entitySchema.PrimaryFields {
		if f.AutoIncrement {
			if autoIncrementPK != "" {
				// Composite auto-increment keys are not a thing any supported engine
				// reports, so refuse rather than guess which column the id belongs to.
				return nil, fmt.Errorf(
					"orm: %s reports one generated key but %s has multiple auto-increment "+
						"primary key columns", o.dialect().Name(), entitySchema.Table)
			}
			autoIncrementPK = f.DBName
		}
	}

	if res.HasLastInsertID && autoIncrementPK != "" {
		generated[autoIncrementPK] = res.LastInsertID
	}

	// Fetch any other generated columns in one read, keyed on the primary key we now
	// know. Skipped entirely when the only generated column was the key itself.
	remaining := make([]string, 0, len(returningCols))
	for _, col := range returningCols {
		if _, have := generated[col]; !have {
			remaining = append(remaining, col)
		}
	}
	if len(remaining) == 0 {
		return generated, nil
	}

	keyColumn, keyValue, err := o.readBackKey(entitySchema, generated, autoIncrementPK)
	if err != nil {
		// Nothing to key the read on. The insert succeeded, so report what is known
		// rather than failing the whole Create.
		log.Log().Warnf(
			"orm: created a row in %s but cannot read back %v: %v",
			entitySchema.Table, remaining, err)
		return generated, nil
	}

	sql, args, err := o.newBuilder().
		Select(remaining...).
		From(entitySchema.Table).
		Eq(keyColumn, keyValue).
		Limit(1).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("orm: cannot render the generated-column read-back: %w", err)
	}

	fetched := map[string]interface{}{}
	queryRes := o.db.Executor().FindRaw(context.Background(), &fetched, sql, args...)
	if queryRes.Error != nil {
		return nil, fmt.Errorf("orm: cannot read back generated columns: %w", queryRes.Error)
	}
	for col, value := range fetched {
		generated[col] = value
	}

	return generated, nil
}

// readBackKey picks the column and value to key the generated-column read-back on:
// the auto-increment key just reported, or otherwise a primary key the caller
// supplied itself.
func (o *ORM[T]) readBackKey(
	entitySchema *schema.Schema,
	generated map[string]interface{},
	autoIncrementPK string,
) (string, interface{}, error) {
	if autoIncrementPK != "" {
		if value, ok := generated[autoIncrementPK]; ok {
			return autoIncrementPK, value, nil
		}
	}

	// A client-assigned primary key is just as good to select on.
	for _, f := range entitySchema.PrimaryFields {
		if value, ok := generated[f.DBName]; ok {
			return f.DBName, value, nil
		}
	}

	return "", nil, fmt.Errorf("no known primary key value")
}
