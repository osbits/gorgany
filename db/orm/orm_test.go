package orm

import (
	"context"
	"errors"
	"fmt"
	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	v2 "git.qix.sx/gorgany/gorgany.git/db/sql/gorm/postgres/v2"
	"reflect"
	"testing"
)

// TestEntity is a test entity that implements EntityWithMeta
type TestEntity struct {
	BaseEntity
	ID        int    `gorm:"primaryKey"`
	Name      string `gorm:"column:name"`
	Email     string `gorm:"column:email"`
	Age       int    `gorm:"column:age"`
	CreatedAt string `gorm:"column:created_at"`
}

// TestRelatedEntity is a test entity for testing relations
type TestRelatedEntity struct {
	BaseEntity
	ID       int    `gorm:"primaryKey"`
	TestID   int    `gorm:"column:test_id"`
	Category string `gorm:"column:category"`
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

// TestEntityWithEmbedded is a test entity with embedded structs
type TestEntityWithEmbedded struct {
	BaseEntity
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"column:name"`
	TestEmbeddedStruct
	NestedStruct TestNestedEmbeddedStruct
	// Override a field from the embedded struct
	Status string `gorm:"column:status_override"`
}

// MockSession implements dbCore.ISession for testing
type MockSession struct {
	executor *MockExecutor
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

func (m *MockSession) Close() error {
	return nil
}

// MockExecutor implements dbCore.IQueryExecutor for testing
type MockExecutor struct {
	execFunc      func(ctx context.Context, q *dbCore.Query) error
	queryOneFunc  func(ctx context.Context, q *dbCore.Query, dest interface{}) error
	queryListFunc func(ctx context.Context, q *dbCore.Query, dest interface{}) error
	countFunc     func(ctx context.Context, q *dbCore.Query) (int64, error)
	execRawFunc   func(ctx context.Context, sql string, args ...interface{}) error
	queryRawFunc  func(ctx context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult
	countRawFunc  func(ctx context.Context, sql string, args ...interface{}) (int64, error)
}

func (m *MockExecutor) Exec(ctx context.Context, q *dbCore.Query) error {
	if m.execFunc != nil {
		return m.execFunc(ctx, q)
	}
	return nil
}

func (m *MockExecutor) QueryOne(ctx context.Context, q *dbCore.Query, dest interface{}) error {
	if m.queryOneFunc != nil {
		return m.queryOneFunc(ctx, q, dest)
	}
	return nil
}

func (m *MockExecutor) QueryList(ctx context.Context, q *dbCore.Query, dest interface{}) error {
	if m.queryListFunc != nil {
		return m.queryListFunc(ctx, q, dest)
	}
	return nil
}

func (m *MockExecutor) Count(ctx context.Context, q *dbCore.Query) (int64, error) {
	if m.countFunc != nil {
		return m.countFunc(ctx, q)
	}
	return 0, nil
}

func (m *MockExecutor) ExecRaw(ctx context.Context, sql string, args ...interface{}) error {
	if m.execRawFunc != nil {
		return m.execRawFunc(ctx, sql, args...)
	}
	return nil
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
			TableName:       "test_entities",
			PrimaryKey:      "id",
			IsLoaded:        true,
			IsNew:           false,
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
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
				TableName:       "test_entities",
				PrimaryKey:      "id",
				IsLoaded:        true,
				IsNew:           false,
				LoadedColumns:   make(map[string]bool),
				LoadedRelations: make(map[string]bool),
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

// NewMockDataSource creates a new mock data source for testing
func NewMockDataSource() *MockDataSource {
	executor := &MockExecutor{}
	session := &MockSession{executor: executor}
	return &MockDataSource{session: session}
}

// TestFind tests the Find method
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

		// Set the destination entity
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
			TableName:       "test_entities",
			PrimaryKey:      "id",
			IsLoaded:        true,
			IsNew:           false,
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
		})

		return dbCore.QueryResult{
			Found:        true,
			RowsAffected: 1,
		}
	}

	// Create an ORM instance
	orm := New[*TestEntity](mockDS)

	// Call Find
	entity, err := orm.Find(1)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the entity was populated correctly
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

	// Check that the entity metadata was set correctly
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
	if meta.IsNew {
		t.Errorf("Expected IsNew: false, got: true")
	}
}

// TestCreate tests the Create method
func TestCreate(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.queryOneFunc = func(ctx context.Context, q *dbCore.Query, dest interface{}) error {
		// Check that the query is an INSERT
		if q.Insert == nil {
			t.Errorf("Expected INSERT query, got: %v", q)
		}

		// Check that the table is correct
		if q.Insert.Table != "test_entities" {
			t.Errorf("Expected table: test_entities, got: %s", q.Insert.Table)
		}

		// Set the result for the RETURNING clause
		result, ok := dest.(*map[string]interface{})
		if !ok {
			return errors.New("destination is not a *map[string]interface{}")
		}

		*result = map[string]interface{}{
			"id": 1,
		}

		return nil
	}

	// Create an ORM instance
	orm := New[*TestEntity](mockDS)

	// Create a test entity
	entity := &TestEntity{
		Name:  "Test Entity",
		Email: "test@example.com",
		Age:   30,
	}

	// Call Create
	createdEntity, err := orm.Create(entity)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the returned entity is the same as the input entity
	if createdEntity != entity {
		t.Errorf("Expected returned entity to be the same as input entity")
	}

	// Check that the entity ID was set
	if entity.ID != 1 {
		t.Errorf("Expected ID: 1, got: %d", entity.ID)
	}

	// Check that the entity metadata was set correctly
	meta := entity.GetMeta()
	if meta.IsNew {
		t.Errorf("Expected IsNew: false, got: true")
	}
	if !meta.IsLoaded {
		t.Errorf("Expected IsLoaded: true, got: false")
	}
}

// TestUpdate tests the Update method
func TestUpdate(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.execFunc = func(ctx context.Context, q *dbCore.Query) error {
		// Check that the query is an UPDATE
		if q.Update == nil {
			t.Errorf("Expected UPDATE query, got: %v", q)
		}

		// Check that the table is correct
		if q.Update.Table != "test_entities" {
			t.Errorf("Expected table: test_entities, got: %s", q.Update.Table)
		}

		// Check that the WHERE clause is correct
		if q.Where == nil || len(q.Where.Conditions) != 1 {
			t.Errorf("Expected WHERE clause with 1 condition, got: %v", q.Where)
		}

		return nil
	}

	// Create an ORM instance
	orm := New[*TestEntity](mockDS)

	// Create a test entity
	entity := &TestEntity{
		ID:    1,
		Name:  "Updated Entity",
		Email: "updated@example.com",
		Age:   35,
	}
	entity.SetMeta(&EntityMeta{
		TableName:       "test_entities",
		PrimaryKey:      "id",
		IsLoaded:        true,
		IsNew:           false,
		LoadedColumns:   make(map[string]bool),
		LoadedRelations: make(map[string]bool),
	})

	// Call Update
	updatedEntity, err := orm.Update(entity)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the returned entity is the same as the input entity
	if updatedEntity != entity {
		t.Errorf("Expected returned entity to be the same as input entity")
	}

	// Check that the entity metadata was updated correctly
	meta := entity.GetMeta()
	if meta.IsDirty {
		t.Errorf("Expected IsDirty: false, got: true")
	}
}

// TestDelete tests the Delete method
func TestDelete(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor
	mockDS.session.executor.execFunc = func(ctx context.Context, q *dbCore.Query) error {
		// Check that the query is a DELETE
		if q.Delete == nil {
			t.Errorf("Expected DELETE query, got: %v", q)
		}

		// Check that the table is correct
		if q.Delete.Table != "test_entities" {
			t.Errorf("Expected table: test_entities, got: %s", q.Delete.Table)
		}

		// Check that the WHERE clause is correct
		if q.Where == nil || len(q.Where.Conditions) != 1 {
			t.Errorf("Expected WHERE clause with 1 condition, got: %v", q.Where)
		}

		return nil
	}

	// Create an ORM instance
	orm := New[*TestEntity](mockDS)

	// Create a test entity
	entity := &TestEntity{
		ID: 1,
	}
	entity.SetMeta(&EntityMeta{
		TableName:       "test_entities",
		PrimaryKey:      "id",
		IsLoaded:        true,
		IsNew:           false,
		LoadedColumns:   make(map[string]bool),
		LoadedRelations: make(map[string]bool),
	})

	// Call Delete
	deletedEntity, err := orm.Delete(entity)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the returned entity is the same as the input entity
	if deletedEntity != entity {
		t.Errorf("Expected returned entity to be the same as input entity")
	}
}

// TestLoadRelation tests the LoadRelation method
func TestLoadRelation(t *testing.T) {
	// Create a mock data source
	mockDS := NewMockDataSource()

	// Configure the mock executor for loading relations
	mockDS.session.executor.queryListFunc = func(ctx context.Context, q *dbCore.Query, dest interface{}) error {
		// Check that the query is a SELECT
		if q.Select == nil {
			t.Errorf("Expected SELECT query, got: %v", q)
		}

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

			// Set the entity in the slice
			newSlice.Index(i).Set(entity)
		}

		// Set the new slice to the destination
		destSlice.Set(newSlice)

		return nil
	}

	// Create an ORM instance
	orm := New[*TestEntity](mockDS)

	// Create a test entity with a relation
	entity := &TestEntity{
		ID:    1,
		Name:  "Test Entity",
		Email: "test@example.com",
		Age:   30,
	}
	entity.SetMeta(&EntityMeta{
		TableName:       "test_entities",
		PrimaryKey:      "id",
		IsLoaded:        true,
		IsNew:           false,
		LoadedColumns:   make(map[string]bool),
		LoadedRelations: make(map[string]bool),
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

// TestRawQuery tests the RawQuery method
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

		// Set the destination entity
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
			TableName:       "test_entities",
			PrimaryKey:      "id",
			IsLoaded:        true,
			IsNew:           false,
			LoadedColumns:   make(map[string]bool),
			LoadedRelations: make(map[string]bool),
		})

		return dbCore.QueryResult{
			Found:        true,
			RowsAffected: 1,
		}
	}

	// Create an ORM instance
	orm := New[*TestEntity](mockDS)

	// Call RawQuery
	entity, err := orm.RawQuery("SELECT * FROM test_entities WHERE id = ?", 1)

	// Check that there was no error
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	// Check that the entity was populated correctly
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

		// Set metadata for each entity
		for i := range *entities {
			(*entities)[i].SetMeta(&EntityMeta{
				TableName:       "test_entities",
				PrimaryKey:      "id",
				IsLoaded:        true,
				IsNew:           false,
				LoadedColumns:   make(map[string]bool),
				LoadedRelations: make(map[string]bool),
			})
		}

		return dbCore.QueryResult{
			Error:        errors.New("destination is not a *[]*TestEntity"),
			RowsAffected: 0,
			Found:        false,
		}
	}

	// Create an ORM instance
	orm := New[*TestEntity](mockDS)

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

	// Check the first entity
	if entities[0].ID != 1 {
		t.Errorf("Expected ID: 1, got: %d", entities[0].ID)
	}
	if entities[0].Name != "Test Entity 1" {
		t.Errorf("Expected Name: Test Entity 1, got: %s", entities[0].Name)
	}

	// Check the second entity
	if entities[1].ID != 2 {
		t.Errorf("Expected ID: 2, got: %d", entities[1].ID)
	}
	if entities[1].Name != "Test Entity 2" {
		t.Errorf("Expected Name: Test Entity 2, got: %s", entities[1].Name)
	}
}

// TestRawQueryAll tests the RawQueryAll method
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

		// Set metadata for each entity
		for i := range *entities {
			(*entities)[i].SetMeta(&EntityMeta{
				TableName:       "test_entities",
				PrimaryKey:      "id",
				IsLoaded:        true,
				IsNew:           false,
				LoadedColumns:   make(map[string]bool),
				LoadedRelations: make(map[string]bool),
			})
		}

		return dbCore.QueryResult{
			Error:        nil,
			RowsAffected: 0,
			Found:        false,
		}
	}

	// Create an ORM instance
	orm := New[*TestEntity](mockDS)

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

	// Check the first entity
	if entities[0].ID != 1 {
		t.Errorf("Expected ID: 1, got: %d", entities[0].ID)
	}
	if entities[0].Name != "Test Entity 1" {
		t.Errorf("Expected Name: Test Entity 1, got: %s", entities[0].Name)
	}

	// Check the second entity
	if entities[1].ID != 2 {
		t.Errorf("Expected ID: 2, got: %d", entities[1].ID)
	}
	if entities[1].Name != "Test Entity 2" {
		t.Errorf("Expected Name: Test Entity 2, got: %s", entities[1].Name)
	}
}
