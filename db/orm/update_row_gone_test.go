package orm

import (
	"context"
	"errors"
	"strings"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestUpdateExistingProbesACompositeKeyByEveryColumn. The existence probe keyed on the first
// key column only, so when line 2 of order 7 was gone, line 1 answered "still there" and the
// write with nowhere to land was reported as a success. The probe here answers the way such
// a table would: a query that pins line_no finds nothing, one that does not finds the
// sibling.
func TestUpdateExistingProbesACompositeKeyByEveryColumn(t *testing.T) {
	mockDS := NewMockDataSource()
	mockDS.session.executor.execFunc = func(context.Context, dbCore.IQueryBuilder) dbCore.QueryResult {
		return dbCore.QueryResult{RowsAffected: 0}
	}
	var probe string
	var probeArgs []any
	mockDS.session.executor.queryRawFunc = func(_ context.Context, _ interface{}, sql string, args ...interface{}) dbCore.QueryResult {
		probe, probeArgs = sql, args
		if strings.Contains(sql, "line_no = ?") {
			return dbCore.QueryResult{Found: false}
		}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}

	err := New[*OrderLine](mockDS.session).UpdateExisting(loadedOrderLine(7, 2))

	require.True(t, errors.Is(err, ErrRowGone), "expected ErrRowGone, got %v", err)
	assert.Equal(t, "orm: the row this entity was loaded from no longer exists: "+
		"order_lines where order_id = 7 AND line_no = 2", err.Error(),
		"the message must name the whole key, not only its first column")
	assert.Equal(t, "SELECT order_id, line_no FROM order_lines WHERE order_id = ? AND line_no = ? LIMIT 1", probe)
	assert.Equal(t, []any{int64(7), 2}, probeArgs)
}

// LinkRow is a join row: every column it has is part of its key, so an update of it has
// nothing to write.
type LinkRow struct {
	BaseEntity
	LeftID  int64 `gorm:"primaryKey;autoIncrement:false;column:left_id"`
	RightID int64 `gorm:"primaryKey;autoIncrement:false;column:right_id"`
}

// TestUpdateExistingChecksTheRowWhenThereIsNothingToWrite. Every key column is left out of
// the SET list, so a model whose columns are all key columns has an empty one, and the
// update returns before sending a statement. It used to return before the check that
// statement would have been followed by, too, so UpdateExisting of a join row that was gone
// reported success. The check now runs without the statement: the key and any guard in one
// probe, and only a miss under a guard is probed again, to tell gone from changed.
func TestUpdateExistingChecksTheRowWhenThereIsNothingToWrite(t *testing.T) {
	loadedLink := func() *LinkRow {
		link := &LinkRow{LeftID: 1, RightID: 2}
		link.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}})
		return link
	}
	guard := &dbCore.BinaryCondition{Left: "left_id", Operator: ">", Right: 0}
	const keyed = "SELECT left_id, right_id FROM link_rows WHERE left_id = ? AND right_id = ? LIMIT 1"
	const guarded = "SELECT left_id, right_id FROM link_rows WHERE left_id = ? AND right_id = ? AND left_id > ? LIMIT 1"

	cases := []struct {
		name  string
		call  func(*ORM[*LinkRow], *LinkRow) error
		guard bool
		// found answers each probe by its SQL.
		found   map[string]bool
		wantErr error
		// wantProbes are the probes sent, in order.
		wantProbes []string
	}{
		{name: "gone", call: (*ORM[*LinkRow]).UpdateExisting, found: map[string]bool{}, wantErr: ErrRowGone, wantProbes: []string{keyed}},
		{name: "still there", call: (*ORM[*LinkRow]).UpdateExisting, found: map[string]bool{keyed: true}, wantProbes: []string{keyed}},
		{name: "guard matches", call: (*ORM[*LinkRow]).Update, guard: true, found: map[string]bool{guarded: true}, wantProbes: []string{guarded}},
		{name: "guard lost", call: (*ORM[*LinkRow]).Update, guard: true, found: map[string]bool{keyed: true}, wantErr: ErrRowConflict, wantProbes: []string{guarded, keyed}},
		{name: "guarded and gone", call: (*ORM[*LinkRow]).Update, guard: true, found: map[string]bool{}, wantErr: ErrRowGone, wantProbes: []string{guarded, keyed}},
		// Update without a guard never asked whether the row is there, and still does not.
		{name: "plain update", call: (*ORM[*LinkRow]).Update, found: map[string]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockDS := NewMockDataSource()
			recorder := recordStatements(t, mockDS)
			var probes []string
			mockDS.session.executor.queryRawFunc = func(_ context.Context, _ interface{}, sql string, _ ...interface{}) dbCore.QueryResult {
				probes = append(probes, sql)
				return dbCore.QueryResult{Found: tc.found[sql]}
			}
			link := loadedLink()
			if tc.guard {
				link.GetMeta().UpdateGuard = []dbCore.Condition{guard}
			}

			err := tc.call(New[*LinkRow](mockDS.session), link)

			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.True(t, errors.Is(err, tc.wantErr), "expected %v, got %v", tc.wantErr, err)
				assert.Equal(t, tc.wantErr.Error()+": link_rows where left_id = 1 AND right_id = 2", err.Error())
			}
			assert.Equal(t, tc.wantProbes, probes)
			assert.Empty(t, recorder.rendered, "there is nothing to write, so no UPDATE may be sent")
		})
	}
}
