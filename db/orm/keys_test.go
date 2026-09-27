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

// Every statement that addresses one existing row — Delete, Update, UpdateExisting's
// existence probe and Refresh — used to address it by the first primary key column only,
// and Delete by that column's Go field name rather than its column name. On a composite key
// that wrote, deleted or reloaded every row sharing the first part; on a key whose Go name
// differs from its column it sent a column that does not exist. These tests pin the key
// predicate those paths now share.

// OrderLine has a composite primary key: a line is only one row given both parts.
type OrderLine struct {
	BaseEntity
	OrderID int64  `gorm:"primaryKey;autoIncrement:false;column:order_id"`
	LineNo  int    `gorm:"primaryKey;autoIncrement:false;column:line_no"`
	Sku     string `gorm:"column:sku"`
	Qty     int    `gorm:"column:qty"`
}

func (OrderLine) TableName() string { return "order_lines" }

// RenamedKeyEntity's key has a Go name, UserID, that is not its column name, user_id.
type RenamedKeyEntity struct {
	BaseEntity
	UserID string `gorm:"primaryKey;column:user_id"`
	Name   string `gorm:"column:name"`
}

func loadedOrderLine(orderID int64, lineNo int) *OrderLine {
	line := &OrderLine{OrderID: orderID, LineNo: lineNo, Sku: "sku-1", Qty: 3}
	line.SetMeta(&EntityMeta{
		TableName:     "order_lines",
		PrimaryKey:    "order_id",
		IsLoaded:      true,
		LoadedColumns: make(map[string]bool),
	})
	return line
}

// statementRecorder captures every builder statement and raw query the mock executor is
// handed, so a test can assert on what reached the database and on what did not.
type statementRecorder struct {
	statements []*dbCore.Query
	rendered   []string
	args       [][]any
	raw        []string
	rawArgs    [][]any
}

func recordStatements(t *testing.T, mockDS *MockDataSource) *statementRecorder {
	t.Helper()
	recorder := &statementRecorder{}
	mockDS.session.executor.execFunc = func(_ context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
		sql, args, err := q.ToSQL()
		require.NoError(t, err)
		recorder.statements = append(recorder.statements, q.Build())
		recorder.rendered = append(recorder.rendered, sql)
		recorder.args = append(recorder.args, args)
		return dbCore.QueryResult{RowsAffected: 1}
	}
	return recorder
}

// setColumnsOf returns the sorted SET list of an UPDATE.
func setColumnsOf(t *testing.T, query *dbCore.Query) []string {
	t.Helper()
	require.NotNil(t, query)
	require.NotNil(t, query.Update, "the statement was not an UPDATE")
	columns := make([]string, 0, len(query.Update.Values))
	for column := range query.Update.Values {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	return columns
}

// TestDeleteWhereUsesColumnName pins the Delete that sent `WHERE UserID = ?`: the WHERE
// names the key's column, and meta.PrimaryKey is left holding the column name too, since
// the Go name used to leak into it for every later call to read.
func TestDeleteWhereUsesColumnName(t *testing.T) {
	mockDS := NewMockDataSource()
	recorder := recordStatements(t, mockDS)

	entity := &RenamedKeyEntity{UserID: "u-1", Name: "Ada"}
	require.NoError(t, New[*RenamedKeyEntity](mockDS.session).Delete(entity))

	require.Len(t, recorder.rendered, 1)
	assert.Equal(t, "DELETE FROM renamed_key_entities WHERE user_id = ?", recorder.rendered[0])
	assert.Equal(t, []any{"u-1"}, recorder.args[0])
	assert.Equal(t, "user_id", entity.GetMeta().PrimaryKey,
		"meta.PrimaryKey must hold the column name, not the Go field name")
}

// TestCompositeKeyUpdateAndDeleteMatchEveryKeyColumn is the row-safety fence: with only the
// first column in the WHERE, updating line 2 of order 7 rewrote every line of order 7, and
// deleting it deleted the whole order. The SET list leaves every key column out, since the
// WHERE names them.
func TestCompositeKeyUpdateAndDeleteMatchEveryKeyColumn(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordStatements(t, mockDS)

		require.NoError(t, New[*OrderLine](mockDS.session).Update(loadedOrderLine(7, 2)))

		require.Len(t, recorder.statements, 1)
		assert.Equal(t, []string{"qty", "sku"}, setColumnsOf(t, recorder.statements[0]),
			"no key column may be written back")
		assert.Equal(t, "UPDATE order_lines SET qty = ?, sku = ? WHERE order_id = ? AND line_no = ?",
			recorder.rendered[0])
		assert.Equal(t, []any{3, "sku-1", int64(7), 2}, recorder.args[0],
			"the WHERE must bind both parts of the key")
	})

	t.Run("delete", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordStatements(t, mockDS)

		require.NoError(t, New[*OrderLine](mockDS.session).Delete(loadedOrderLine(7, 2)))

		require.Len(t, recorder.rendered, 1)
		assert.Equal(t, "DELETE FROM order_lines WHERE order_id = ? AND line_no = ?", recorder.rendered[0])
		assert.Equal(t, []any{int64(7), 2}, recorder.args[0])
	})
}

// TestCompositeKeyWithAZeroPartIsRefused. A zero part addresses a row that was never
// written, or — on an engine that accepts it — the part of the key the caller forgot to
// set, so the statement must not be sent at all.
func TestCompositeKeyWithAZeroPartIsRefused(t *testing.T) {
	operations := []struct {
		name      string
		operation func(*ORM[*OrderLine], *OrderLine) error
		verb      string
	}{
		{"update", (*ORM[*OrderLine]).Update, "update"},
		{"delete", (*ORM[*OrderLine]).Delete, "delete"},
		{"refresh", (*ORM[*OrderLine]).Refresh, "refresh"},
	}
	for _, tc := range operations {
		operation := tc.operation
		t.Run(tc.name, func(t *testing.T) {
			mockDS := NewMockDataSource()
			recorder := recordStatements(t, mockDS)
			mockDS.session.executor.queryRawFunc = func(_ context.Context, _ interface{}, sql string, args ...interface{}) dbCore.QueryResult {
				recorder.raw = append(recorder.raw, sql)
				return dbCore.QueryResult{Found: true, RowsAffected: 1}
			}

			err := operation(New[*OrderLine](mockDS.session), loadedOrderLine(7, 0))

			require.Error(t, err)
			assert.Equal(t, "cannot "+tc.verb+` domain: orm: primary key column "line_no" of order_lines is zero`,
				err.Error())
			assert.Empty(t, recorder.rendered, "no statement may reach the database")
			assert.Empty(t, recorder.raw, "no query may reach the database")
		})
	}
}

// TestRefreshMatchesEveryKeyColumn. Refresh compared lowercased Go field names with the
// first key's column name, so UserID never matched user_id, and on a composite key it
// reloaded whichever row shared the first column. It now reads by every key column, by
// column name, and copies the row it read.
func TestRefreshMatchesEveryKeyColumn(t *testing.T) {
	t.Run("composite key", func(t *testing.T) {
		mockDS := NewMockDataSource()
		var sql string
		var args []any
		mockDS.session.executor.queryRawFunc = func(_ context.Context, dest interface{}, query string, queryArgs ...interface{}) dbCore.QueryResult {
			sql, args = query, queryArgs
			*dest.(**OrderLine) = &OrderLine{OrderID: 7, LineNo: 2, Sku: "sku-fresh", Qty: 9}
			return dbCore.QueryResult{Found: true, RowsAffected: 1}
		}

		line := loadedOrderLine(7, 2)
		require.NoError(t, New[*OrderLine](mockDS.session).Refresh(line))

		assert.Equal(t, "SELECT * FROM order_lines WHERE order_id = ? AND line_no = ? LIMIT 1", sql)
		assert.Equal(t, []any{int64(7), 2}, args)
		assert.Equal(t, "sku-fresh", line.Sku)
		assert.Equal(t, 9, line.Qty)
		assert.True(t, line.GetMeta().IsLoaded)
	})

	t.Run("key whose Go name is not its column", func(t *testing.T) {
		mockDS := NewMockDataSource()
		var sql string
		mockDS.session.executor.queryRawFunc = func(_ context.Context, dest interface{}, query string, _ ...interface{}) dbCore.QueryResult {
			sql = query
			*dest.(**RenamedKeyEntity) = &RenamedKeyEntity{UserID: "u-1", Name: "Grace"}
			return dbCore.QueryResult{Found: true, RowsAffected: 1}
		}

		entity := &RenamedKeyEntity{UserID: "u-1", Name: "Ada"}
		require.NoError(t, New[*RenamedKeyEntity](mockDS.session).Refresh(entity))

		assert.Equal(t, "SELECT * FROM renamed_key_entities WHERE user_id = ? LIMIT 1", sql)
		assert.Equal(t, "Grace", entity.Name)
	})

	// A row that is gone used to panic, copying from the nil entity the reload returned.
	t.Run("row that is gone", func(t *testing.T) {
		mockDS := NewMockDataSource()
		mockDS.session.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
			return dbCore.QueryResult{Found: false}
		}

		line := loadedOrderLine(7, 2)
		err := New[*OrderLine](mockDS.session).Refresh(line)

		require.True(t, errors.Is(err, ErrRowGone), "expected ErrRowGone, got %v", err)
		assert.Equal(t, "orm: the row this entity was loaded from no longer exists: "+
			"order_lines where order_id = 7 AND line_no = 2", err.Error())
		assert.Equal(t, "sku-1", line.Sku, "a failed Refresh must leave the entity as it was")
	})
}

// TestUpdateRefusesChangingAKeyColumn. The ORM does not remember the key a row was loaded
// with, so an UPDATE is addressed by the key the entity holds now. Moving line (1,1) to
// line_no 2 and marking line_no dirty used to send `UPDATE order_lines SET qty = ? WHERE
// order_id = ? AND line_no = ?` with (99, 1, 2): line (1,2) took line (1,1)'s quantity, line
// (1,1) was left as it was, and the Update reported success. A dirty set that names a key
// column is the caller saying it meant to change the key, so it is refused before anything
// is sent, on a single key as on a composite one.
func TestUpdateRefusesChangingAKeyColumn(t *testing.T) {
	t.Run("composite key", func(t *testing.T) {
		for name, update := range map[string]func(*ORM[*OrderLine], *OrderLine) error{
			"Update":         (*ORM[*OrderLine]).Update,
			"UpdateExisting": (*ORM[*OrderLine]).UpdateExisting,
			"Save":           (*ORM[*OrderLine]).Save,
		} {
			t.Run(name, func(t *testing.T) {
				mockDS := NewMockDataSource()
				recorder := recordStatements(t, mockDS)
				line := loadedOrderLine(1, 1)
				line.LineNo, line.Qty = 2, 99
				line.GetMeta().DirtyColumns = map[string]bool{"line_no": true, "qty": true}

				err := update(New[*OrderLine](mockDS.session), line)

				require.Error(t, err)
				assert.Equal(t, `orm: cannot change primary key column "line_no" of order_lines through Update; `+
					"delete the row and create it again", err.Error())
				assert.Empty(t, recorder.rendered, "no statement may reach the database")
			})
		}
	})

	t.Run("single key", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordStatements(t, mockDS)
		entity := &RenamedKeyEntity{UserID: "u-2", Name: "Ada"}
		entity.SetMeta(&EntityMeta{
			IsLoaded:      true,
			LoadedColumns: map[string]bool{},
			DirtyColumns:  map[string]bool{"user_id": true, "name": true},
		})

		err := New[*RenamedKeyEntity](mockDS.session).Update(entity)

		require.Error(t, err)
		assert.Equal(t, `orm: cannot change primary key column "user_id" of renamed_key_entities through Update; `+
			"delete the row and create it again", err.Error())
		assert.Empty(t, recorder.rendered, "no statement may reach the database")
	})
}

// PointerKeyEntity's key is a pointer, which is how a model tells "not assigned yet" from a
// real key.
type PointerKeyEntity struct {
	BaseEntity
	ID   *int   `gorm:"primaryKey;column:id"`
	Name string `gorm:"column:name"`
}

// TestAPointerKeyIsJudgedByTheValueItPointsAt. gorm's ValueOf calls only a nil pointer zero,
// so a key of &0 got past the zero-part refusal: Delete sent `DELETE FROM pointer_key_entities
// WHERE id = ?` bound to a *int that pointed at 0, and reported success. The value behind the
// pointer is what reaches the row, so it is what is judged, bound and printed.
func TestAPointerKeyIsJudgedByTheValueItPointsAt(t *testing.T) {
	zero, five := 0, 5

	t.Run("a pointer to zero is refused", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordStatements(t, mockDS)

		err := New[*PointerKeyEntity](mockDS.session).Delete(&PointerKeyEntity{ID: &zero})

		require.Error(t, err)
		assert.Equal(t, `cannot delete domain: orm: primary key column "id" of pointer_key_entities is zero`, err.Error())
		assert.Empty(t, recorder.rendered, "no statement may reach the database")
	})

	t.Run("a nil pointer is refused", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordStatements(t, mockDS)

		err := New[*PointerKeyEntity](mockDS.session).Delete(&PointerKeyEntity{})

		require.Error(t, err)
		assert.Equal(t, `cannot delete domain: orm: primary key column "id" of pointer_key_entities is zero`, err.Error())
		assert.Empty(t, recorder.rendered, "no statement may reach the database")
	})

	t.Run("a set pointer binds and prints its value", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordStatements(t, mockDS)
		var probe []any
		mockDS.session.executor.execFunc = func(_ context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
			sql, args, err := q.ToSQL()
			require.NoError(t, err)
			recorder.rendered = append(recorder.rendered, sql)
			recorder.args = append(recorder.args, args)
			return dbCore.QueryResult{RowsAffected: 0}
		}
		mockDS.session.executor.queryRawFunc = func(_ context.Context, _ interface{}, _ string, args ...interface{}) dbCore.QueryResult {
			probe = args
			return dbCore.QueryResult{Found: false}
		}
		entity := &PointerKeyEntity{ID: &five, Name: "n"}
		entity.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}})

		err := New[*PointerKeyEntity](mockDS.session).UpdateExisting(entity)

		require.True(t, errors.Is(err, ErrRowGone), "expected ErrRowGone, got %v", err)
		assert.Equal(t, "orm: the row this entity was loaded from no longer exists: pointer_key_entities where id = 5",
			err.Error(), "the message must print the key, not the pointer's address")
		assert.Equal(t, []string{"UPDATE pointer_key_entities SET name = ? WHERE id = ?"}, recorder.rendered)
		assert.Equal(t, [][]any{{"n", 5}}, recorder.args)
		assert.Equal(t, []any{5}, probe)
	})
}

// TestRefreshKeepsLoadedRelations. The scan behind Refresh reads the row, not its relations,
// so its copy of every relation field is nil. Refresh used to copy that nil over a loaded
// many-to-many relation while the meta still recorded it as loaded, and the next Save read
// that as "loaded and then cleared" and sent `DELETE FROM test_user_roles WHERE
// test_many_to_many_user_id = ?`, removing every role the user had.
func TestRefreshKeepsLoadedRelations(t *testing.T) {
	mockDS := NewMockDataSource()
	mockDS.session.executor.queryRawFunc = func(_ context.Context, dest interface{}, _ string, _ ...interface{}) dbCore.QueryResult {
		*dest.(**TestManyToManyUser) = &TestManyToManyUser{ID: 1, Name: "fresh"}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}
	// The role is already stored, so the cascade links it rather than inserting it.
	mockDS.session.executor.countRawFunc = func(context.Context, string, ...interface{}) (int64, error) {
		return 1, nil
	}
	recorder := recordStatements(t, mockDS)

	user := &TestManyToManyUser{ID: 1, Name: "stale", Roles: []*TestManyToManyRole{{ID: 10, Name: "admin"}}}
	user.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}, RelationMeta: map[string]*RelationMeta{}})
	user.GetMeta().SetRelationLoaded("Roles", &RelationMeta{Type: "many2many"})
	orm := New[*TestManyToManyUser](mockDS.session)

	require.NoError(t, orm.Refresh(user))

	assert.Equal(t, "fresh", user.Name, "the row's columns must be refreshed")
	require.Len(t, user.Roles, 1, "a loaded relation must survive the refresh")
	assert.Equal(t, 10, user.Roles[0].ID)
	assert.True(t, user.GetMeta().IsRelationLoaded("Roles"))

	require.NoError(t, orm.Save(user))

	assert.Equal(t, []string{
		"UPDATE test_many_to_many_users SET name = ? WHERE id = ?",
		"INSERT INTO test_user_roles (test_many_to_many_user_id, test_many_to_many_role_id) VALUES (?, ?) ON CONFLICT (test_many_to_many_user_id, test_many_to_many_role_id) DO NOTHING",
		"DELETE FROM test_user_roles WHERE test_many_to_many_user_id = ? AND test_many_to_many_role_id NOT IN (?)",
	}, recorder.rendered, "the Save must keep the user's role, not delete every join row")
}
