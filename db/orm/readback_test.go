package orm

import (
	"context"
	"reflect"
	"strings"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	v2 "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Create reads the generated columns back — the key, columns with defaults, and fields tagged
// grgorm:"readback" — and Update re-reads the read-back fields. These tests pin how: through
// RETURNING where the dialect and the table allow it, else through ExecInsert and a SELECT keyed
// on every key column, and never by leaving the entity holding a key that is not the row's.

// insertingExecutor is a MockExecutor that also reports generated keys through ExecInsert, as
// the MySQL and SQL Server executors do.
type insertingExecutor struct {
	*MockExecutor
	insert func(q dbCore.IQueryBuilder) dbCore.InsertResult
}

func (e *insertingExecutor) ExecInsert(_ context.Context, q dbCore.IQueryBuilder) dbCore.InsertResult {
	return e.insert(q)
}

// insertingSession is a MockSession whose executor is an insertingExecutor.
type insertingSession struct {
	*MockSession
	executor *insertingExecutor
}

func (s *insertingSession) Executor() dbCore.IQueryExecutor { return s.executor }

// newInsertingSession returns a session speaking builder's dialect whose ExecInsert answers
// with result and records the INSERT it was handed in *inserted.
func newInsertingSession(t *testing.T, builder func() dbCore.IQueryBuilder, result dbCore.InsertResult,
	inserted *string) *insertingSession {
	t.Helper()
	mockDS := NewMockDataSource()
	mockDS.session.queryFn = builder
	return &insertingSession{
		MockSession: mockDS.session,
		executor: &insertingExecutor{
			MockExecutor: mockDS.session.executor,
			insert: func(q dbCore.IQueryBuilder) dbCore.InsertResult {
				sql, _, err := q.ToSQL()
				require.NoError(t, err)
				*inserted = sql
				return result
			},
		},
	}
}

// triggerBlockedDialect is Postgres with SQL Server's rule that a trigger on the table blocks
// RETURNING (see core.TriggerSensitiveReturning).
type triggerBlockedDialect struct{ *v2.PostgresDialect }

func (triggerBlockedDialect) ReturningBlockedByTriggers() bool { return true }

func triggerBlockedBuilder() dbCore.IQueryBuilder {
	return v2.NewBuilderWithDialect(triggerBlockedDialect{&v2.PostgresDialect{}})
}

// versionedEntity has a column the server sets on every write. Note's tag merely contains the
// word "readback", which does not make it one.
type versionedEntity struct {
	BaseEntity
	ID         int64  `gorm:"primaryKey"`
	Name       string `gorm:"column:name"`
	RowVersion []byte `gorm:"column:row_version;->" grgorm:"readback"`
	Note       string `gorm:"column:note" grgorm:"noreadback"`
}

func (versionedEntity) TableName() string { return "versioned_entities" }

// auditedEntity is versionedEntity on a table with triggers.
type auditedEntity struct {
	BaseEntity
	ID         int64  `gorm:"primaryKey"`
	Name       string `gorm:"column:name"`
	RowVersion []byte `gorm:"column:row_version;->" grgorm:"readback"`
}

func (auditedEntity) TableName() string      { return "audited_entities" }
func (auditedEntity) TableHasTriggers() bool { return true }

// foldedEntity spells its columns in a case an engine may report differently.
type foldedEntity struct {
	BaseEntity
	Id    int64  `gorm:"column:Id;primaryKey;autoIncrement"`
	Stamp string `gorm:"column:Stamp;default:now()"`
}

func (foldedEntity) TableName() string { return "folded_entities" }

// guidKeyedEntity's key is assigned by the caller, and its created column by the server.
type guidKeyedEntity struct {
	BaseEntity
	ID      string `gorm:"column:id;primaryKey"`
	Name    string `gorm:"column:name"`
	Created string `gorm:"column:created;default:CURRENT_TIMESTAMP"`
}

func (guidKeyedEntity) TableName() string { return "guid_keyed_entities" }

// unparseableEntity has a field gorm can neither map to a column nor read as a relation.
type unparseableEntity struct {
	BaseEntity
	ID     int64 `gorm:"primaryKey"`
	Nested struct{ A int }
}

// TestCreateErrorsWhenSchemaParseFails: the schema names the key and the generated columns. A
// model gorm cannot parse used to be inserted all the same, with the Go field names standing in
// for the columns the schema could not name, and the server's refusal was the only error. It is
// now refused before the hooks run and before anything is sent.
func TestCreateErrorsWhenSchemaParseFails(t *testing.T) {
	mockDS := NewMockDataSource()
	sent := 0
	mockDS.session.executor.queryOneFunc = func(context.Context, dbCore.IQueryBuilder, interface{}) dbCore.QueryResult {
		sent++
		return dbCore.QueryResult{}
	}
	mockDS.session.executor.execFunc = func(context.Context, dbCore.IQueryBuilder) dbCore.QueryResult {
		sent++
		return dbCore.QueryResult{}
	}

	var err error
	require.NotPanics(t, func() { err = New[*unparseableEntity](mockDS.session).Create(&unparseableEntity{}) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot create domain")
	assert.Contains(t, err.Error(), "unparseableEntity")
	assert.Zero(t, sent, "nothing is sent for a model whose schema cannot be read")
}

// TestCreateReadsBackGeneratedColumnsCaseInsensitively: Postgres folds an unquoted RETURNING
// Id to id, and a generated column looked up only by its exact name was silently left unset. A
// name reported exactly still wins over one that differs only in case.
func TestCreateReadsBackGeneratedColumnsCaseInsensitively(t *testing.T) {
	for name, tc := range map[string]struct {
		returned  map[string]interface{}
		wantID    int64
		wantStamp string
	}{
		"folded":       {map[string]interface{}{"id": int64(5), "stamp": "now"}, 5, "now"},
		"exact first":  {map[string]interface{}{"Id": int64(5), "ID": int64(6), "id": int64(7), "Stamp": "a", "stamp": "b"}, 5, "a"},
		"sorted folds": {map[string]interface{}{"ID": int64(6), "id": int64(7)}, 6, ""},
	} {
		t.Run(name, func(t *testing.T) {
			mockDS := NewMockDataSource()
			var sql string
			mockDS.session.executor.queryOneFunc = func(_ context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
				sql, _, _ = q.ToSQL()
				*dest.(*map[string]interface{}) = tc.returned
				return dbCore.QueryResult{Found: true, RowsAffected: 1}
			}

			entity := &foldedEntity{}
			require.NoError(t, New[*foldedEntity](mockDS.session).Create(entity))
			assert.Equal(t, "INSERT INTO folded_entities (Stamp) VALUES (?) RETURNING Id, Stamp", sql)
			assert.Equal(t, tc.wantID, entity.Id)
			assert.Equal(t, tc.wantStamp, entity.Stamp)
		})
	}
}

// TestCreateFailsWhenGeneratedKeyCannotBeReadBack: the INSERT left the key to the server, which
// reported none, so nothing can address the row. That used to be a warning and a nil error,
// which left the entity with a zero key for every relation saved against it and let a retry
// write the row twice. It is an error that says the row exists.
func TestCreateFailsWhenGeneratedKeyCannotBeReadBack(t *testing.T) {
	var inserted string
	session := newInsertingSession(t, mysqlBuilder, dbCore.InsertResult{QueryResult: dbCore.QueryResult{RowsAffected: 1}}, &inserted)
	selects := 0
	session.MockSession.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
		selects++
		return dbCore.QueryResult{}
	}

	entity := &TestEntity{Name: "n"}
	err := New[*TestEntity](session).Create(entity)
	require.Error(t, err)
	assert.Equal(t, "orm: the INSERT into test_entities succeeded but the generated key could not be read back "+
		`([id]); the row exists: the INSERT left primary key column "id" to the server, which did not report the `+
		"value it generated", err.Error())
	assert.NotEmpty(t, inserted, "the INSERT was sent")
	assert.Zero(t, selects, "there is no key to select the row by")
	assert.False(t, entity.GetMeta().IsLoaded, "the entity is not marked as stored with a key it does not have")
}

// TestCreateUsesClientAssignedPKForReadBack: a key the caller assigned — a GUID, say — is
// never among the values the server reports, and the read-back used to look only there, so a
// generated column of such a row could not be read back at all. The entity's own key keys it.
func TestCreateUsesClientAssignedPKForReadBack(t *testing.T) {
	var inserted, selected string
	var selectedArgs []any
	session := newInsertingSession(t, mysqlBuilder, dbCore.InsertResult{QueryResult: dbCore.QueryResult{RowsAffected: 1}}, &inserted)
	session.MockSession.executor.queryRawFunc = func(_ context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
		selected, selectedArgs = sql, args
		*dest.(*map[string]interface{}) = map[string]interface{}{"created": "2026-09-27"}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}

	entity := &guidKeyedEntity{ID: "7c9e6679-7425-40de-944b-e07fc1f90ae7", Name: "n"}
	require.NoError(t, New[*guidKeyedEntity](session).Create(entity))
	assert.Equal(t, "INSERT INTO guid_keyed_entities (id, name, created) VALUES (?, ?, ?)", inserted)
	assert.Equal(t, "SELECT created FROM guid_keyed_entities WHERE id = ? LIMIT 1", selected,
		"the key column itself is known and not read again")
	assert.Equal(t, []any{"7c9e6679-7425-40de-944b-e07fc1f90ae7"}, selectedArgs)
	assert.Equal(t, "2026-09-27", entity.Created)
	assert.Equal(t, "7c9e6679-7425-40de-944b-e07fc1f90ae7", entity.ID)
}

// TestReadBackColumnsReturnedAfterCreate: a grgorm:"readback" field is read with the key, in
// the INSERT's RETURNING, and a field whose tag merely contains the word is not.
func TestReadBackColumnsReturnedAfterCreate(t *testing.T) {
	mockDS := NewMockDataSource()
	var sql string
	mockDS.session.executor.queryOneFunc = func(_ context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
		sql, _, _ = q.ToSQL()
		*dest.(*map[string]interface{}) = map[string]interface{}{"id": int64(4), "row_version": []byte{0, 1}}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}

	entity := &versionedEntity{Name: "n", Note: "x"}
	require.NoError(t, New[*versionedEntity](mockDS.session).Create(entity))
	assert.Equal(t, "INSERT INTO versioned_entities (name, note) VALUES (?, ?) RETURNING id, row_version", sql)
	assert.Equal(t, int64(4), entity.ID)
	assert.Equal(t, []byte{0, 1}, entity.RowVersion)
}

// TestReadBackColumnsReturnedByUpdate: after an UPDATE the entity's copy of a column the server
// changes on every write is the value from before it, and the next update guarded on it would
// conflict with the entity's own write. Where the dialect's RETURNING can be used, it is read in
// the UPDATE itself: a SELECT afterwards could read a later write's value instead, which a guard
// on it would then match over. A guarded UPDATE that matched nothing returns no row, and the
// field is left as it was.
func TestReadBackColumnsReturnedByUpdate(t *testing.T) {
	mockDS := NewMockDataSource()
	recorder := recordStatements(t, mockDS)
	var updated string
	var updatedArgs []any
	mockDS.session.executor.queryOneFunc = func(_ context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
		updated, updatedArgs, _ = q.ToSQL()
		*dest.(*map[string]interface{}) = map[string]interface{}{"row_version": []byte{0, 2}}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}
	selects := 0
	mockDS.session.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
		selects++
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}

	entity := &versionedEntity{ID: 3, Name: "n", RowVersion: []byte{0, 1}}
	entity.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}})
	require.NoError(t, New[*versionedEntity](mockDS.session).Update(entity))

	assert.Empty(t, recorder.rendered, "the UPDATE runs as a query, so its RETURNING row is read")
	assert.Equal(t, "UPDATE versioned_entities SET name = ?, note = ? WHERE id = ? RETURNING row_version", updated,
		"the read-only column is not written, and is returned")
	assert.Equal(t, []any{"n", "", int64(3)}, updatedArgs)
	assert.Zero(t, selects, "no SELECT follows the UPDATE")
	assert.Equal(t, []byte{0, 2}, entity.RowVersion)

	// A guarded UPDATE that lost returns no row; the existence probe tells the conflict from a
	// row that is gone, and the entity keeps the version it held.
	mockDS.session.executor.queryOneFunc = func(_ context.Context, q dbCore.IQueryBuilder, _ interface{}) dbCore.QueryResult {
		updated, _, _ = q.ToSQL()
		return dbCore.QueryResult{}
	}
	entity.GetMeta().UpdateGuard = []dbCore.Condition{&dbCore.BinaryCondition{Left: "row_version", Operator: "=", Right: []byte{0, 1}}}
	err := New[*versionedEntity](mockDS.session).Update(entity)
	require.ErrorIs(t, err, ErrRowConflict)
	assert.Equal(t, "UPDATE versioned_entities SET name = ?, note = ? WHERE id = ? AND row_version = ? RETURNING row_version", updated)
	assert.Equal(t, 1, selects, "one existence probe")
	assert.Equal(t, []byte{0, 2}, entity.RowVersion)
}

// TestReadBackColumnsReselectedAfterUpdate: where RETURNING cannot be used — MySQL has none, and
// a model that says its table has triggers opts out of it where they block it — the read-back
// fields are re-read by the row's key after the UPDATE.
func TestReadBackColumnsReselectedAfterUpdate(t *testing.T) {
	for name, tc := range map[string]struct {
		builder func() dbCore.IQueryBuilder
		entity  func() EntityWithMeta
		update  string
		reread  string
	}{
		"mysql": {mysqlBuilder, func() EntityWithMeta { return &versionedEntity{ID: 3, Name: "n", RowVersion: []byte{0, 1}} },
			"UPDATE versioned_entities SET name = ?, note = ? WHERE id = ?",
			"SELECT row_version FROM versioned_entities WHERE id = ? LIMIT 1"},
		"a trigger table": {triggerBlockedBuilder, func() EntityWithMeta { return &auditedEntity{ID: 3, Name: "n", RowVersion: []byte{0, 1}} },
			"UPDATE audited_entities SET name = ? WHERE id = ?",
			"SELECT row_version FROM audited_entities WHERE id = ? LIMIT 1"},
	} {
		t.Run(name, func(t *testing.T) {
			mockDS := NewMockDataSource()
			mockDS.session.queryFn = tc.builder
			recorder := recordStatements(t, mockDS)
			var selected string
			var selectedArgs []any
			mockDS.session.executor.queryRawFunc = func(_ context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
				selected, selectedArgs = sql, args
				*dest.(*map[string]interface{}) = map[string]interface{}{"row_version": []byte{0, 2}}
				return dbCore.QueryResult{Found: true, RowsAffected: 1}
			}

			entity := tc.entity()
			entity.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}})
			require.NoError(t, New[EntityWithMeta](mockDS.session).Update(entity))

			require.Len(t, recorder.rendered, 1)
			assert.Equal(t, tc.update, recorder.rendered[0], "the read-only column is not written")
			assert.Equal(t, tc.reread, selected)
			assert.Equal(t, []any{int64(3)}, selectedArgs)
			assert.Equal(t, []byte{0, 2}, reflect.Indirect(reflect.ValueOf(entity)).FieldByName("RowVersion").Bytes())

			// An unguarded UPDATE of a row that is gone reports nothing, and the re-read finds
			// nothing to read; the field is left as it was.
			mockDS.session.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
				return dbCore.QueryResult{Found: false}
			}
			require.NoError(t, New[EntityWithMeta](mockDS.session).Update(entity))
			assert.Equal(t, []byte{0, 2}, reflect.Indirect(reflect.ValueOf(entity)).FieldByName("RowVersion").Bytes())
		})
	}
}

// TestCreateRoutesTriggerTablesThroughExecInsert: where the dialect says a trigger blocks its
// RETURNING (SQL Server's Msg 334) and the model says its table has one, Create sends the
// INSERT without RETURNING through ExecInsert, takes the key it reports, and reads the other
// generated columns with a SELECT keyed on it.
func TestCreateRoutesTriggerTablesThroughExecInsert(t *testing.T) {
	var inserted, selected string
	var selectedArgs []any
	session := newInsertingSession(t, triggerBlockedBuilder, dbCore.InsertResult{
		QueryResult: dbCore.QueryResult{RowsAffected: 1}, LastInsertID: 12, HasLastInsertID: true,
	}, &inserted)
	returning := 0
	session.MockSession.executor.queryOneFunc = func(context.Context, dbCore.IQueryBuilder, interface{}) dbCore.QueryResult {
		returning++
		return dbCore.QueryResult{}
	}
	session.MockSession.executor.queryRawFunc = func(_ context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
		selected, selectedArgs = sql, args
		*dest.(*map[string]interface{}) = map[string]interface{}{"row_version": []byte{0, 9}}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}

	entity := &auditedEntity{Name: "n"}
	require.NoError(t, New[*auditedEntity](session).Create(entity))
	assert.Zero(t, returning, "no INSERT with RETURNING is sent")
	assert.Equal(t, "INSERT INTO audited_entities (name) VALUES (?)", inserted)
	assert.Equal(t, "SELECT row_version FROM audited_entities WHERE id = ? LIMIT 1", selected)
	assert.Equal(t, []any{int64(12)}, selectedArgs)
	assert.Equal(t, int64(12), entity.ID)
	assert.Equal(t, []byte{0, 9}, entity.RowVersion)
}

// TestTriggerOptOutIgnoredWithoutDialectRestriction: TableHasTriggers changes nothing where
// triggers do not block RETURNING. An EF model reused on Postgres keeps RETURNING, and a model
// without the opt-out keeps it on a dialect where they would.
func TestTriggerOptOutIgnoredWithoutDialectRestriction(t *testing.T) {
	for name, tc := range map[string]struct {
		builder func() dbCore.IQueryBuilder
		entity  EntityWithMeta
		want    string
	}{
		"opted out on Postgres": {pgBuilder, &auditedEntity{Name: "n"},
			"INSERT INTO audited_entities (name) VALUES (?) RETURNING id, row_version"},
		"no opt-out where triggers block": {triggerBlockedBuilder, &versionedEntity{Name: "n"},
			"INSERT INTO versioned_entities (name, note) VALUES (?, ?) RETURNING id, row_version"},
	} {
		t.Run(name, func(t *testing.T) {
			var inserted, returned string
			session := newInsertingSession(t, tc.builder, dbCore.InsertResult{}, &inserted)
			session.MockSession.executor.queryOneFunc = func(_ context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
				returned, _, _ = q.ToSQL()
				*dest.(*map[string]interface{}) = map[string]interface{}{"id": int64(8)}
				return dbCore.QueryResult{Found: true, RowsAffected: 1}
			}

			require.NoError(t, New[EntityWithMeta](session).Create(tc.entity))
			assert.Equal(t, tc.want, returned)
			assert.Empty(t, inserted, "ExecInsert is not used")
		})
	}
}

// TestHasTriggersAndReadbackFields pins the two hints' readers: a model says it has triggers
// only by returning true, and only a whole "readback" in the grgorm tag marks a field.
func TestHasTriggersAndReadbackFields(t *testing.T) {
	assert.True(t, hasTriggers(&auditedEntity{}))
	assert.True(t, hasTriggers(auditedEntity{}), "a value receiver answers for the value too")
	assert.False(t, hasTriggers(&versionedEntity{}))

	s, err := keySchema(&versionedEntity{})
	require.NoError(t, err)
	fields := readbackFields(s)
	require.Len(t, fields, 1)
	assert.Equal(t, "row_version", fields[0].DBName)

	assert.True(t, hasORMTagValue("preload, readback", "readback"))
	assert.False(t, hasORMTagValue("noreadback", "readback"))
	assert.False(t, hasORMTagValue("", "readback"))
}

// assignedKeyOrder's key is assigned by the caller. gorm makes it auto-increment all the same,
// as it makes every single untagged integer key, and its table's IDENTITY is another column
// the model does not map.
type assignedKeyOrder struct {
	BaseEntity
	OrderNo    int32  `gorm:"column:OrderNo;primaryKey"`
	Name       string `gorm:"column:Name"`
	RowVersion []byte `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (assignedKeyOrder) TableName() string      { return "dbo.2024Orders" }
func (assignedKeyOrder) TableHasTriggers() bool { return true }

// TestCreateKeepsAKeyItWroteOverTheReportedIdentity: the key reported by ExecInsert is the
// table's IDENTITY value, and on this table that is another column's. It used to replace the
// key the INSERT had just written, so the read-back selected another row, the entity came back
// holding that row's key and rowversion, and its next guarded Update overwrote that row. A key
// the INSERT wrote is the row's key; the reported value keys only a key the INSERT left out.
func TestCreateKeepsAKeyItWroteOverTheReportedIdentity(t *testing.T) {
	var inserted, selected string
	var selectedArgs []any
	session := newInsertingSession(t, triggerBlockedBuilder, dbCore.InsertResult{
		QueryResult: dbCore.QueryResult{RowsAffected: 1}, LastInsertID: 2, HasLastInsertID: true,
	}, &inserted)
	session.MockSession.executor.queryRawFunc = func(_ context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
		selected, selectedArgs = sql, args
		*dest.(*map[string]interface{}) = map[string]interface{}{"RowVersion": []byte{0, 50}}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}

	order := &assignedKeyOrder{OrderNo: 50, Name: "mine"}
	require.NoError(t, New[*assignedKeyOrder](session).Create(order))
	assert.Equal(t, "INSERT INTO dbo.2024Orders (OrderNo, Name) VALUES (?, ?)", inserted)
	assert.Equal(t, "SELECT RowVersion FROM dbo.2024Orders WHERE OrderNo = ? LIMIT 1", selected)
	assert.Equal(t, []any{int32(50)}, selectedArgs, "the read-back is keyed on the key the INSERT wrote")
	assert.Equal(t, int32(50), order.OrderNo, "and the entity keeps it")
	assert.Equal(t, []byte{0, 50}, order.RowVersion)
}

// zeroCodeStatus's key is assigned by the caller, and 0 is one of its values.
type zeroCodeStatus struct {
	BaseEntity
	Code  int32  `gorm:"column:code;primaryKey;autoIncrement:false"`
	Label string `gorm:"column:label;default:'new'"`
}

func (zeroCodeStatus) TableName() string { return "statuses" }

// zeroPartLine has a composite key whose second part is often 0.
type zeroPartLine struct {
	BaseEntity
	OrderID int64  `gorm:"column:order_id;primaryKey"`
	LineNo  int64  `gorm:"column:line_no;primaryKey"`
	Status  string `gorm:"column:status;default:'new'"`
}

func (zeroPartLine) TableName() string { return "lines" }

// tenantScoped has an auto-increment key and a second key part the caller assigns.
type tenantScoped struct {
	BaseEntity
	ID       uint   `gorm:"column:id;primaryKey"`
	TenantID uint   `gorm:"column:tenant_id;primaryKey"`
	Status   string `gorm:"column:status;default:'created'"`
}

func (tenantScoped) TableName() string { return "tenant_rows" }

// TestCreateKeysTheReadBackOnAZeroKeyItWrote: a key column the INSERT wrote holds what the
// entity holds, zero included, so the read-back is keyed on it. A zero key used to count as
// none, so on MySQL, and on a SQL Server table with TableHasTriggers, such a Create wrote its row
// and then returned an error, where the same Create through RETURNING succeeded; and a reported
// auto-increment key beside a zero part never reached the entity.
func TestCreateKeysTheReadBackOnAZeroKeyItWrote(t *testing.T) {
	for name, tc := range map[string]struct {
		entity   EntityWithMeta
		result   dbCore.InsertResult
		insert   string
		selected string
		args     []any
		check    func(t *testing.T, entity EntityWithMeta)
	}{
		"a zero assigned key": {
			entity:   &zeroCodeStatus{},
			insert:   "INSERT INTO statuses (code, label) VALUES (?, ?)",
			selected: "SELECT label FROM statuses WHERE code = ? LIMIT 1",
			args:     []any{int32(0)},
			check: func(t *testing.T, entity EntityWithMeta) {
				assert.Equal(t, "read back", entity.(*zeroCodeStatus).Label)
			},
		},
		"a zero part of a composite key": {
			entity:   &zeroPartLine{OrderID: 7},
			insert:   "INSERT INTO lines (order_id, line_no, status) VALUES (?, ?, ?)",
			selected: "SELECT status FROM lines WHERE order_id = ? AND line_no = ? LIMIT 1",
			args:     []any{int64(7), int64(0)},
			check: func(t *testing.T, entity EntityWithMeta) {
				assert.Equal(t, "read back", entity.(*zeroPartLine).Status)
			},
		},
		"a generated key beside a zero part": {
			entity:   &tenantScoped{},
			result:   dbCore.InsertResult{LastInsertID: 42, HasLastInsertID: true},
			insert:   "INSERT INTO tenant_rows (tenant_id, status) VALUES (?, ?)",
			selected: "SELECT status FROM tenant_rows WHERE id = ? AND tenant_id = ? LIMIT 1",
			args:     []any{int64(42), uint(0)},
			check: func(t *testing.T, entity EntityWithMeta) {
				assert.Equal(t, uint(42), entity.(*tenantScoped).ID, "the reported key reaches the entity")
				assert.Equal(t, "read back", entity.(*tenantScoped).Status)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var inserted, selected string
			var selectedArgs []any
			tc.result.RowsAffected = 1
			session := newInsertingSession(t, mysqlBuilder, tc.result, &inserted)
			session.MockSession.executor.queryRawFunc = func(_ context.Context, dest interface{}, sql string, args ...interface{}) dbCore.QueryResult {
				selected, selectedArgs = sql, args
				row := map[string]interface{}{}
				for _, column := range []string{"label", "status"} {
					if strings.Contains(sql, column) {
						row[column] = "read back"
					}
				}
				*dest.(*map[string]interface{}) = row
				return dbCore.QueryResult{Found: true, RowsAffected: 1}
			}

			require.NoError(t, New[EntityWithMeta](session).Create(tc.entity))
			assert.Equal(t, tc.insert, inserted)
			assert.Equal(t, tc.selected, selected)
			assert.Equal(t, tc.args, selectedArgs)
			assert.True(t, tc.entity.GetMeta().IsLoaded)
			tc.check(t, tc.entity)
		})
	}
}

// keylessLog maps a table without a primary key, with a defaulted column.
type keylessLog struct {
	BaseEntity
	Message string `gorm:"column:message"`
	Level   string `gorm:"column:level;default:'info'"`
}

func (keylessLog) TableName() string { return "logs" }

// TestCreateOfAKeylessModelStillSucceeds: a model without a primary key has nothing to select
// its row by, so its defaulted columns cannot be read back after an INSERT without RETURNING.
// No key can be lost there either, so that stays a warning and the INSERT's success, as it
// always was, rather than an error for a row that was written.
func TestCreateOfAKeylessModelStillSucceeds(t *testing.T) {
	var inserted string
	session := newInsertingSession(t, mysqlBuilder, dbCore.InsertResult{QueryResult: dbCore.QueryResult{RowsAffected: 1}}, &inserted)
	selects := 0
	session.MockSession.executor.queryRawFunc = func(context.Context, interface{}, string, ...interface{}) dbCore.QueryResult {
		selects++
		return dbCore.QueryResult{}
	}

	entry := &keylessLog{Message: "started"}
	require.NoError(t, New[*keylessLog](session).Create(entry))
	assert.Equal(t, "INSERT INTO logs (message, level) VALUES (?, ?)", inserted)
	assert.Zero(t, selects)
	assert.True(t, entry.GetMeta().IsLoaded)
}
