package orm

import (
	"context"
	"reflect"
	"sort"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The INSERT and UPDATE column lists follow gorm's permission tags, and the zero-key rule
// follows the schema's AutoIncrement. Before, the walks read only the column name from the
// schema, so "->", "<-" and "-:all" were written, and a key was judged auto-increment by a tag
// heuristic that took every integer primaryKey for one.

// permissionEntity has a field for each of gorm's write permissions.
type permissionEntity struct {
	BaseEntity
	ID        int64  `gorm:"primaryKey"`
	Name      string `gorm:"column:name"`
	Version   []byte `gorm:"column:version;->"`
	CreatedBy string `gorm:"column:created_by;<-:create"`
	UpdatedBy string `gorm:"column:updated_by;<-:update"`
	Frozen    string `gorm:"column:frozen;<-:false"`
	Hidden    string `gorm:"column:hidden;->:false"`
	Scratch   string `gorm:"-:all"`
}

func (permissionEntity) TableName() string { return "permission_entities" }

// taglessIDEntity's key is an int named ID with no tag at all, which gorm makes the primary key
// and, being the only integer key, auto-increment.
type taglessIDEntity struct {
	BaseEntity
	ID   int
	Name string `gorm:"column:name"`
}

func (taglessIDEntity) TableName() string { return "tagless_ids" }

// assignedKeyEntity's integer key is assigned by the caller: autoIncrement:false says so.
type assignedKeyEntity struct {
	BaseEntity
	Code int32  `gorm:"column:code;primaryKey;autoIncrement:false"`
	Name string `gorm:"column:name"`
}

func (assignedKeyEntity) TableName() string { return "assigned_keys" }

// insertColumns and updateColumns are the column lists the ORM's walks produce for entity.
func insertColumns(t *testing.T, entity EntityWithMeta) []string {
	t.Helper()
	columns, _ := extractFieldsForInsert(reflect.Indirect(reflect.ValueOf(entity)), primedMeta(t, entity))
	return columns
}

func updateColumns(t *testing.T, entity EntityWithMeta) []string {
	t.Helper()
	fields := extractFieldsForUpdate(reflect.Indirect(reflect.ValueOf(entity)), primedMeta(t, entity))
	columns := make([]string, 0, len(fields))
	for column := range fields {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	return columns
}

// primedMeta returns entity's meta with its key column set, as createEntity and updateEntity
// set it before they walk the fields.
func primedMeta(t *testing.T, entity EntityWithMeta) *EntityMeta {
	t.Helper()
	meta := entity.GetMeta()
	if s, err := keySchema(entity); err == nil && len(s.PrimaryFieldDBNames) > 0 {
		meta.PrimaryKey = s.PrimaryFieldDBNames[0]
	}
	return meta
}

// createdSQL runs Create against Postgres and returns the INSERT it sent.
func createdSQL(t *testing.T, entity EntityWithMeta) (string, []any) {
	t.Helper()
	mockDS := NewMockDataSource()
	var sql string
	var args []any
	mockDS.session.executor.queryOneFunc = func(_ context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
		var err error
		sql, args, err = q.ToSQL()
		require.NoError(t, err)
		*dest.(*map[string]interface{}) = map[string]interface{}{"id": int64(41)}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}
	require.NoError(t, New[EntityWithMeta](mockDS.session).Create(entity))
	return sql, args
}

// TestInsertSkipsReadOnlyColumns: "->" (a rowversion, a computed column), "->:false",
// "<-:update" and "<-:false" are not created. They used to be, so a model could not map a
// column the server refuses to be written.
func TestInsertSkipsReadOnlyColumns(t *testing.T) {
	entity := &permissionEntity{Name: "n", Version: []byte{1}, CreatedBy: "c", UpdatedBy: "u", Frozen: "f", Hidden: "h"}
	assert.Equal(t, []string{"name", "created_by"}, insertColumns(t, entity))

	sql, args := createdSQL(t, &permissionEntity{Name: "n", Version: []byte{1}, CreatedBy: "c", UpdatedBy: "u"})
	assert.Equal(t, "INSERT INTO permission_entities (name, created_by) VALUES (?, ?) RETURNING id", sql)
	assert.Equal(t, []any{"n", "c"}, args)
}

// TestInsertSkipsDashAllColumns: gorm leaves the column of a "-:all" field empty, and the walk
// wrote the field under that empty name, an INSERT no engine parses.
func TestInsertSkipsDashAllColumns(t *testing.T) {
	entity := &permissionEntity{Name: "n", Scratch: "never written"}
	columns := insertColumns(t, entity)
	assert.NotContains(t, columns, "")
	assert.NotContains(t, columns, "scratch")
	assert.NotContains(t, updateColumns(t, entity), "")
}

// TestUpdateSkipsCreateOnlyColumns: "<-:create" is written once and never again, and "->",
// "->:false" and "<-:false" never; "<-:update" is written by an update only.
func TestUpdateSkipsCreateOnlyColumns(t *testing.T) {
	entity := &permissionEntity{ID: 3, Name: "n", Version: []byte{1}, CreatedBy: "c", UpdatedBy: "u", Frozen: "f", Hidden: "h"}
	assert.Equal(t, []string{"name", "updated_by"}, updateColumns(t, entity))

	entity.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}})
	mockDS := NewMockDataSource()
	recorder := recordStatements(t, mockDS)
	require.NoError(t, New[*permissionEntity](mockDS.session).Update(entity))
	require.Len(t, recorder.rendered, 1)
	assert.Equal(t, "UPDATE permission_entities SET name = ?, updated_by = ? WHERE id = ?", recorder.rendered[0])
}

// TestTaglessIntIDIsLeftToTheDatabase: gorm makes a tagless ID int the auto-increment key, and
// so does the ORM now. The tag heuristic found no tag and wrote the zero, so the first row got
// the key 0 and the second failed on it.
func TestTaglessIntIDIsLeftToTheDatabase(t *testing.T) {
	assert.Equal(t, []string{"name"}, insertColumns(t, &taglessIDEntity{Name: "n"}))
	assert.Equal(t, []string{"id", "name"}, insertColumns(t, &taglessIDEntity{ID: 5, Name: "n"}),
		"a key the caller set is written")

	entity := &taglessIDEntity{Name: "n"}
	sql, _ := createdSQL(t, entity)
	assert.Equal(t, "INSERT INTO tagless_ids (name) VALUES (?) RETURNING id", sql)
	assert.Equal(t, 41, entity.ID, "the key the server generated is read back")
}

// TestAutoIncrementFalseIsInserted: autoIncrement:false says the caller assigns the key, so it
// is written even when it is zero. The heuristic took the integer primaryKey for an
// auto-increment one whatever the tag said and left it out, so a zero code was never stored.
func TestAutoIncrementFalseIsInserted(t *testing.T) {
	assert.Equal(t, []string{"code", "name"}, insertColumns(t, &assignedKeyEntity{Name: "n"}))
	assert.Equal(t, []string{"order_id", "line_no", "sku", "qty"}, insertColumns(t, &OrderLine{Sku: "s"}),
		"every part of a composite key the caller assigns")
}

// TestInsertWithoutASchemaSkipsTheReadOnlyTagSegment: without a schema only the tag is there to
// read, and "->" is the one permission read from it.
func TestInsertWithoutASchemaSkipsTheReadOnlyTagSegment(t *testing.T) {
	var columns []string
	var values []any
	entity := permissionEntity{Name: "n", Version: []byte{1}, Hidden: "h", CreatedBy: "c"}
	extractFieldsFromStructSkipRelations(reflect.ValueOf(entity), &EntityMeta{PrimaryKey: "id"}, &columns, &values,
		map[string]bool{}, false, nil, false, map[string]struct{}{})
	assert.NotContains(t, columns, "version")
	assert.NotContains(t, columns, "hidden")
	assert.Contains(t, columns, "created_by", "the other permissions are gorm's to parse")
	assert.Contains(t, columns, "name")

	update := map[string]interface{}{}
	extractUpdateFieldsFromStruct(reflect.ValueOf(entity), &EntityMeta{PrimaryKey: "id"}, map[string]bool{"id": true},
		update, map[string]bool{}, map[string]struct{}{}, nil, false)
	assert.NotContains(t, update, "version")
	assert.Contains(t, update, "name")
}

// TestExistingFixturesKeepTheirColumnLists pins, for every model this package's tests use, the
// INSERT and UPDATE column lists the walks produced before they read gorm's permissions. None of
// these models has a permission tag, and gorm makes an untagged field creatable and updatable,
// so the lists of ordinary fields are unchanged. The only differences are zero keys: a key
// tagged autoIncrement:false is now written (OrderLine, LinkRow, TenantGroup, TenantMember),
// and a nil pointer key that gorm calls auto-increment is left to the server
// (PointerKeyEntity), where it used to be written as NULL.
func TestExistingFixturesKeepTheirColumnLists(t *testing.T) {
	seven := 7
	for _, tc := range []struct {
		zero, keyed          EntityWithMeta
		zeroInsert, keyedIns []string
		update               []string
	}{
		{&CascadeChild{}, &CascadeChild{ID: 7}, []string{"cascade_parent_id", "label"}, []string{"id", "cascade_parent_id", "label"}, []string{"cascade_parent_id", "label"}},
		{&CascadeGroup{}, &CascadeGroup{ID: 7}, []string{"title"}, []string{"id", "title"}, []string{"title"}},
		{&CascadeItem{}, &CascadeItem{ID: 7}, []string{"cascade_owner_id", "label"}, []string{"id", "cascade_owner_id", "label"}, []string{"cascade_owner_id", "label"}},
		{&CascadeOwner{}, &CascadeOwner{ID: 7}, []string{"name", "group_id"}, []string{"id", "name", "group_id"}, []string{"group_id", "name"}},
		{&CascadeParent{}, &CascadeParent{ID: 7}, []string{"name"}, []string{"id", "name"}, []string{"name"}},
		{&CascadeProfile{}, &CascadeProfile{ID: 7}, []string{"cascade_owner_id", "bio"}, []string{"id", "cascade_owner_id", "bio"}, []string{"bio", "cascade_owner_id"}},
		{&CascadeTag{}, &CascadeTag{ID: 7}, []string{"name"}, []string{"id", "name"}, []string{"name"}},
		{&CascadeToy{}, &CascadeToy{ID: 7}, []string{"cascade_child_id", "name"}, []string{"id", "cascade_child_id", "name"}, []string{"cascade_child_id", "name"}},
		{&GroupOwner{}, &GroupOwner{ID: 7}, []string{}, []string{"id"}, []string{}},
		{&Language{}, &Language{ID: 7}, []string{"code"}, []string{"id", "code"}, []string{"code"}},
		{&LanguageOwner{}, &LanguageOwner{ID: 7}, []string{}, []string{"id"}, []string{}},
		{&LinkRow{}, &LinkRow{LeftID: 7, RightID: 7}, []string{"left_id", "right_id"}, []string{"left_id", "right_id"}, []string{}},
		{&MemberGroup{}, &MemberGroup{ID: 7}, []string{"name"}, []string{"id", "name"}, []string{"name"}},
		{&OrderLine{}, &OrderLine{OrderID: 7, LineNo: 7}, []string{"order_id", "line_no", "sku", "qty"}, []string{"order_id", "line_no", "sku", "qty"}, []string{"qty", "sku"}},
		{&PointerKeyEntity{}, &PointerKeyEntity{ID: &seven}, []string{"name"}, []string{"id", "name"}, []string{"name"}},
		{&RenamedKeyEntity{}, &RenamedKeyEntity{UserID: "u"}, []string{"user_id", "name"}, []string{"user_id", "name"}, []string{"name"}},
		{&TenantGroup{}, &TenantGroup{TenantID: 7, Code: "c"}, []string{"tenant_id", "code"}, []string{"tenant_id", "code"}, []string{}},
		{&TenantMember{}, &TenantMember{TenantID: 7, ID: 7}, []string{"tenant_id", "id", "name"}, []string{"tenant_id", "id", "name"}, []string{"name"}},
		{&TestEntity{}, &TestEntity{ID: 7}, []string{"name", "email", "age", "created_at"}, []string{"id", "name", "email", "age", "created_at"}, []string{"age", "created_at", "email", "name"}},
		{&TestEntityWithCustomTableName{}, &TestEntityWithCustomTableName{ID: 7}, []string{"name"}, []string{"id", "name"}, []string{"name"}},
		{&TestEntityWithValueReceiverTableName{}, &TestEntityWithValueReceiverTableName{ID: 7}, []string{"name"}, []string{"id", "name"}, []string{"name"}},
		{&TestManyToManyRole{}, &TestManyToManyRole{ID: 7}, []string{"name"}, []string{"id", "name"}, []string{"name"}},
		{&TestManyToManyUser{}, &TestManyToManyUser{ID: 7}, []string{"name"}, []string{"id", "name"}, []string{"name"}},
		{&TestRelatedEntity{}, &TestRelatedEntity{ID: 7}, []string{"test_id", "category"}, []string{"id", "test_id", "category"}, []string{"category", "test_id"}},
		// gorm cannot parse this one (NestedStruct is neither a relation nor a Valuer), so the
		// walks fall back to the tags, as they always did.
		{&TestEntityWithEmbedded{}, &TestEntityWithEmbedded{ID: 7},
			[]string{"ID", "name", "description", "status", "NestedStruct", "status_override"},
			[]string{"ID", "name", "description", "status", "NestedStruct", "status_override"},
			[]string{"ID", "NestedStruct", "description", "name", "status", "status_override"}},
	} {
		name := reflect.TypeOf(tc.zero).Elem().Name()
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.zeroInsert, orEmpty(insertColumns(t, tc.zero)), "INSERT with a zero key")
			assert.Equal(t, tc.keyedIns, orEmpty(insertColumns(t, tc.keyed)), "INSERT with a key set")
			assert.Equal(t, tc.update, updateColumns(t, tc.keyed), "UPDATE")
		})
	}
}

func orEmpty(columns []string) []string {
	if columns == nil {
		return []string{}
	}
	return columns
}
