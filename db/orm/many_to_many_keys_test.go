package orm

import (
	"context"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SaveRelations writes a many-to-many relation's join rows by one owner column and one
// related column: it upserts a row per related entity and deletes the owner's other rows.
// Each side used to be picked from the relation's references by overwriting a variable in a
// loop, so a composite key on either side was addressed by one of its parts, and the related
// side stored the related entity's first key column whatever the join column referenced.

// TenantMember is keyed by (tenant_id, id): the same id is a different member in each tenant.
type TenantMember struct {
	BaseEntity
	TenantID int64          `gorm:"primaryKey;autoIncrement:false;column:tenant_id"`
	ID       int64          `gorm:"primaryKey;autoIncrement:false;column:id"`
	Name     string         `gorm:"column:name"`
	Groups   []*MemberGroup `gorm:"many2many:tenant_member_groups;"`
}

type MemberGroup struct {
	BaseEntity
	ID   int64  `gorm:"primaryKey"`
	Name string `gorm:"column:name"`
}

// GroupOwner links to TenantGroup, which is keyed by (tenant_id, code).
type GroupOwner struct {
	BaseEntity
	ID     int64          `gorm:"primaryKey"`
	Groups []*TenantGroup `gorm:"many2many:group_owner_groups;"`
}

type TenantGroup struct {
	BaseEntity
	TenantID int64  `gorm:"primaryKey;autoIncrement:false;column:tenant_id"`
	Code     string `gorm:"primaryKey;column:code"`
}

// LanguageOwner's relation references Language's code, not its key.
type LanguageOwner struct {
	BaseEntity
	ID        int64       `gorm:"primaryKey"`
	Languages []*Language `gorm:"many2many:owner_languages;references:Code"`
}

type Language struct {
	BaseEntity
	ID   int64  `gorm:"primaryKey"`
	Code string `gorm:"column:code;uniqueIndex"`
}

// TestManyToManyWithACompositeKeyIsRefused. On the owner side the DELETE of stale join rows
// named only the last key column, so clearing member 1's groups in tenant 7 sent `DELETE
// FROM tenant_member_groups WHERE tenant_member_id = ?` and removed member 1's groups in every
// tenant. On the related side the join row stored the tenant id in the code column, and the
// DELETE compared the code column with tenant ids, so it removed every join row of the owner,
// the one being kept included. Until join rows are addressed by every column, such a relation
// is refused, before anything is written, on every datasource.
func TestManyToManyWithACompositeKeyIsRefused(t *testing.T) {
	t.Run("owner side", func(t *testing.T) {
		mockDS, sent := cascadeSession(t, dbCore.DataSourcePolicy{})
		member := &TenantMember{TenantID: 7, ID: 1, Name: "m", Groups: []*MemberGroup{}}
		member.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}, RelationMeta: map[string]*RelationMeta{}})

		err := New[*TenantMember](mockDS.session).Save(member)

		require.Error(t, err)
		assert.Equal(t, `orm: Save cannot write many-to-many relation "Groups": its join table tenant_member_groups `+
			"links a composite key (tenant_member_tenant_id, tenant_member_id, member_group_id), which Save would "+
			"address by one column; write its join rows explicitly", err.Error())
		assert.Empty(t, *sent, "not even the member's own row may be written")
	})

	t.Run("related side", func(t *testing.T) {
		mockDS, sent := cascadeSession(t, dbCore.DataSourcePolicy{})
		owner := &GroupOwner{ID: 1, Groups: []*TenantGroup{{TenantID: 7, Code: "sales"}}}
		owner.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}, RelationMeta: map[string]*RelationMeta{}})

		err := New[*GroupOwner](mockDS.session).SaveRelations(owner)

		require.Error(t, err)
		assert.Equal(t, `orm: Save cannot write many-to-many relation "Groups": its join table group_owner_groups `+
			"links a composite key (group_owner_id, tenant_group_tenant_id, tenant_group_code), which Save would "+
			"address by one column; write its join rows explicitly", err.Error())
		assert.Empty(t, *sent)
	})

	// Loading is not saving: a composite relation that is nil and was never loaded is left
	// alone, as every such relation is, and the owner's row is written.
	t.Run("untouched", func(t *testing.T) {
		mockDS, sent := cascadeSession(t, dbCore.DataSourcePolicy{})
		member := &TenantMember{TenantID: 7, ID: 1, Name: "m"}
		member.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}, RelationMeta: map[string]*RelationMeta{}})

		require.NoError(t, New[*TenantMember](mockDS.session).Save(member))

		assert.Equal(t, []string{"UPDATE tenant_members SET name = ? WHERE tenant_id = ? AND id = ?"}, *sent)
	})
}

// TestManyToManyJoinRowStoresTheReferencedColumn. A relation may reference a column of the
// related entity other than its key, and the join column then holds that column's value. It
// used to be given the related entity's first key column instead, so the join row linked
// language 3 under code "3" and the stale-row DELETE kept that row rather than "en".
func TestManyToManyJoinRowStoresTheReferencedColumn(t *testing.T) {
	mockDS := NewMockDataSource()
	recorder := recordStatements(t, mockDS)
	// The language is already stored, so the cascade links it rather than inserting it.
	mockDS.session.executor.countRawFunc = func(context.Context, string, ...interface{}) (int64, error) {
		return 1, nil
	}
	owner := &LanguageOwner{ID: 1, Languages: []*Language{{ID: 3, Code: "en"}}}
	owner.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}, RelationMeta: map[string]*RelationMeta{}})

	require.NoError(t, New[*LanguageOwner](mockDS.session).SaveRelations(owner))

	assert.Equal(t, []string{
		"INSERT INTO owner_languages (language_owner_id, language_code) VALUES (?, ?) ON CONFLICT (language_owner_id, language_code) DO NOTHING",
		"DELETE FROM owner_languages WHERE language_owner_id = ? AND language_code NOT IN (?)",
	}, recorder.rendered)
	assert.Equal(t, [][]any{{int64(1), "en"}, {int64(1), "en"}}, recorder.args)
}
