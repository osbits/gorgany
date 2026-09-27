package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/log"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// ErrRowGone reports that the row an entity addresses no longer exists.
//
// UpdateExisting, an update guarded by EntityMeta.UpdateGuard, and Refresh return it. Save
// and Update without a guard deliberately do not: see updateEntity for why zero matched rows
// is not, on its own, evidence of anything.
var ErrRowGone = errors.New("orm: the row this entity was loaded from no longer exists")

// ErrRowConflict reports a guarded update whose guard did not match a row that is still there.
//
// It is returned only when EntityMeta.UpdateGuard is set, and it is deliberately distinct from
// ErrRowGone. Gone means the row was deleted and the caller must fail closed; conflict means
// somebody else wrote the row first and the caller has to re-read it and decide what its own
// pending changes mean on top of the new state. Collapsing the two would make a lost update
// look like a revocation.
var ErrRowConflict = errors.New("orm: the row this entity was loaded from has changed since it was read")

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

// Delete deletes an domain.
//
// The row is addressed by every primary key column, by column name (see pkPredicate). It
// used to be addressed by the first key's Go field name, which it also wrote into
// meta.PrimaryKey, so `UserID` reached the server as the column name and a composite key
// deleted every row that shared its first part.
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

	entitySchema, err := keySchema(entity)
	if err != nil {
		return fmt.Errorf("cannot delete domain: %w", err)
	}
	if len(entitySchema.PrimaryFieldDBNames) > 0 {
		meta.PrimaryKey = entitySchema.PrimaryFieldDBNames[0]
	}

	// Execute hooks if domain implements them
	if hook, ok := any(entity).(interface{ BeforeDelete(*gorm.DB) error }); ok {
		if err := hook.BeforeDelete(nil); err != nil {
			return err
		}
	}

	// Get table name
	tableName := meta.TableName
	if tableName == "" {
		tableName = GetTableName(entity)
		meta.TableName = tableName
	}

	key, err := pkPredicate(entitySchema, reflect.ValueOf(entity))
	if err != nil {
		return fmt.Errorf("cannot delete domain: %w", err)
	}

	builder := whereAll(o.db.Query().Delete(tableName), key)

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

// createEntity handles the actual creation logic for an domain.
//
// A model gorm cannot parse is refused before its hooks run and before anything is sent. Its
// schema names the key and the generated columns to read back. The model used to be inserted
// all the same, with the Go field names for columns wherever the schema could not name them,
// so the server refused the INSERT, or worse, took it.
//
// The generated columns — the key, every column with a default and every field tagged
// grgorm:"readback" — are read back with the INSERT's RETURNING where the dialect has one and
// the table allows it (see TableWithTriggers), and otherwise by insertAndReadBack. Each is
// matched to its field by exact column name first and then ignoring case, since an engine may
// report a name folded: Postgres returns RETURNING Id as id.
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

	entitySchema, err := keySchema(entity)
	if err != nil {
		return fmt.Errorf("cannot create domain: %w", err)
	}
	if len(entitySchema.PrimaryFieldDBNames) > 0 {
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

	// Refused before the INSERT is sent, so a Save whose cascade is refused writes nothing at
	// all rather than the entity's own row and then an error (see checkCascade).
	if err := o.checkCascade(entity, entitySchema, val); err != nil {
		return err
	}

	// Extract field values using our new function that supports embedded structs
	columns, values := extractFieldsForInsert(val, meta)
	inserted := make(map[string]bool, len(columns))
	for _, column := range columns {
		inserted[column] = true
	}

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
	readback := map[string]bool{}
	for _, f := range readbackFields(entitySchema) {
		readback[f.DBName] = true
		if _, ok := seen[f.DBName]; !ok {
			returningCols = append(returningCols, f.DBName)
			seen[f.DBName] = struct{}{}
		}
	}

	// Read the server-generated columns back.
	//
	// RETURNING is the efficient way and it is what Postgres supports, but MySQL has
	// no such clause: appending it unconditionally is what made `orm.Create` fail
	// with MySQL error 1064 for the whole of v2. Which path to take is decided by
	// asking the dialect, not by naming an engine, so any other dialect gets the right
	// behaviour without touching this code.
	//
	// A dialect can also say that a trigger on the table makes the server refuse its
	// RETURNING, as SQL Server's OUTPUT is refused (Msg 334), and a model on such a table
	// says it has one (see TableWithTriggers). Both have to hold: the model's word alone
	// changes nothing on an engine whose RETURNING triggers do not block, so a model shared
	// with Postgres keeps RETURNING there.
	destVal := reflect.Indirect(reflect.ValueOf(entity))

	generated := map[string]interface{}{}
	if o.canReturn(entity) {
		builder = builder.Returning(returningCols...)

		queryRes := o.db.Executor().Find(context.Background(), builder, &generated)
		if queryRes.Error != nil {
			return fmt.Errorf("failed to create domain: %w", queryRes.Error)
		}
	} else {
		var err error
		generated, err = o.insertAndReadBack(builder, entitySchema, returningCols, destVal, inserted)
		if err != nil {
			return err
		}
	}

	for _, col := range returningCols {
		if val, ok := columnValue(generated, col); ok {
			if sf := entitySchema.LookUpField(col); sf != nil {
				// Set is schema.Field.Set, which reaches a field of an embedded struct too. A key
				// or a read-back column that cannot be set is an error, as one that cannot be
				// read is: the entity would carry a key, or a version, that is not the row's.
				// Any other default column is left as it was when it cannot be, as it always
				// has been.
				if err := sf.Set(context.Background(), destVal, val); err != nil && (sf.PrimaryKey || readback[col]) {
					return fmt.Errorf("orm: the INSERT into %s succeeded but its column %s could not be "+
						"set on the entity; the row exists: %w", tableName, col, err)
				}
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
// the engines the ORM supports. (SQL Server's executor reports matched rows, as Postgres
// does, from ROWCOUNT_BIG().)
//
// What is unconditional is recording the result on the entity's meta. Before, the statement
// result was thrown away entirely: an update against a deleted row was indistinguishable
// from one that landed, for every caller, with no way to find out.
//
// EntityMeta.UpdateGuard turns the same zero-row result into a decidable one without needing
// requireRow, and the MySQL caveat above is why: a guarded write, by construction, sets a
// column to a value the row does not currently hold — the version it is guarding on — so a
// guard that matched always reports at least one changed row on every engine. Zero rows under
// a guard therefore means either the row is gone or the guard lost, and the existence probe
// below tells them apart.
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

	// The key columns come from the schema. A schema gorm cannot parse is refused (see
	// keySchema) rather than addressed through a guessed "id" column.
	entitySchema, err := keySchema(entity)
	if err != nil {
		return fmt.Errorf("cannot update domain: %w", err)
	}
	if len(entitySchema.PrimaryFieldDBNames) > 0 {
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

	// Every key column identifies the row, so none of them is written: the SET list leaves
	// them all out and the WHERE names them all.
	key, err := pkPredicate(entitySchema, val)
	if err != nil {
		return fmt.Errorf("cannot update domain: %w", err)
	}
	if err := refuseKeyChange(entitySchema, meta); err != nil {
		return err
	}

	// Extract field values using our new function that supports embedded structs
	updateFields := extractFieldsForUpdate(val, meta)

	// Add fields to SET clause
	for columnName, value := range updateFields {
		builder = builder.Set(columnName, value)
	}

	// A DirtyColumns set that named nothing writable leaves no SET clause, and so does a model
	// whose every column is a key column, such as a join row. An UPDATE with no SET is a syntax
	// error rather than a no-op. Nothing was asked for, so nothing is written, but the meta is
	// still refreshed below, because the caller's view of the entity has not changed either.
	//
	// A caller that asked for the row to be there, through UpdateExisting or a guard, still
	// gets that answer: skipping the statement must not also skip the check that would have
	// followed it, or a row that is gone reads as a success.
	if len(updateFields) == 0 {
		if requireRow || len(meta.UpdateGuard) > 0 {
			if err := o.requireRowWithoutWrite(tableName, entitySchema.PrimaryFieldDBNames, key, meta.UpdateGuard); err != nil {
				return err
			}
		}
		meta.IsDirty = false
		meta.IsLoaded = true
		return nil
	}

	// Refused before the statement is sent, so a Save whose cascade is refused writes nothing
	// at all rather than the entity's own row and then an error (see checkCascade).
	if err := o.checkCascade(entity, entitySchema, val); err != nil {
		return err
	}

	// Add WHERE clause for primary key
	builder = whereAll(builder, key)

	// And the caller's own guard, if it set one. Successive Where calls are ANDed, so this
	// narrows the statement rather than replacing the key predicate.
	for _, guard := range meta.UpdateGuard {
		builder = builder.Where(guard)
	}

	// The columns the server changes on this write — its grgorm:"readback" fields — are read in
	// the UPDATE itself where the dialect's RETURNING can be used on this table, so the entity
	// holds the values this write produced. A separate SELECT afterwards would take a later
	// write's values if another session committed one in between, and a guard on the column
	// would then match over that write (see rereadReadbackFields for where that SELECT is still
	// used). The rows RETURNING yields are the rows the UPDATE matched, so the guard logic below
	// reads its count just as it reads Exec's.
	readback := readbackFields(entitySchema)
	returned := map[string]interface{}{}
	returning := len(readback) > 0 && o.canReturn(entity)
	var queryRes dbCore.QueryResult
	if returning {
		builder = builder.Returning(fieldColumns(readback)...)
		queryRes = o.db.Executor().Find(context.Background(), builder, &returned)
	} else {
		queryRes = o.db.Executor().Exec(context.Background(), builder)
	}
	if queryRes.Error != nil {
		return fmt.Errorf("failed to update domain: %w", queryRes.Error)
	}
	meta.QueryResult = &queryRes

	if (requireRow || len(meta.UpdateGuard) > 0) && queryRes.RowsAffected == 0 {
		exists, existsErr := o.rowExists(tableName, entitySchema.PrimaryFieldDBNames, key)
		if existsErr != nil {
			return existsErr
		}
		if !exists {
			return fmt.Errorf("%w: %s where %s", ErrRowGone, tableName, describeKey(key))
		}
		// The row is there and the statement still matched nothing, so it was the guard that
		// refused. Only reachable when a guard was set: without one, zero matched rows against
		// a row that exists is the ordinary MySQL "wrote the values it already held" case
		// described above, and must not be reported as a failure.
		if len(meta.UpdateGuard) > 0 {
			return fmt.Errorf("%w: %s where %s", ErrRowConflict, tableName, describeKey(key))
		}
	}

	// The columns the server changed on this write replace the entity's copies, which are the
	// values from before it: from the UPDATE's RETURNING when it had one, and otherwise re-read.
	switch {
	case returning && queryRes.Found:
		if err := setReadbackFields(tableName, readback, returned, val); err != nil {
			return err
		}
	case !returning && len(readback) > 0:
		if err := o.rereadReadbackFields(tableName, readback, key, val); err != nil {
			return err
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

// requireRowWithoutWrite answers, for an update that had nothing to write, what the
// statement's row count would have told UpdateExisting and a guarded update: ErrRowGone
// when the row is not there, ErrRowConflict when it is there but the guard does not match
// it, and nil otherwise.
//
// The first probe carries the guard, so the ordinary case costs one read. Only a miss is
// asked again without it, to tell a row that is gone from one that has changed, as the
// statement path does.
func (o *ORM[T]) requireRowWithoutWrite(tableName string, keyColumns []string, key, guard []dbCore.Condition) error {
	guarded := append(append([]dbCore.Condition{}, key...), guard...)
	matched, err := o.rowExists(tableName, keyColumns, guarded)
	if err != nil {
		return err
	}
	if matched {
		return nil
	}
	if len(guard) > 0 {
		exists, err := o.rowExists(tableName, keyColumns, key)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: %s where %s", ErrRowConflict, tableName, describeKey(key))
		}
	}
	return fmt.Errorf("%w: %s where %s", ErrRowGone, tableName, describeKey(key))
}

// rowExists reports whether a row matching conditions — the key, and for
// requireRowWithoutWrite the guard too — is still in the table.
//
// Keyed on every key column and limited to one row, and read through Find rather than a
// COUNT so it goes through the same path createEntity's read-back uses on every engine. It
// used to key on the first column only, so on a composite key a sibling row sharing that
// column answered "still there" for a row that was gone.
func (o *ORM[T]) rowExists(tableName string, keyColumns []string, conditions []dbCore.Condition) (bool, error) {
	sql, args, err := whereAll(o.newBuilder().Select(keyColumns...).From(tableName), conditions).
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

// canReturn reports whether a write of entity may read columns back with the dialect's
// RETURNING: the dialect has one, and it is not one a trigger blocks on a table whose model
// says it has triggers (see TableWithTriggers). Both have to hold for the model's word to
// matter: on an engine whose RETURNING triggers do not block, a model shared with it keeps
// RETURNING there.
func (o *ORM[T]) canReturn(entity any) bool {
	dialect := o.dialect()
	return dbCore.SupportsReturning(dialect) &&
		!(dbCore.ReturningBlockedByTriggers(dialect) && hasTriggers(entity))
}

// errNoKeyToSelectBy is readBackKey's answer for a model with no primary key column.
var errNoKeyToSelectBy = errors.New("the model has no primary key column to select the row by")

// insertAndReadBack performs an INSERT without a RETURNING clause, then reads the
// server-generated columns back. It is how Create reads them on an engine that has no
// RETURNING, and on a table whose triggers make the server refuse the dialect's RETURNING
// (see TableWithTriggers).
//
// The generated key comes from the executor's ExecInsert — the driver's own sql.Result for the
// INSERT, or on SQL Server SCOPE_IDENTITY() read in the same batch — see
// core.LastInsertIDExecutor for why a separate `SELECT LAST_INSERT_ID()` would be unsafe against
// a connection pool. Any remaining generated columns (defaults, computed values, read-back
// fields) are then fetched with one SELECT keyed on every primary key column, so the cost is one
// extra round trip, and none when the key was the only generated column. That SELECT is a
// statement of its own: a write another session commits to the new row between the two is
// what it reads, and only a transaction around both would keep that write out.
//
// inserted holds the columns the INSERT wrote. The reported key is taken only for an
// auto-increment key column the INSERT left out, which is the only one the server chose. A key
// the INSERT wrote is the row's as the entity holds it, zero included — a GUID the caller
// assigned, say — and the read-back is keyed on it. The reported value is not that key's even
// then: it is the value of the table's IDENTITY or auto-increment column, which need not be
// the key, and taking it for the key used to key the read-back on another row, take that
// row's rowversion and key, and hand them to the next guarded Update.
//
// A generated key that still cannot be read back — the INSERT left it to the server, which did
// not report it — is an error that says the row exists. It used to be a warning and a Create
// that returned nil, leaving the entity with a zero key — which every relation saved against
// it then stored — and a caller that retried wrote the row twice. A model with no primary key
// is still only warned about: no key is lost there, and none can select its row.
func (o *ORM[T]) insertAndReadBack(
	builder dbCore.IQueryBuilder,
	entitySchema *schema.Schema,
	returningCols []string,
	entityVal reflect.Value,
	inserted map[string]bool,
) (map[string]interface{}, error) {
	generated := map[string]interface{}{}

	executor, ok := o.db.Executor().(dbCore.LastInsertIDExecutor)
	if !ok {
		// Without a generated-key channel there is no correct way to learn an
		// auto-increment value, and silently returning a zero id would corrupt every
		// relation saved against this entity.
		return nil, fmt.Errorf(
			"orm: %s cannot use RETURNING here and its executor (%T) cannot report a "+
				"generated key, so a created row's primary key cannot be read back",
			o.dialect().Name(), o.db.Executor())
	}

	// Identify the single auto-increment primary key, if there is one. Refused before the
	// INSERT is sent: afterwards the row would exist with a key nobody can name.
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

	res := executor.ExecInsert(context.Background(), builder)
	if res.Error != nil {
		return nil, fmt.Errorf("failed to create domain: %w", res.Error)
	}

	if res.HasLastInsertID && autoIncrementPK != "" && !inserted[autoIncrementPK] {
		generated[autoIncrementPK] = res.LastInsertID
	}

	key, keyErr := readBackKey(entitySchema, generated, entityVal, inserted)

	// Fetch any other generated columns in one read, keyed on the primary key we now
	// know. Skipped entirely when the only generated columns were key columns whose values
	// are already known.
	known := map[string]bool{}
	if keyErr == nil {
		for _, f := range entitySchema.PrimaryFields {
			known[f.DBName] = true
		}
	}
	remaining := make([]string, 0, len(returningCols))
	for _, col := range returningCols {
		if _, have := columnValue(generated, col); !have && !known[col] {
			remaining = append(remaining, col)
		}
	}
	if len(remaining) == 0 {
		return generated, nil
	}
	if errors.Is(keyErr, errNoKeyToSelectBy) {
		log.Log().Warnf("orm: created a row in %s but cannot read back %v: %v",
			entitySchema.Table, remaining, keyErr)
		return generated, nil
	}
	if keyErr != nil {
		return nil, fmt.Errorf("orm: the INSERT into %s succeeded but the generated key could not be "+
			"read back (%v); the row exists: %w", entitySchema.Table, remaining, keyErr)
	}

	sql, args, err := whereAll(o.newBuilder().Select(remaining...).From(entitySchema.Table), key).
		Limit(1).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("orm: cannot render the generated-column read-back: %w", err)
	}

	fetched := map[string]interface{}{}
	queryRes := o.db.Executor().FindRaw(context.Background(), &fetched, sql, args...)
	if queryRes.Error != nil {
		return nil, fmt.Errorf("orm: the INSERT into %s succeeded but its generated columns could not "+
			"be read back; the row exists: %w", entitySchema.Table, queryRes.Error)
	}
	for col, value := range fetched {
		generated[col] = value
	}

	return generated, nil
}

// readBackKey returns the conditions that address the row an INSERT just wrote: one equality
// per primary key column. A column the INSERT wrote (inserted) is keyed on the value the entity
// holds, zero included, since that is the value the row now holds. A column it left out is
// keyed on the value the server reported for it in generated.
//
// A left-out key column the server reported nothing for is an error. So is a model without a
// primary key (errNoKeyToSelectBy): there is nothing to select the row by. A pointer key is
// bound by the value it points at, as pkPredicate binds it, and a nil one, which the INSERT
// wrote as NULL, keys nothing.
//
// A zero key the INSERT wrote used to count as no key at all. On the engines that read the key
// back without RETURNING, a Create with a zero client-assigned key, or with a zero part of a
// composite key, then wrote its row and returned an error, where the same Create through
// RETURNING succeeded.
func readBackKey(
	entitySchema *schema.Schema,
	generated map[string]interface{},
	entityVal reflect.Value,
	inserted map[string]bool,
) ([]dbCore.Condition, error) {
	if len(entitySchema.PrimaryFields) == 0 {
		return nil, fmt.Errorf("%s: %w", entitySchema.Table, errNoKeyToSelectBy)
	}

	entityVal = reflect.Indirect(entityVal)
	key := make([]dbCore.Condition, 0, len(entitySchema.PrimaryFields))
	for _, f := range entitySchema.PrimaryFields {
		var value interface{}
		if inserted[f.DBName] {
			if !entityVal.IsValid() {
				return nil, fmt.Errorf("the entity's value for primary key column %q cannot be read", f.DBName)
			}
			held, _ := f.ValueOf(context.Background(), entityVal)
			if pointer := reflect.ValueOf(held); pointer.Kind() == reflect.Ptr {
				if pointer.IsNil() {
					return nil, fmt.Errorf("the INSERT wrote NULL for primary key column %q", f.DBName)
				}
				held = pointer.Elem().Interface()
			}
			value = held
		} else {
			reported, ok := columnValue(generated, f.DBName)
			if !ok || reported == nil {
				return nil, fmt.Errorf("the INSERT left primary key column %q to the server, which did not "+
					"report the value it generated", f.DBName)
			}
			value = reported
		}
		key = append(key, &dbCore.BinaryCondition{Left: f.DBName, Operator: "=", Right: value})
	}
	return key, nil
}

// columnValue returns the value row holds for column: under exactly that name, else under a
// name equal to it ignoring case, the first in sorted order when several are.
//
// Engines report names as they store them, and that is not always how a model spells them:
// Postgres folds an unquoted RETURNING Id to id, and a collation may report a name in another
// case than the model's tag. A generated column matched only exactly was silently left unset.
func columnValue(row map[string]interface{}, column string) (interface{}, bool) {
	if value, ok := row[column]; ok {
		return value, true
	}
	var folded []string
	for name := range row {
		if strings.EqualFold(name, column) {
			folded = append(folded, name)
		}
	}
	if len(folded) == 0 {
		return nil, false
	}
	sort.Strings(folded)
	return row[folded[0]], true
}

// fieldColumns returns the columns of fields, in order.
func fieldColumns(fields []*schema.Field) []string {
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = f.DBName
	}
	return columns
}

// setReadbackFields sets each of fields on entityVal from the row the UPDATE of tableName
// returned or a re-read selected. A column the row does not hold is left as it was.
func setReadbackFields(tableName string, fields []*schema.Field, row map[string]interface{},
	entityVal reflect.Value) error {
	for _, f := range fields {
		value, ok := columnValue(row, f.DBName)
		if !ok {
			continue
		}
		if err := f.Set(context.Background(), entityVal, value); err != nil {
			return fmt.Errorf("orm: the UPDATE of %s succeeded but its read-back column %s could not be "+
				"set on the entity: %w", tableName, f.DBName, err)
		}
	}
	return nil
}

// rereadReadbackFields selects the entity's grgorm:"readback" columns from its row after an
// UPDATE that could not read them with RETURNING, and sets them on entityVal.
//
// Such a column is one the server changes on every write — a rowversion, a computed column, one
// a trigger maintains — so after an UPDATE the entity's copy is the value from before it, and an
// update guarded on it would conflict with the entity's own write. The row is read by every key
// column, as the UPDATE addressed it. A row that is not there any more, which an unguarded
// UPDATE does not report, leaves the fields as they were.
//
// It is used only where RETURNING cannot be: on an engine without it, such as MySQL, and on a
// table whose model says it has triggers (see TableWithTriggers). The SELECT is then a
// statement of its own, and a write another session commits to the row between the UPDATE and
// it is what it reads: the entity takes that write's rowversion without its values, and its
// next update guarded on the rowversion matches over that write. Only a transaction around both
// statements keeps such a write out, since the UPDATE's lock is then held until the SELECT has
// run. On those tables, where another writer may interleave, Refresh the entity before a
// guarded update that must not overwrite a write it has not seen.
func (o *ORM[T]) rereadReadbackFields(tableName string, fields []*schema.Field, key []dbCore.Condition,
	entityVal reflect.Value) error {
	columns := fieldColumns(fields)

	sql, args, err := whereAll(o.newBuilder().Select(columns...).From(tableName), key).Limit(1).ToSQL()
	if err != nil {
		return fmt.Errorf("orm: cannot render the read-back of %s: %w", tableName, err)
	}

	row := map[string]interface{}{}
	queryRes := o.db.Executor().FindRaw(context.Background(), &row, sql, args...)
	if queryRes.Error != nil {
		return fmt.Errorf("orm: the UPDATE of %s succeeded but its read-back columns %v could not be "+
			"read: %w", tableName, columns, queryRes.Error)
	}
	if !queryRes.Found {
		return nil
	}
	return setReadbackFields(tableName, fields, row, entityVal)
}
