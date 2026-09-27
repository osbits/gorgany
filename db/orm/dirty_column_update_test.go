package orm

import (
	"context"
	"errors"
	"sort"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An UPDATE built from an entity writes every column the struct has, using whatever the
// in-memory copy holds. For an entity two processes share that is a silent overwrite: the
// second process re-sends columns it never touched and undoes what the first one wrote.
//
// EntityMeta.DirtyColumns narrows the SET list to what actually changed, and
// EntityMeta.UpdateGuard adds a predicate that refuses the write when the row moved
// underneath it. These tests fence both, plus the back-compatibility of leaving them unset.

// dirtyTestEntity is a loaded entity whose meta the caller is expected to adjust.
func dirtyTestEntity() *TestEntity {
	entity := &TestEntity{ID: 1, Name: "Entity", Email: "e@example.com", Age: 30, CreatedAt: "2026-01-01"}
	entity.SetMeta(&EntityMeta{
		TableName:     "test_entities",
		PrimaryKey:    "id",
		IsLoaded:      true,
		LoadedColumns: make(map[string]bool),
	})
	return entity
}

// captureUpdate runs an update and returns the statement the executor was handed.
func captureUpdate(t *testing.T, entity *TestEntity, result dbCore.QueryResult,
	rowStillThere bool) (*dbCore.Query, error) {
	t.Helper()

	mockDS := NewMockDataSource()
	var captured *dbCore.Query
	mockDS.session.executor.execFunc = func(_ context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
		captured = q.Build()
		return result
	}
	mockDS.session.executor.queryRawFunc = func(_ context.Context, dest interface{}, _ string, _ ...interface{}) dbCore.QueryResult {
		if !rowStillThere {
			return dbCore.QueryResult{Found: false}
		}
		if row, ok := dest.(*map[string]interface{}); ok {
			*row = map[string]interface{}{"id": int64(1)}
		}
		return dbCore.QueryResult{RowsAffected: 1, Found: true}
	}

	err := New[*TestEntity](mockDS.session).Update(entity)
	return captured, err
}

func setColumns(t *testing.T, query *dbCore.Query) []string {
	t.Helper()
	if query == nil {
		t.Fatal("no statement reached the executor")
	}
	if query.Update == nil {
		t.Fatal("the statement that reached the executor was not an UPDATE")
	}
	columns := make([]string, 0, len(query.Update.Values))
	for column := range query.Update.Values {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	return columns
}

// TestAnUpdateWritesOnlyTheDirtyColumns is the point of the whole mechanism: a caller that
// changed one column must not re-send the other four.
func TestAnUpdateWritesOnlyTheDirtyColumns(t *testing.T) {
	entity := dirtyTestEntity()
	entity.GetMeta().DirtyColumns = map[string]bool{"email": true}

	query, err := captureUpdate(t, entity, dbCore.QueryResult{RowsAffected: 1}, true)
	if err != nil {
		t.Fatalf("the update should have succeeded: %v", err)
	}

	columns := setColumns(t, query)
	if len(columns) != 1 || columns[0] != "email" {
		t.Fatalf("expected the SET list to be exactly [email], got %v — every extra column "+
			"here is one this write would overwrite with a stale local value", columns)
	}
}

// TestTheDirtyColumnSetNeverDropsThePrimaryKeyFromTheWhereClause. The key identifies the row
// rather than being written to it, so filtering it out would leave the statement with no
// predicate — an UPDATE that hits the whole table.
func TestTheDirtyColumnSetNeverDropsThePrimaryKeyFromTheWhereClause(t *testing.T) {
	entity := dirtyTestEntity()
	entity.GetMeta().DirtyColumns = map[string]bool{"email": true}

	query, err := captureUpdate(t, entity, dbCore.QueryResult{RowsAffected: 1}, true)
	if err != nil {
		t.Fatalf("the update should have succeeded: %v", err)
	}

	for _, column := range setColumns(t, query) {
		if column == "id" {
			t.Fatal("the primary key must not appear in the SET list")
		}
	}
	if query.Where == nil || len(query.Where.Conditions) == 0 {
		t.Fatal("the statement carries no WHERE clause, so it would update every row")
	}
}

// TestAnEmptyDirtySetStillWritesEveryColumn is the back-compatibility fence. Every entity in
// the framework other than the session row leaves DirtyColumns unset, and must keep behaving
// exactly as it did.
func TestAnEmptyDirtySetStillWritesEveryColumn(t *testing.T) {
	query, err := captureUpdate(t, dirtyTestEntity(), dbCore.QueryResult{RowsAffected: 1}, true)
	if err != nil {
		t.Fatalf("the update should have succeeded: %v", err)
	}

	columns := setColumns(t, query)
	expected := []string{"age", "created_at", "email", "name"}
	if len(columns) != len(expected) {
		t.Fatalf("expected every non-key column %v, got %v", expected, columns)
	}
	for i := range expected {
		if columns[i] != expected[i] {
			t.Fatalf("expected every non-key column %v, got %v", expected, columns)
		}
	}
}

// TestADirtySetThatNamesNoWritableColumnWritesNothing. An UPDATE with an empty SET clause is a
// syntax error, not a no-op, so the statement must not be sent at all.
func TestADirtySetThatNamesNoWritableColumnWritesNothing(t *testing.T) {
	entity := dirtyTestEntity()
	entity.GetMeta().DirtyColumns = map[string]bool{"no_such_column": true}

	query, err := captureUpdate(t, entity, dbCore.QueryResult{RowsAffected: 1}, true)
	if err != nil {
		t.Fatalf("asking for nothing is not a failure: %v", err)
	}
	if query != nil {
		t.Fatal("a statement with an empty SET clause reached the executor")
	}
}

// TestAGuardedUpdateAddsItsPredicateToTheStatement.
func TestAGuardedUpdateAddsItsPredicateToTheStatement(t *testing.T) {
	entity := dirtyTestEntity()
	entity.GetMeta().UpdateGuard = []dbCore.Condition{
		&dbCore.BinaryCondition{Left: "version", Operator: "=", Right: int64(7)},
	}

	query, err := captureUpdate(t, entity, dbCore.QueryResult{RowsAffected: 1}, true)
	if err != nil {
		t.Fatalf("the update should have succeeded: %v", err)
	}
	if query.Where == nil || len(query.Where.Conditions) != 2 {
		t.Fatalf("expected the key predicate and the guard, got %v", query.Where)
	}
}

// TestAGuardedUpdateWhoseGuardFailsIsAConflictNotASuccess. Somebody else wrote the row first;
// the caller has to re-read it rather than believe its own write landed.
func TestAGuardedUpdateWhoseGuardFailsIsAConflictNotASuccess(t *testing.T) {
	entity := dirtyTestEntity()
	entity.GetMeta().UpdateGuard = []dbCore.Condition{
		&dbCore.BinaryCondition{Left: "version", Operator: "=", Right: int64(7)},
	}

	// Nothing matched, and the row is still there.
	_, err := captureUpdate(t, entity, dbCore.QueryResult{RowsAffected: 0}, true)
	if !errors.Is(err, ErrRowConflict) {
		t.Fatalf("expected ErrRowConflict, got %v", err)
	}
}

// TestAGuardedUpdateAgainstAMissingRowIsStillRowGone. Gone outranks conflict: the caller must
// fail closed rather than re-read a row that is not there.
func TestAGuardedUpdateAgainstAMissingRowIsStillRowGone(t *testing.T) {
	entity := dirtyTestEntity()
	entity.GetMeta().UpdateGuard = []dbCore.Condition{
		&dbCore.BinaryCondition{Left: "version", Operator: "=", Right: int64(7)},
	}

	_, err := captureUpdate(t, entity, dbCore.QueryResult{RowsAffected: 0}, false)
	if !errors.Is(err, ErrRowGone) {
		t.Fatalf("expected ErrRowGone, got %v", err)
	}
	if errors.Is(err, ErrRowConflict) {
		t.Fatal("a row that is gone must not also be reported as a conflict")
	}
}

// TestAnUnguardedNoOpUpdateIsStillNotAConflict pins the MySQL reasoning in updateEntity: that
// engine reports *changed* rows, so an update writing the values a row already holds
// legitimately reports zero. Without a guard, zero says nothing and must stay silent.
func TestAnUnguardedNoOpUpdateIsStillNotAConflict(t *testing.T) {
	_, err := captureUpdate(t, dirtyTestEntity(), dbCore.QueryResult{RowsAffected: 0}, true)
	if err != nil {
		t.Fatalf("an unguarded update that matched nothing must not report an error: %v", err)
	}
}

// TestADirtySetNeverPutsAKeyColumnOfACompositeKeyInTheSetList. Only the first key column used
// to be kept out of the SET list, so an empty DirtyColumns wrote the second one back into the
// row it was supposed to identify, and naming it wrote to whichever row held its new value.
// A full-row write now leaves every key column out, and a dirty set that names one is refused
// before anything is sent (see TestUpdateRefusesChangingAKeyColumn).
func TestADirtySetNeverPutsAKeyColumnOfACompositeKeyInTheSetList(t *testing.T) {
	refusal := func(column string) string {
		return `orm: cannot change primary key column "` + column + `" of order_lines through Update; ` +
			"delete the row and create it again"
	}
	cases := []struct {
		name  string
		dirty map[string]bool
		// wantSQL and wantArgs are the one UPDATE sent. wantErr, when set, is the refusal, and
		// then nothing may be sent.
		wantSQL  string
		wantArgs []any
		wantErr  string
	}{
		{
			name:     "every column",
			wantSQL:  "UPDATE order_lines SET qty = ?, sku = ? WHERE order_id = ? AND line_no = ?",
			wantArgs: []any{3, "sku-1", int64(7), 2},
		},
		{
			name:     "a column that is not a key column",
			dirty:    map[string]bool{"qty": true},
			wantSQL:  "UPDATE order_lines SET qty = ? WHERE order_id = ? AND line_no = ?",
			wantArgs: []any{3, int64(7), 2},
		},
		{name: "a key column named", dirty: map[string]bool{"line_no": true, "qty": true}, wantErr: refusal("line_no")},
		{name: "only key columns named", dirty: map[string]bool{"order_id": true, "line_no": true}, wantErr: refusal("order_id")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDS := NewMockDataSource()
			recorder := recordStatements(t, mockDS)
			line := loadedOrderLine(7, 2)
			line.GetMeta().DirtyColumns = tc.dirty

			err := New[*OrderLine](mockDS.session).Update(line)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, tc.wantErr, err.Error())
				assert.Empty(t, recorder.statements, "a refused update must send nothing")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{tc.wantSQL}, recorder.rendered)
			assert.Equal(t, [][]any{tc.wantArgs}, recorder.args)
		})
	}
}
