package orm

import (
	"context"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
)

// An UPDATE keyed on a primary key that no longer exists matches nothing. The
// statement succeeds, so a caller that only looks at the error cannot tell a write
// that landed from a write that had nowhere to land.

func loadedTestEntity() *TestEntity {
	entity := &TestEntity{ID: 1, Name: "Entity", Email: "e@example.com", Age: 30}
	entity.SetMeta(&EntityMeta{
		TableName:     "test_entities",
		PrimaryKey:    "id",
		IsLoaded:      true,
		LoadedColumns: make(map[string]bool),
	})
	return entity
}

// TestUpdateRecordsWhetherARowWasMatched.
func TestUpdateRecordsWhetherARowWasMatched(t *testing.T) {
	mockDS := NewMockDataSource()
	mockDS.session.executor.execFunc = func(context.Context, dbCore.IQueryBuilder) dbCore.QueryResult {
		return dbCore.QueryResult{RowsAffected: 0}
	}

	entity := loadedTestEntity()
	orm := New[*TestEntity](mockDS.session)

	if err := orm.Update(entity); err != nil {
		t.Fatalf("the statement itself succeeded, so Update must not report an error: %v", err)
	}

	result := entity.GetMeta().GetQueryResult()
	if result == nil {
		t.Fatal("the update left no record of how many rows it matched, so a caller " +
			"cannot tell a write that landed from one that had nowhere to land")
	}
	if result.RowsAffected != 0 {
		t.Fatalf("expected the recorded row count to be 0, got %d", result.RowsAffected)
	}
}

// TestUpdateExistingFailsWhenTheRowIsGone. Zero matched rows is ambiguous on its own:
// MySQL counts changed rows rather than matched ones by default, so a no-op update of
// a row that is present also reports zero. The ambiguity is resolved by asking whether
// the row is there, and only the absent case is an error.
func TestUpdateExistingFailsWhenTheRowIsGone(t *testing.T) {
	mockDS := NewMockDataSource()
	mockDS.session.executor.execFunc = func(context.Context, dbCore.IQueryBuilder) dbCore.QueryResult {
		return dbCore.QueryResult{RowsAffected: 0}
	}
	// The row is gone: the existence check finds nothing.
	mockDS.session.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
		return dbCore.QueryResult{RowsAffected: 0, Found: false}
	}

	orm := New[*TestEntity](mockDS.session)
	if err := orm.UpdateExisting(loadedTestEntity()); err == nil {
		t.Fatal("an update against a row that is gone must not report success")
	}
}

// TestUpdateExistingAcceptsANoOpUpdate is the other half: a present row whose columns
// did not change must not be reported as gone.
func TestUpdateExistingAcceptsANoOpUpdate(t *testing.T) {
	mockDS := NewMockDataSource()
	mockDS.session.executor.execFunc = func(context.Context, dbCore.IQueryBuilder) dbCore.QueryResult {
		return dbCore.QueryResult{RowsAffected: 0}
	}
	mockDS.session.executor.queryRawFunc = func(_ context.Context, dest interface{}, _ string, _ ...interface{}) dbCore.QueryResult {
		if row, ok := dest.(*map[string]interface{}); ok {
			*row = map[string]interface{}{"id": int64(1)}
		}
		return dbCore.QueryResult{RowsAffected: 1, Found: true}
	}

	orm := New[*TestEntity](mockDS.session)
	if err := orm.UpdateExisting(loadedTestEntity()); err != nil {
		t.Fatalf("a present row whose columns did not change is not a missing row: %v", err)
	}
}
