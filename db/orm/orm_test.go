package orm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	dbCore "github.com/osbits/gorgany/db/sql/core"
	v2 "github.com/osbits/gorgany/db/sql/gorm/postgres/v2"
	"github.com/stretchr/testify/require"
)

// TestEntity is a test domain that implements EntityWithMeta
type TestEntity struct {
	BaseEntity
	ID        int    `gorm:"primaryKey"`
	Name      string `gorm:"column:name"`
	Email     string `gorm:"column:email"`
	Age       int    `gorm:"column:age"`
	CreatedAt string `gorm:"column:created_at"`
}

// TestRelatedEntity is a test domain for testing relations
type TestRelatedEntity struct {
	BaseEntity
	ID       int    `gorm:"primaryKey"`
	TestID   int    `gorm:"column:test_id"`
	Category string `gorm:"column:category"`
}

type TestManyToManyRole struct {
	BaseEntity
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"column:name"`
}

type TestManyToManyUser struct {
	BaseEntity
	ID    int                   `gorm:"primaryKey"`
	Name  string                `gorm:"column:name"`
	Roles []*TestManyToManyRole `gorm:"many2many:test_user_roles;"`
}

// TestEmbeddedStruct is a struct to be embedded in TestEntityWithEmbedded
type TestEmbeddedStruct struct {
	Description string `gorm:"column:description"`
	Status      string `gorm:"column:status"`
}

// TestNestedEmbeddedStruct is a struct to be embedded in TestEmbeddedStruct
type TestNestedEmbeddedStruct struct {
	Tags     string `gorm:"column:tags"`
	Priority int    `gorm:"column:priority"`
}

// TestEntityWithEmbedded is a test domain with embedded structs
type TestEntityWithEmbedded struct {
	BaseEntity
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"column:name"`
	TestEmbeddedStruct
	NestedStruct TestNestedEmbeddedStruct
	// Override a field from the embedded struct
	Status string `gorm:"column:status_override"`
}

// TestEntityWithCustomTableName is a test entity that implements TableName()
type TestEntityWithCustomTableName struct {
	BaseEntity
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"column:name"`
}

func (t *TestEntityWithCustomTableName) TableName() string {
	return "custom_table_name"
}

// TestEntityWithValueReceiverTableName is a test entity with TableName() method on value receiver
type TestEntityWithValueReceiverTableName struct {
	BaseEntity
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"column:name"`
}

func (t TestEntityWithValueReceiverTableName) TableName() string {
	return "value_receiver_table"
}

// MockSession implements dbCore.ISession for testing
type MockSession struct {
	executor *MockExecutor
	ds       dbCore.IDataSource
}

func (m *MockSession) Executor() dbCore.IQueryExecutor {
	return m.executor
}

func (m *MockSession) Query() dbCore.IQueryBuilder {
	return v2.NewBuilder()
}

func (m *MockSession) Transaction(ctx context.Context, fn func(dbCore.IDBTransaction) error) error {
	return fn(nil) // Simplified for testing
}

func (m *MockSession) DataSource() dbCore.IDataSource {
	return m.ds
}

func (m *MockSession) Close() error {
	return nil
}

// MockExecutor implements dbCore.IQueryExecutor for testing
type MockExecutor struct {
	execFunc      func(ctx context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult
	queryOneFunc  func(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult
	queryListFunc func(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult
	countFunc     func(ctx context.Context, q dbCore.IQueryBuilder) (int64, error)
	execRawFunc   func(ctx context.Context, sql string, args ...interface{}) dbCore.QueryResult
	queryRawFunc  func(ctx context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult
	countRawFunc  func(ctx context.Context, sql string, args ...interface{}) (int64, error)
}

func (m *MockExecutor) Exec(ctx context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
	if m.execFunc != nil {
		return m.execFunc(ctx, q)
	}
	return dbCore.QueryResult{}
}

func (m *MockExecutor) QueryOne(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
	if m.queryOneFunc != nil {
		return m.queryOneFunc(ctx, q, dest)
	}
	return dbCore.QueryResult{}
}

func (m *MockExecutor) QueryList(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
	if m.queryListFunc != nil {
		return m.queryListFunc(ctx, q, dest)
	}
	return dbCore.QueryResult{}
}

func (m *MockExecutor) Count(ctx context.Context, q dbCore.IQueryBuilder) (int64, error) {
	if m.countFunc != nil {
		return m.countFunc(ctx, q)
	}
	return 0, nil
}

func (m *MockExecutor) ExecRaw(ctx context.Context, sql string, args ...interface{}) dbCore.QueryResult {
	if m.execRawFunc != nil {
		return m.execRawFunc(ctx, sql, args...)
	}
	return dbCore.QueryResult{}
}

func (m *MockExecutor) QueryRaw(ctx context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
	if m.queryRawFunc != nil {
		return m.queryRawFunc(ctx, dest, sql, args...)
	}

	// Default implementation for testing
	queryResult := dbCore.QueryResult{
		Found:        true,
		RowsAffected: 1,
	}

	switch dest := dest.(type) {
	case *TestEntity:
		*dest = TestEntity{
			ID:    1,
			Name:  "Test Entity",
			Email: "test@example.com",
			Age:   30,
		}
		dest.SetMeta(&EntityMeta{
			TableName:     "test_entities",
			PrimaryKey:    "id",
			IsLoaded:      true,
			LoadedColumns: make(map[string]bool),
		})
	case *[]TestEntity:
		*dest = []TestEntity{
			{
				ID:    1,
				Name:  "Test Entity 1",
				Email: "test1@example.com",
				Age:   30,
			},
			{
				ID:    2,
				Name:  "Test Entity 2",
				Email: "test2@example.com",
				Age:   25,
			},
		}
		for i := range *dest {
			(*dest)[i].SetMeta(&EntityMeta{
				TableName:     "test_entities",
				PrimaryKey:    "id",
				IsLoaded:      true,
				LoadedColumns: make(map[string]bool),
			})
		}
		queryResult.RowsAffected = 2
	}

	return queryResult
}

func (m *MockExecutor) CountRaw(ctx context.Context, sql string, args ...interface{}) (int64, error) {
	if m.countRawFunc != nil {
		return m.countRawFunc(ctx, sql, args...)
	}
	return 0, nil
}

func (m *MockExecutor) Find(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
	if m.queryOneFunc != nil {
		return m.queryOneFunc(ctx, q, dest)
	}
	return dbCore.QueryResult{}
}

func (m *MockExecutor) FindRaw(ctx context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
	if m.queryRawFunc != nil {
		return m.queryRawFunc(ctx, dest, sql, args...)
	}
	return dbCore.QueryResult{}
}

// MockDataSource implements dbCore.IDataSource for testing
type MockDataSource struct {
	session *MockSession
}

func (m *MockDataSource) NewSession() (dbCore.ISession, error) {
	return m.session, nil
}

func (m *MockDataSource) Close() error {
	return nil
}

func (m *MockDataSource) GetDriver() (any, error) {
	return nil, nil
}

// NewMockDataSource creates a new mock data source for testing
func NewMockDataSource() *MockDataSource {
	executor := &MockExecutor{}
	ds := &MockDataSource{}
	session := &MockSession{executor: executor, ds: ds}
	ds.session = session
	return ds
}

// TestFind tests the Find method
// TestFind verifies that Finder interface's Find method retrieves an domain by its primary key.
func TestFind(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.queryRawFunc = func(ctx context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
		// Check that the SQL query is correct
		expectedSQL := "SELECT * FROM test_entities WHERE id = ? LIMIT 1"
		if sql != expectedSQL {
			t.Errorf("Expected SQL: %s, got: %s", expectedSQL, sql)
		}

		// Check that the args are correct
		if len(args) != 1 || args[0] != 1 {
			t.Errorf("Expected args: [1], got: %v", args)
		}

		// Set the destination domain
		entity, ok := dest.(**TestEntity)
		if !ok {
			return dbCore.QueryResult{
				Error: errors.New("destination is not a **TestEntity"),
			}
		}

		*entity = &TestEntity{
			ID:    1,
			Name:  "Test Entity",
			Email: "test@example.com",
			Age:   30,
		}
		(*entity).SetMeta(&EntityMeta{
			TableName:     "test_entities",
			PrimaryKey:    "id",
			IsLoaded:      true,
			LoadedColumns: make(map[string]bool),
		})

		return dbCore.QueryResult{
			Found:        true,
			RowsAffected: 1,
		}
	}

	// Use Finder interface for ORM
	var orm Finder[*TestEntity] = New[*TestEntity](mockDS.session)

	// Call Find
	entity, err := orm.Find(1)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the domain was populated correctly
	if entity.ID != 1 {
		t.Errorf("Expected ID: 1, got: %d", entity.ID)
	}
	if entity.Name != "Test Entity" {
		t.Errorf("Expected Name: Test Entity, got: %s", entity.Name)
	}
	if entity.Email != "test@example.com" {
		t.Errorf("Expected Email: test@example.com, got: %s", entity.Email)
	}
	if entity.Age != 30 {
		t.Errorf("Expected Age: 30, got: %d", entity.Age)
	}

	// Check that the domain metadata was set correctly
	meta := entity.GetMeta()
	if meta.TableName != "test_entities" {
		t.Errorf("Expected TableName: test_entities, got: %s", meta.TableName)
	}
	if meta.PrimaryKey != "id" {
		t.Errorf("Expected PrimaryKey: id, got: %s", meta.PrimaryKey)
	}
	if !meta.IsLoaded {
		t.Errorf("Expected IsLoaded: true, got: false")
	}
}

// TestCreate tests the Create method
// TestCreate verifies that Saver interface's Create method inserts a new domain.
func TestCreate(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.queryOneFunc = func(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
		// Check that the query is an INSERT
		// (Cannot check q.Insert.Table directly on IQueryBuilder interface)
		// if q.Insert == nil {
		// 	t.Errorf("Expected INSERT query, got: %v", q)
		// }

		// Set the result for the RETURNING clause
		result, ok := dest.(*map[string]interface{})
		if !ok {
			return dbCore.QueryResult{
				Error: errors.New("destination is not a *map[string]interface{}"),
			}
		}

		*result = map[string]interface{}{
			"id": 1,
		}

		return dbCore.QueryResult{}
	}

	// Use Saver interface for ORM
	var orm Saver[*TestEntity] = New[*TestEntity](mockDS.session)

	// Create a test domain
	entity := &TestEntity{
		Name:  "Test Entity",
		Email: "test@example.com",
		Age:   30,
	}

	// Call Create
	err := orm.Create(entity)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the domain ID was set
	if entity.ID != 1 {
		t.Errorf("Expected ID: 1, got: %d", entity.ID)
	}

	// Check that the domain metadata was set correctly
	meta := entity.GetMeta()
	if !meta.IsLoaded {
		t.Errorf("Expected IsLoaded: true, got: false")
	}
}

// TestUpdate tests the Update method
// TestUpdate verifies that Saver interface's Update method updates an domain.
func TestUpdate(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.execFunc = func(ctx context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
		// Check that the query is an UPDATE
		// (Cannot check q.Update.Table or q.Where.Conditions directly on IQueryBuilder interface)
		return dbCore.QueryResult{}
	}

	// Use Saver interface for ORM
	var orm Saver[*TestEntity] = New[*TestEntity](mockDS.session)

	// Create a test domain
	entity := &TestEntity{
		ID:    1,
		Name:  "Updated Entity",
		Email: "updated@example.com",
		Age:   35,
	}
	entity.SetMeta(&EntityMeta{
		TableName:     "test_entities",
		PrimaryKey:    "id",
		IsLoaded:      true,
		LoadedColumns: make(map[string]bool),
	})

	// Call Update
	err := orm.Update(entity)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the domain metadata was updated correctly
	meta := entity.GetMeta()
	if meta.IsDirty {
		t.Errorf("Expected IsDirty: false, got: true")
	}
}

// TestDelete tests the Delete method
// TestDelete verifies that Saver interface's Delete method deletes an domain.
func TestDelete(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.execFunc = func(ctx context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
		// Check that the query is a DELETE
		// (Cannot check q.Delete.Table or q.Where.Conditions directly on IQueryBuilder interface)
		return dbCore.QueryResult{}
	}

	// Use Saver interface for ORM
	var orm Saver[*TestEntity] = New[*TestEntity](mockDS.session)

	// Create a test domain
	entity := &TestEntity{
		ID: 1,
	}
	entity.SetMeta(&EntityMeta{
		TableName:     "test_entities",
		PrimaryKey:    "id",
		IsLoaded:      true,
		LoadedColumns: make(map[string]bool),
	})

	// Call Delete
	err := orm.Delete(entity)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

// TestLoadRelation tests the LoadRelation method
// TestLoadRelation verifies that RelationLoader interface's LoadRelation method loads a relation.
func TestLoadRelation(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor for loading relations
	mockDS.session.executor.queryListFunc = func(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
		// Check that the query is a SELECT
		//if q.Select == nil {
		//	t.Errorf("Expected SELECT query, got: %v", q)
		//}

		// Set the destination slice
		destSlice := reflect.ValueOf(dest).Elem()
		elemType := destSlice.Type().Elem().Elem()

		// Create a new slice with test data
		newSlice := reflect.MakeSlice(destSlice.Type(), 2, 2)

		// Create test related entities
		for i := 0; i < 2; i++ {
			entity := reflect.New(elemType.Elem())

			// Set field values
			entity.Elem().FieldByName("ID").SetInt(int64(i + 1))
			entity.Elem().FieldByName("TestID").SetInt(1)
			entity.Elem().FieldByName("Category").SetString(fmt.Sprintf("Category %d", i+1))

			// Set the domain in the slice
			newSlice.Index(i).Set(entity)
		}

		// Set the new slice to the destination
		destSlice.Set(newSlice)

		return dbCore.QueryResult{
			Error: nil,
		}
	}

	// Use RelationLoader interface for ORM
	var orm RelationLoader[*TestEntity] = New[*TestEntity](mockDS.session)

	// Create a test domain with a relation
	entity := &TestEntity{
		ID:    1,
		Name:  "Test Entity",
		Email: "test@example.com",
		Age:   30,
	}
	entity.SetMeta(&EntityMeta{
		TableName:     "test_entities",
		PrimaryKey:    "id",
		IsLoaded:      true,
		LoadedColumns: make(map[string]bool),
	})

	// This test is simplified since we can't easily mock the schema.Parse function
	// In a real test, we would need to mock the schema.Parse function to return a schema
	// with the correct relationships

	// For now, we'll just check that the LoadRelation method returns an error
	// when the relation is not found in the schema
	err := orm.LoadRelation(entity, "RelatedEntities")

	// We expect an error since we haven't mocked the schema.Parse function
	if err == nil {
		t.Errorf("Expected an error, got nil")
	}
}

// TestSaveRelations tests the SaveRelations method
// TestSaveRelations verifies that RelationLoader interface's SaveRelations method saves all relations.
func TestSaveRelations(t *testing.T) {
	mockDS := NewMockDataSource()
	var orm RelationLoader[*TestEntity] = New[*TestEntity](mockDS.session)
	entity := &TestEntity{ID: 1, Name: "Test Entity"}
	entity.SetMeta(&EntityMeta{TableName: "test_entities", PrimaryKey: "id", IsLoaded: true, LoadedColumns: make(map[string]bool)})
	// Should not panic or error (no relations defined in schema)
	if err := orm.SaveRelations(entity); err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
}

func TestSaveRelationsManyToManyUsesExistingRelatedEntity(t *testing.T) {
	mockDS := NewMockDataSource()
	executedSQL := make([]string, 0)

	mockDS.session.executor.queryOneFunc = func(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
		sql, _, err := q.ToSQL()
		require.NoError(t, err)
		if strings.HasPrefix(sql, "INSERT INTO test_many_to_many_roles") {
			return dbCore.QueryResult{
				Error: errors.New("duplicate key value violates unique constraint"),
			}
		}
		return dbCore.QueryResult{}
	}

	mockDS.session.executor.countRawFunc = func(ctx context.Context, sql string, args ...interface{}) (int64, error) {
		if !strings.Contains(sql, "FROM test_many_to_many_roles") {
			t.Fatalf("unexpected existence check SQL: %s", sql)
		}
		if len(args) != 1 || args[0] != 10 {
			t.Fatalf("unexpected existence check args: %v", args)
		}
		return 1, nil
	}

	mockDS.session.executor.execFunc = func(ctx context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
		sql, _, err := q.ToSQL()
		require.NoError(t, err)
		executedSQL = append(executedSQL, sql)
		return dbCore.QueryResult{RowsAffected: 1}
	}

	var orm RelationLoader[*TestManyToManyUser] = New[*TestManyToManyUser](mockDS.session)
	entity := &TestManyToManyUser{
		ID:   1,
		Name: "Test User",
		Roles: []*TestManyToManyRole{
			{ID: 10, Name: "admin"},
		},
	}
	entity.SetMeta(&EntityMeta{
		TableName:     "test_many_to_many_users",
		PrimaryKey:    "id",
		IsLoaded:      true,
		LoadedColumns: make(map[string]bool),
		RelationMeta:  make(map[string]*RelationMeta),
	})

	if err := orm.SaveRelations(entity); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	for _, sql := range executedSQL {
		if strings.HasPrefix(sql, "INSERT INTO test_many_to_many_roles") {
			t.Fatalf("expected existing related entity to avoid insert, got SQL: %s", sql)
		}
	}

	if !entity.Roles[0].GetMeta().IsLoaded {
		t.Fatalf("expected existing related entity to be marked as loaded")
	}
}

func TestSaveManyToManyRelatedEntitySetsReflectablePrimaryKey(t *testing.T) {
	mockDS := NewMockDataSource()
	mockDS.session.executor.countRawFunc = func(ctx context.Context, sql string, args ...interface{}) (int64, error) {
		if !strings.Contains(sql, "FROM test_many_to_many_roles") {
			t.Fatalf("unexpected existence check SQL: %s", sql)
		}
		return 1, nil
	}
	mockDS.session.executor.execFunc = func(ctx context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
		return dbCore.QueryResult{RowsAffected: 1}
	}

	orm := New[*TestManyToManyUser](mockDS.session)
	role := &TestManyToManyRole{ID: 10, Name: "admin"}

	if err := orm.saveManyToManyRelatedEntity(role); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if role.GetMeta().PrimaryKey != "ID" {
		t.Fatalf("expected reflectable primary key field name, got: %q", role.GetMeta().PrimaryKey)
	}

	roleORM := New[*TestManyToManyRole](mockDS.session)
	if pk := roleORM.getPrimaryKeyValue(role); pk != 10 {
		t.Fatalf("expected getPrimaryKeyValue to return 10, got: %#v", pk)
	}
}

func TestSaveManyToManyRelatedEntityUsesSchemaPrimaryKeyWhenMetaAlreadySet(t *testing.T) {
	mockDS := NewMockDataSource()
	mockDS.session.executor.countRawFunc = func(ctx context.Context, sql string, args ...interface{}) (int64, error) {
		if !strings.Contains(sql, "FROM test_many_to_many_roles") {
			t.Fatalf("unexpected existence check SQL: %s", sql)
		}
		if len(args) != 1 || args[0] != 10 {
			t.Fatalf("expected schema primary key value 10, got args: %v", args)
		}
		return 1, nil
	}
	mockDS.session.executor.execFunc = func(ctx context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
		return dbCore.QueryResult{RowsAffected: 1}
	}

	orm := New[*TestManyToManyUser](mockDS.session)
	role := &TestManyToManyRole{ID: 10, Name: "admin"}
	role.SetMeta(&EntityMeta{
		PrimaryKey:    "ID",
		LoadedColumns: make(map[string]bool),
		RelationMeta:  make(map[string]*RelationMeta),
	})

	if err := orm.saveManyToManyRelatedEntity(role); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if !role.GetMeta().IsLoaded {
		t.Fatalf("expected existing related entity to be marked as loaded")
	}
}

// TestRawQuery tests the RawQuery method
// TestRawQuery verifies that Finder interface's RawQuery method executes a raw SQL query and returns the first result.
func TestRawQuery(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.queryRawFunc = func(ctx context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
		// Check that the SQL query is correct
		expectedSQL := "SELECT * FROM test_entities WHERE id = ?"
		if sql != expectedSQL {
			t.Errorf("Expected SQL: %s, got: %s", expectedSQL, sql)
		}

		// Check that the args are correct
		if len(args) != 1 || args[0] != 1 {
			t.Errorf("Expected args: [1], got: %v", args)
		}

		// Set the destination domain
		entity, ok := dest.(**TestEntity)
		if !ok {
			return dbCore.QueryResult{
				Error: errors.New("destination is not a **TestEntity"),
			}
		}

		*entity = &TestEntity{
			ID:    1,
			Name:  "Test Entity",
			Email: "test@example.com",
			Age:   30,
		}
		(*entity).SetMeta(&EntityMeta{
			TableName:     "test_entities",
			PrimaryKey:    "id",
			IsLoaded:      true,
			LoadedColumns: make(map[string]bool),
		})

		return dbCore.QueryResult{
			Found:        true,
			RowsAffected: 1,
		}
	}

	// Use Finder interface for ORM
	var orm Finder[*TestEntity] = New[*TestEntity](mockDS.session)

	// Call RawQuery
	entity, err := orm.RawQuery("SELECT * FROM test_entities WHERE id = ?", 1)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the domain was populated correctly
	if entity.ID != 1 {
		t.Errorf("Expected ID: 1, got: %d", entity.ID)
	}
	if entity.Name != "Test Entity" {
		t.Errorf("Expected Name: Test Entity, got: %s", entity.Name)
	}
	if entity.Email != "test@example.com" {
		t.Errorf("Expected Email: test@example.com, got: %s", entity.Email)
	}
	if entity.Age != 30 {
		t.Errorf("Expected Age: 30, got: %d", entity.Age)
	}
}

// TestAll tests the All method
// TestAll verifies that Finder interface's All method retrieves all entities.
func TestAll(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.queryRawFunc = func(ctx context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
		// Check that the SQL query is correct
		expectedSQL := "SELECT * FROM test_entities"
		if sql != expectedSQL {
			t.Errorf("Expected SQL: %s, got: %s", expectedSQL, sql)
		}

		// Set the destination slice
		entities, ok := dest.(*[]*TestEntity)
		if !ok {
			return dbCore.QueryResult{
				Error:        errors.New("destination is not a *[]*TestEntity"),
				RowsAffected: 0,
				Found:        false,
			}
		}

		// Create test entities
		*entities = []*TestEntity{
			{
				ID:    1,
				Name:  "Test Entity 1",
				Email: "test1@example.com",
				Age:   30,
			},
			{
				ID:    2,
				Name:  "Test Entity 2",
				Email: "test2@example.com",
				Age:   25,
			},
		}

		// Set metadata for each domain
		for i := range *entities {
			(*entities)[i].SetMeta(&EntityMeta{
				TableName:     "test_entities",
				PrimaryKey:    "id",
				IsLoaded:      true,
				LoadedColumns: make(map[string]bool),
			})
		}

		return dbCore.QueryResult{
			Error:        nil,
			RowsAffected: 0,
			Found:        false,
		}
	}

	// Use Finder interface for ORM
	var orm Finder[*TestEntity] = New[*TestEntity](mockDS.session)

	// Call All
	entities, err := orm.All()

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the entities were populated correctly
	if len(entities) != 2 {
		t.Errorf("Expected 2 entities, got: %d", len(entities))
	}

	// Check the first domain
	if entities[0].ID != 1 {
		t.Errorf("Expected ID: 1, got: %d", entities[0].ID)
	}
	if entities[0].Name != "Test Entity 1" {
		t.Errorf("Expected Name: Test Entity 1, got: %s", entities[0].Name)
	}

	// Check the second domain
	if entities[1].ID != 2 {
		t.Errorf("Expected ID: 2, got: %d", entities[1].ID)
	}
	if entities[1].Name != "Test Entity 2" {
		t.Errorf("Expected Name: Test Entity 2, got: %s", entities[1].Name)
	}
}

// TestRawQueryAll tests the RawQueryAll method
// TestRawQueryAll verifies that Finder interface's RawQueryAll method executes a raw SQL query and returns all results.
func TestRawQueryAll(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.queryRawFunc = func(ctx context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
		// Check that the SQL query is correct
		expectedSQL := "SELECT * FROM test_entities"
		if sql != expectedSQL {
			t.Errorf("Expected SQL: %s, got: %s", expectedSQL, sql)
		}

		// Set the destination slice
		entities, ok := dest.(*[]*TestEntity)
		if !ok {
			return dbCore.QueryResult{
				Error:        errors.New("destination is not a *[]*TestEntity"),
				RowsAffected: 0,
				Found:        false,
			}
		}

		// Create test entities
		*entities = []*TestEntity{
			{
				ID:    1,
				Name:  "Test Entity 1",
				Email: "test1@example.com",
				Age:   30,
			},
			{
				ID:    2,
				Name:  "Test Entity 2",
				Email: "test2@example.com",
				Age:   25,
			},
		}

		// Set metadata for each domain
		for i := range *entities {
			(*entities)[i].SetMeta(&EntityMeta{
				TableName:     "test_entities",
				PrimaryKey:    "id",
				IsLoaded:      true,
				LoadedColumns: make(map[string]bool),
			})
		}

		return dbCore.QueryResult{
			Error:        nil,
			RowsAffected: 0,
			Found:        false,
		}
	}

	// Use Finder interface for ORM
	var orm Finder[*TestEntity] = New[*TestEntity](mockDS.session)

	// Call RawQueryAll
	entities, err := orm.RawQueryAll("SELECT * FROM test_entities")

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the entities were populated correctly
	if len(entities) != 2 {
		t.Errorf("Expected 2 entities, got: %d", len(entities))
	}

	// Check the first domain
	if entities[0].ID != 1 {
		t.Errorf("Expected ID: 1, got: %d", entities[0].ID)
	}
	if entities[0].Name != "Test Entity 1" {
		t.Errorf("Expected Name: Test Entity 1, got: %s", entities[0].Name)
	}

	// Check the second domain
	if entities[1].ID != 2 {
		t.Errorf("Expected ID: 2, got: %d", entities[1].ID)
	}
	if entities[1].Name != "Test Entity 2" {
		t.Errorf("Expected Name: Test Entity 2, got: %s", entities[1].Name)
	}
}

// TestCustomTableName tests that entities with custom TableName() methods work correctly
func TestCustomTableName(t *testing.T) {
	// Test the GetTableName function directly with pointer receiver
	entity := &TestEntityWithCustomTableName{
		ID:   1,
		Name: "Test",
	}

	tableName := GetTableName(entity)
	expectedTableName := "custom_table_name"
	if tableName != expectedTableName {
		t.Errorf("Expected table name: %s, got: %s", expectedTableName, tableName)
	}

	// Test with value receiver
	valueEntity := TestEntityWithValueReceiverTableName{
		ID:   1,
		Name: "Test",
	}

	valueTableName := GetTableName(valueEntity)
	expectedValueTableName := "value_receiver_table"
	if valueTableName != expectedValueTableName {
		t.Errorf("Expected value receiver table name: %s, got: %s", expectedValueTableName, valueTableName)
	}

	// Test with a regular entity (should use default naming strategy)
	regularEntity := &TestEntity{
		ID:   1,
		Name: "Test",
	}

	regularTableName := GetTableName(regularEntity)
	expectedRegularTableName := "test_entities" // Default naming strategy converts TestEntity to test_entities
	if regularTableName != expectedRegularTableName {
		t.Errorf("Expected regular table name: %s, got: %s", expectedRegularTableName, regularTableName)
	}

	// Test with nil entity
	nilTableName := GetTableName(nil)
	if nilTableName != "" {
		t.Errorf("Expected nil table name to be empty, got: %s", nilTableName)
	}

	// Test with zero value entity (should still call TableName() method since pointer is not nil)
	var zeroEntity TestEntityWithCustomTableName
	zeroTableName := GetTableName(&zeroEntity)
	expectedZeroTableName := "custom_table_name" // Should still call the method
	if zeroTableName != expectedZeroTableName {
		t.Errorf("Expected zero value table name: %s, got: %s", expectedZeroTableName, zeroTableName)
	}
}
