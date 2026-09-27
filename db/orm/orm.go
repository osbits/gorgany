package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/util"
	"gorm.io/gorm/schema"
)

// ORM provides generic ORM operations for any domain type
type ORM[T EntityWithMeta] struct {
	db dbCore.ISession `container:"inject"`
}

// New creates a new ORM instance for the given domain type
func New[T EntityWithMeta](db dbCore.ISession) *ORM[T] {
	return &ORM[T]{
		db: db,
	}
}

// newBuilder returns a query builder speaking the dialect of this ORM's session.
//
// Every query the ORM builds must go through here. It used to call
// v2.NewBuilder() — the *Postgres* builder — unconditionally at 11 sites across
// this package, which is why `orm.Create` against MySQL emitted `... RETURNING id`
// and died with error 1064, and why the many-to-many path sent raw
// `ON CONFLICT` to a server that has no such clause. The session was right there
// the whole time; T2.1 threaded the dialect as far as session.Query() and the ORM
// simply never asked.
//
// Reads, updates and deletes happened to survive that, because PostgresDialect
// emits bare unquoted identifiers and Postgres and MySQL both accept
// `LIMIT n OFFSET m`. That was luck, not design: any dialect-specific emission added
// later would have broken all of them at once, as SQL Server's TOP, OFFSET … FETCH and
// bracketed identifiers would have.
func (o *ORM[T]) newBuilder() dbCore.IQueryBuilder {
	if o.db == nil {
		// The ORM is always constructed with a session (New) or has one injected.
		// A nil session is a wiring error, and saying so here beats a nil
		// dereference inside whichever query is being built.
		panic("gorgany/db/orm: ORM has no session, so its dialect is unknown")
	}
	return o.db.Query()
}

// dialect returns the SQL dialect this ORM's session speaks.
func (o *ORM[T]) dialect() dbCore.SQLDialect {
	return o.newBuilder().Dialect()
}

// tableName returns the table T is stored in.
//
// It is the table gorm's schema names, which is what Find and the *ByQuery methods already
// used, falling back to GetTableName for a T gorm cannot parse. All and Count used to call
// GetTableName on `var sample T` directly, and for a pointer model that is a nil *T, which
// GetTableName then named by the naming strategy without asking TableName(). A model whose
// table is not its type's default name was read, and counted, from a table that does not
// exist.
func (o *ORM[T]) tableName() string {
	var sample T
	if entitySchema, err := schema.Parse(sample, &sync.Map{}, schema.NamingStrategy{}); err == nil && entitySchema.Table != "" {
		return entitySchema.Table
	}
	return GetTableName(sample)
}

// Find finds an domain by its ID and returns it.
//
// id is the value of the single primary key column. A model with a composite key is
// refused, since one value cannot address one row: querying by the first column alone
// returns an arbitrary row among those that share it. Query such a model by every key
// column with FirstByQuery.
//
// A row that is not there is not an error: for a pointer model Find returns nil and no
// error. A query that failed is an error. Before, Find tested the error of rendering the
// query a second time instead of the result of running it, so a failed query — a lost
// connection, a missing table, a column the driver could not scan — came back as "not
// found".
func (o *ORM[T]) Find(id interface{}) (T, error) {
	// Create a new domain
	var entity T

	meta := &EntityMeta{
		PrimaryKey:    "id", // Default, will be overridden by schema if available
		LoadedColumns: make(map[string]bool),
		RelationMeta:  make(map[string]*RelationMeta),
	}

	// Try to use schema.Parse to get primary key information
	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err == nil && len(entitySchema.PrimaryFieldDBNames) > 1 {
		return entity, fmt.Errorf(
			"orm: Find(id) cannot address the composite primary key of %s (%s); query by every key column",
			entitySchema.Table, strings.Join(entitySchema.PrimaryFieldDBNames, ", "))
	}
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

	sql, args, err := builder.From(tableName).Eq(primaryKey, id).Limit(1).ToSQL()
	if err != nil {
		return entity, fmt.Errorf("failed to render Find query in ORM for %s: %w", indirectEntityType.Name(), err)
	}

	meta.LastQuery = sql
	meta.LastArgs = args

	// Execute the query
	queryResult := o.db.Executor().FindRaw(context.Background(), &entity, sql, args...)
	if queryResult.Error != nil {
		return entity, fmt.Errorf("failed to execute Find in ORM for %s: %w", indirectEntityType.Name(), queryResult.Error)
	}

	// Only set metadata if the domain is not nil
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

	// Create a sample domain to get metadata
	var sample T

	rSample := reflect.ValueOf(sample)
	indirectSampleType := util.IndirectType(rSample.Type())

	// Get table name
	tableName := o.tableName()

	builder := o.db.Query()
	if tableName != "" {
		builder = builder.From(tableName)
	}

	// Convert to SQL
	sql, args, err := builder.ToSQL()
	if err != nil {
		return entities, fmt.Errorf("failed to render All query in ORM for %s: %w", indirectSampleType.Name(), err)
	}

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

	// Update metadata for each domain
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
	// Create a new domain
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

	// Only set metadata if the domain is not nil
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

	// Update metadata for each domain
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
	tableName := o.tableName()

	// The builder is copy-on-write: every clause method returns a new builder and
	// leaves the receiver untouched. The FROM used to be applied with its result
	// discarded, so Count() emitted "SELECT COUNT(*)" with no FROM clause at all.
	var builder dbCore.IQueryBuilder = o.newBuilder()
	if tableName != "" {
		builder = builder.From(tableName)
	}

	sql, args, err := builder.Select("COUNT(*)").ToSQL()
	if err != nil {
		return 0, fmt.Errorf("failed to render Count query in ORM: %w", err)
	}

	// Execute the query
	count, err := o.db.Executor().CountRaw(context.Background(), sql, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute Count in ORM: %w", err)
	}

	return count, nil
}

// Refresh reloads the domain from the database.
//
// The row is read by every primary key column, by column name (see pkPredicate), and one
// that is gone is ErrRowGone. Refresh used to find the key field by comparing lowercased Go
// field names with the first key's column name, so a key such as UserID/user_id was "not
// found", and it then reloaded through Find, which on a composite key read whichever row
// shared the first column. A row that was gone panicked, copying from the nil entity Find
// returns for a row it did not find.
func (o *ORM[T]) Refresh(entity T) error {
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
		return fmt.Errorf("cannot refresh domain: %w", err)
	}
	if len(entitySchema.PrimaryFieldDBNames) > 0 {
		meta.PrimaryKey = entitySchema.PrimaryFieldDBNames[0]
	}

	// Get the table name
	tableName := meta.TableName
	if tableName == "" {
		tableName = entitySchema.Table
		meta.TableName = tableName
	}

	entityVal := reflect.Indirect(reflect.ValueOf(entity))
	key, err := pkPredicate(entitySchema, entityVal)
	if err != nil {
		return fmt.Errorf("cannot refresh domain: %w", err)
	}

	sql, args, err := whereAll(o.newBuilder().From(tableName), key).Limit(1).ToSQL()
	if err != nil {
		return fmt.Errorf("failed to render Refresh query in ORM for %s: %w", entitySchema.Name, err)
	}

	var refreshedEntity T
	queryResult := o.db.Executor().FindRaw(context.Background(), &refreshedEntity, sql, args...)
	if queryResult.Error != nil {
		return fmt.Errorf("failed to execute Refresh in ORM for %s: %w", entitySchema.Name, queryResult.Error)
	}
	if !queryResult.Found || isNilValue(refreshedEntity) {
		return fmt.Errorf("%w: %s where %s", ErrRowGone, tableName, describeKey(key))
	}

	// Copy the refreshed domain's data to the original domain
	refreshedVal := reflect.Indirect(reflect.ValueOf(refreshedEntity))

	// Copy all fields except Meta. An unexported field cannot be set through reflection, and
	// the scan did not fill it either, so it keeps the value the caller's entity holds.
	//
	// A relation field is not copied either: the scan reads the row, not its relations, so
	// the fresh copy holds nil there. Copying that nil over a loaded relation, while the meta
	// still records the relation as loaded, reads to SaveRelations as "loaded and then
	// cleared", and the next Save deleted every join row of a many-to-many relation. The
	// relations the caller loaded stay as they were.
	for i := 0; i < refreshedVal.NumField(); i++ {
		field := refreshedVal.Type().Field(i)
		if field.Name == "Meta" || field.Name == "BaseEntity" {
			continue
		}
		if _, isRelation := entitySchema.Relationships.Relations[field.Name]; isRelation {
			continue
		}
		target := entityVal.FieldByName(field.Name)
		if !target.CanSet() {
			continue
		}
		target.Set(refreshedVal.Field(i))
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

	var entity T

	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err != nil {
		return entities, fmt.Errorf("failed to parse domain schema in ORM: %w", err)
	}

	rEntity := reflect.ValueOf(entity)
	indirectEntityType := util.IndirectType(rEntity.Type())

	tableName := ""
	if entitySchema != nil {
		tableName = entitySchema.Table
	} else {
		tableName = GetTableName(indirectEntityType)
	}

	qb = qb.From(tableName)
	sql, args, err := qb.ToSQL()
	if err != nil {
		return entities, fmt.Errorf("failed to render AllByQuery in ORM: %w", err)
	}

	queryResult := o.db.Executor().FindRaw(context.Background(), &entities, sql, args...)
	if queryResult.Error != nil {
		return entities, fmt.Errorf("failed to execute AllByQuery in ORM: %w", queryResult.Error)
	}

	// Update metadata for each domain
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
				TableName:     tableName,
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
		return entity, fmt.Errorf("failed to parse domain schema in ORM: %w", err)
	}

	tableName := ""
	if entitySchema != nil {
		tableName = entitySchema.Table
	} else {
		tableName = GetTableName(entity)
	}

	qb = qb.Limit(1).From(tableName)
	sql, args, err := qb.ToSQL()
	if err != nil {
		return entity, fmt.Errorf("failed to render FirstByQuery in ORM: %w", err)
	}

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

// CountByQuery returns how many rows the given query builder's query matches.
//
// It counts every row the query matches, not the page it would return: ORDER BY, LIMIT and
// OFFSET are dropped. The query is derived from a copy of qb.Build(), so qb itself is left
// as it was, and rendered through qb's own dialect. See countQuery for how a grouped,
// DISTINCT or UNION query is counted, and for the queries it refuses.
//
// The count reads from qb's own FROM when it has one, but AllByQuery and FirstByQuery still
// replace the FROM with the model's table. A builder whose FROM names another source counts
// that source while those two read the model's table, so a paginated list's total and its
// page disagree. Leave the FROM unset, or set it to the model's table, for a builder that is
// passed to both.
func (o *ORM[T]) CountByQuery(qb dbCore.IQueryBuilder) (int64, error) {
	if qb == nil {
		return 0, errors.New("orm: CountByQuery needs a query builder")
	}

	var entity T

	schemaCache := &sync.Map{}
	entitySchema, err := schema.Parse(entity, schemaCache, schema.NamingStrategy{})
	if err != nil {
		return 0, fmt.Errorf("failed to parse domain schema in ORM: %w", err)
	}

	tableName := entitySchema.Table
	if tableName == "" {
		tableName = GetTableName(entity)
	}

	query, err := countQuery(qb.Build(), tableName)
	if err != nil {
		return 0, err
	}

	dialect := qb.Dialect()
	if dialect == nil {
		dialect = o.dialect()
	}
	sql, args, err := dialect.FormatQuery(query)
	if err != nil {
		return 0, fmt.Errorf("failed to render CountByQuery in ORM: %w", err)
	}

	count, err := o.db.Executor().CountRaw(context.Background(), sql, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to execute CountByQuery in ORM: %w", err)
	}

	return count, nil
}
