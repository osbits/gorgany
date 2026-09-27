package orm

import (
	"context"
	"errors"
	"reflect"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Save cascades into every related entity it holds and rewrites many-to-many join rows. On a
// datasource whose schema another system owns (external_schema: true) those related tables
// are that system's too, so a cascade writes into tables the caller never named. It is
// refused there unless the model opts in with CascadingSaves, and refused before the
// entity's own row is written, and before any related entity's, so a refused Save writes
// nothing.

// cascadeRefusal is the refusal's text for relation, a path from the saved entity.
func cascadeRefusal(relation string) string {
	return `orm: refusing to cascade Save into relation "` + relation + `" on an external_schema ` +
		`datasource; save related entities explicitly or implement CascadeSaves()`
}

// CascadeOwner has one relation of each kind. optIn is unexported, so gorm and the ORM's
// field walk ignore it; it lets one type answer CascadeSaves either way.
type CascadeOwner struct {
	BaseEntity
	ID      int             `gorm:"primaryKey"`
	Name    string          `gorm:"column:name"`
	GroupID int             `gorm:"column:group_id"`
	Group   *CascadeGroup   // belongs to
	Profile *CascadeProfile // has one
	Items   []*CascadeItem  // has many
	Tags    []*CascadeTag   `gorm:"many2many:cascade_owner_tags;"`

	optIn bool
}

func (o *CascadeOwner) CascadeSaves() bool { return o.optIn }

type CascadeGroup struct {
	BaseEntity
	ID    int    `gorm:"primaryKey"`
	Title string `gorm:"column:title"`
}

type CascadeProfile struct {
	BaseEntity
	ID             int    `gorm:"primaryKey"`
	CascadeOwnerID int    `gorm:"column:cascade_owner_id"`
	Bio            string `gorm:"column:bio"`
}

type CascadeItem struct {
	BaseEntity
	ID             int    `gorm:"primaryKey"`
	CascadeOwnerID int    `gorm:"column:cascade_owner_id"`
	Label          string `gorm:"column:label"`
}

type CascadeTag struct {
	BaseEntity
	ID   int    `gorm:"primaryKey"`
	Name string `gorm:"column:name"`
}

func loadedCascadeOwner() *CascadeOwner {
	owner := &CascadeOwner{ID: 1, Name: "owner"}
	owner.SetMeta(&EntityMeta{
		TableName:     "cascade_owners",
		PrimaryKey:    "id",
		IsLoaded:      true,
		LoadedColumns: make(map[string]bool),
		RelationMeta:  make(map[string]*RelationMeta),
	})
	return owner
}

// cascadeSession returns a mock session under policy that records every statement sent
// through it: builder statements, RETURNING inserts and raw queries alike.
func cascadeSession(t *testing.T, policy dbCore.DataSourcePolicy) (*MockDataSource, *[]string) {
	t.Helper()
	mockDS := NewMockDataSource()
	mockDS.policy = policy
	sent := &[]string{}
	record := func(q dbCore.IQueryBuilder) {
		sql, _, err := q.ToSQL()
		require.NoError(t, err)
		*sent = append(*sent, sql)
	}
	mockDS.session.executor.execFunc = func(_ context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
		record(q)
		return dbCore.QueryResult{RowsAffected: 1}
	}
	mockDS.session.executor.queryOneFunc = func(_ context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
		record(q)
		if generated, ok := dest.(*map[string]interface{}); ok {
			*generated = map[string]interface{}{"id": 99}
		}
		return dbCore.QueryResult{Found: true, RowsAffected: 1}
	}
	mockDS.session.executor.queryRawFunc = func(_ context.Context, _ interface{}, sql string, _ ...interface{}) dbCore.QueryResult {
		*sent = append(*sent, sql)
		return dbCore.QueryResult{}
	}
	mockDS.session.executor.countRawFunc = func(_ context.Context, sql string, _ ...interface{}) (int64, error) {
		*sent = append(*sent, sql)
		return 0, nil
	}
	return mockDS, sent
}

var externalSchema = dbCore.DataSourcePolicy{ExternalSchema: true}

// TestSaveRelationsRefusesCascadeOnExternalSchema covers each relation kind, through
// SaveRelations and through the Save that calls it, for a model that answers false and for
// one that does not implement CascadingSaves at all.
func TestSaveRelationsRefusesCascadeOnExternalSchema(t *testing.T) {
	relations := map[string]func(*CascadeOwner){
		"Group":   func(o *CascadeOwner) { o.Group = &CascadeGroup{Title: "g"} },
		"Profile": func(o *CascadeOwner) { o.Profile = &CascadeProfile{Bio: "b"} },
		"Items":   func(o *CascadeOwner) { o.Items = []*CascadeItem{{Label: "i"}} },
		"Tags":    func(o *CascadeOwner) { o.Tags = []*CascadeTag{{ID: 5, Name: "t"}} },
	}
	for relation, set := range relations {
		t.Run(relation, func(t *testing.T) {
			mockDS, sent := cascadeSession(t, externalSchema)
			owner := loadedCascadeOwner()
			set(owner)

			err := New[*CascadeOwner](mockDS.session).SaveRelations(owner)

			require.Error(t, err)
			assert.Equal(t, cascadeRefusal(relation), err.Error())
			assert.True(t, errors.Is(err, dbCore.ErrExternalSchema),
				"the refusal must wrap core.ErrExternalSchema like every other policy refusal")
			assert.Empty(t, *sent, "nothing may be written")
		})
	}

	// Save writes the entity's own row before it cascades, so the refusal has to come first:
	// otherwise the row lands and the Save still reports failure.
	for name, loaded := range map[string]bool{"create": false, "update": true} {
		t.Run("Save refuses before writing the entity itself on "+name, func(t *testing.T) {
			mockDS, sent := cascadeSession(t, externalSchema)
			owner := loadedCascadeOwner()
			owner.GetMeta().IsLoaded = loaded
			owner.Profile = &CascadeProfile{Bio: "b"}

			err := New[*CascadeOwner](mockDS.session).Save(owner)

			require.True(t, errors.Is(err, dbCore.ErrExternalSchema), "expected the refusal, got %v", err)
			assert.Empty(t, *sent, "the entity's own row must not be written either")
		})
	}

	t.Run("a model that does not implement CascadingSaves", func(t *testing.T) {
		mockDS, sent := cascadeSession(t, externalSchema)
		user := &TestManyToManyUser{ID: 1, Name: "u", Roles: []*TestManyToManyRole{{ID: 10, Name: "admin"}}}
		user.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}, RelationMeta: map[string]*RelationMeta{}})

		err := New[*TestManyToManyUser](mockDS.session).SaveRelations(user)

		require.True(t, errors.Is(err, dbCore.ErrExternalSchema), "expected the refusal, got %v", err)
		assert.Equal(t, cascadeRefusal("Roles"), err.Error())
		assert.Empty(t, *sent)
	})
}

// TestSaveRelationsRefusesClearingAManyToManyOnExternalSchema. An empty many-to-many slice
// is not "nothing to save": it is how a caller asks SaveRelations to delete the entity's
// join rows, and that DELETE lands in a table the other system owns. A relation that was
// loaded and then set to nil asks for the same DELETE. One that was never loaded and is
// still nil asks for nothing, and SaveRelations leaves its join rows alone.
func TestSaveRelationsRefusesClearingAManyToManyOnExternalSchema(t *testing.T) {
	t.Run("set but empty", func(t *testing.T) {
		mockDS, sent := cascadeSession(t, externalSchema)
		owner := loadedCascadeOwner()
		owner.Tags = []*CascadeTag{}

		err := New[*CascadeOwner](mockDS.session).SaveRelations(owner)

		require.True(t, errors.Is(err, dbCore.ErrExternalSchema), "expected the refusal, got %v", err)
		assert.Equal(t, cascadeRefusal("Tags"), err.Error())
		assert.Empty(t, *sent)
	})

	t.Run("loaded and then set to nil", func(t *testing.T) {
		mockDS, sent := cascadeSession(t, externalSchema)
		owner := loadedCascadeOwner()
		owner.GetMeta().SetRelationLoaded("Tags", &RelationMeta{Type: "many2many"})
		owner.Tags = nil

		err := New[*CascadeOwner](mockDS.session).Save(owner)

		require.True(t, errors.Is(err, dbCore.ErrExternalSchema), "expected the refusal, got %v", err)
		assert.Equal(t, cascadeRefusal("Tags"), err.Error())
		assert.Empty(t, *sent, "neither the owner's row nor the join-row DELETE may be sent")
	})

	t.Run("never loaded and nil", func(t *testing.T) {
		mockDS, sent := cascadeSession(t, externalSchema)
		owner := loadedCascadeOwner()

		require.NoError(t, New[*CascadeOwner](mockDS.session).Save(owner))

		assert.Equal(t, []string{"UPDATE cascade_owners SET group_id = ?, name = ? WHERE id = ?"}, *sent,
			"only the owner's own row may be written")
	})
}

// TestSaveRelationsCascadesWhenTheModelOptsIn. CascadeSaves returning true restores the
// cascade for that model.
func TestSaveRelationsCascadesWhenTheModelOptsIn(t *testing.T) {
	mockDS, sent := cascadeSession(t, externalSchema)
	owner := loadedCascadeOwner()
	owner.optIn = true
	owner.Profile = &CascadeProfile{Bio: "b"}

	require.NoError(t, New[*CascadeOwner](mockDS.session).SaveRelations(owner))

	assert.Equal(t, []string{"INSERT INTO cascade_profiles (cascade_owner_id, bio) VALUES (?, ?) RETURNING id"}, *sent)
	assert.Equal(t, 1, owner.Profile.CascadeOwnerID, "the foreign key must still be set")
}

// TestSaveRelationsStillCascadesOnOwnedDatasource. A datasource gorgany owns cascades as it
// always has, whatever the model answers.
func TestSaveRelationsStillCascadesOnOwnedDatasource(t *testing.T) {
	mockDS, sent := cascadeSession(t, dbCore.DataSourcePolicy{})
	owner := loadedCascadeOwner()
	owner.Profile = &CascadeProfile{Bio: "b"}
	owner.Items = []*CascadeItem{{Label: "i"}}

	require.NoError(t, New[*CascadeOwner](mockDS.session).SaveRelations(owner))

	// The relation map is walked in no fixed order, so the two INSERTs may come either way.
	assert.ElementsMatch(t, []string{
		"INSERT INTO cascade_profiles (cascade_owner_id, bio) VALUES (?, ?) RETURNING id",
		"INSERT INTO cascade_items (cascade_owner_id, label) VALUES (?, ?) RETURNING id",
	}, *sent)
}

// TestSaveRelationsIgnoresNilAndEmptyRelations. A relation SaveRelations would not write
// through cannot be a reason to refuse: an external_schema Save of an entity whose relations
// were never set must go through, and write only the entity's own row.
func TestSaveRelationsIgnoresNilAndEmptyRelations(t *testing.T) {
	mockDS, sent := cascadeSession(t, externalSchema)
	owner := loadedCascadeOwner()
	owner.Items = []*CascadeItem{} // empty has-many: nothing to save
	// Group, Profile and Tags stay nil, and Tags was never loaded.

	require.NoError(t, New[*CascadeOwner](mockDS.session).SaveRelations(owner))
	assert.Empty(t, *sent)

	require.NoError(t, New[*CascadeOwner](mockDS.session).Save(owner))
	assert.Equal(t, []string{"UPDATE cascade_owners SET group_id = ?, name = ? WHERE id = ?"}, *sent,
		"only the entity's own row may be written")
}

// CascadeParent, CascadeChild and CascadeToy are three levels of has-many and has-one, and
// each answers CascadeSaves from its own unexported optIn.
type CascadeParent struct {
	BaseEntity
	ID       int             `gorm:"primaryKey"`
	Name     string          `gorm:"column:name"`
	Children []*CascadeChild // has many

	optIn bool
}

func (p *CascadeParent) CascadeSaves() bool { return p.optIn }

type CascadeChild struct {
	BaseEntity
	ID              int            `gorm:"primaryKey"`
	CascadeParentID int            `gorm:"column:cascade_parent_id"`
	Label           string         `gorm:"column:label"`
	Toy             *CascadeToy    // has one
	Parent          *CascadeParent `gorm:"foreignKey:CascadeParentID"` // belongs to, back up the tree

	optIn bool
}

func (c *CascadeChild) CascadeSaves() bool { return c.optIn }

type CascadeToy struct {
	BaseEntity
	ID             int    `gorm:"primaryKey"`
	CascadeChildID int    `gorm:"column:cascade_child_id"`
	Name           string `gorm:"column:name"`
}

// TestSaveRefusesANestedCascadeBeforeWritingAnything. A related entity is asked whether it
// opts in only when the cascade reaches it, and by then the parent's row and every related
// row saved before it were written. So a parent that opted in, holding a child that did not
// and that had a relation of its own, was written in part, in an order the relation map
// chose, and Save still returned the refusal, which reads as "nothing happened": a retry
// wrote the same rows again. The refusal now comes from a walk of everything the cascade
// would save, before any of it, and names the path to the relation it refuses.
func TestSaveRefusesANestedCascadeBeforeWritingAnything(t *testing.T) {
	for name, loaded := range map[string]bool{"create": false, "update": true} {
		t.Run(name, func(t *testing.T) {
			mockDS, sent := cascadeSession(t, externalSchema)
			parent := &CascadeParent{ID: 1, Name: "p", optIn: true, Children: []*CascadeChild{
				{Label: "ok"},
				{Label: "bad", Toy: &CascadeToy{Name: "t"}},
			}}
			parent.SetMeta(&EntityMeta{IsLoaded: loaded, LoadedColumns: map[string]bool{}, RelationMeta: map[string]*RelationMeta{}})

			err := New[*CascadeParent](mockDS.session).Save(parent)

			require.True(t, errors.Is(err, dbCore.ErrExternalSchema), "expected the refusal, got %v", err)
			assert.Equal(t, cascadeRefusal("Children.Toy"), err.Error())
			assert.Empty(t, *sent, "no row may be written: not the parent's, and not the first child's")
		})
	}

	t.Run("every level opted in", func(t *testing.T) {
		mockDS, sent := cascadeSession(t, externalSchema)
		parent := &CascadeParent{Name: "p", optIn: true, Children: []*CascadeChild{
			{Label: "c", optIn: true, Toy: &CascadeToy{Name: "t"}},
		}}

		require.NoError(t, New[*CascadeParent](mockDS.session).Save(parent))

		assert.Equal(t, []string{
			"INSERT INTO cascade_parents (name) VALUES (?) RETURNING id",
			"INSERT INTO cascade_children (cascade_parent_id, label) VALUES (?, ?) RETURNING id",
			"INSERT INTO cascade_toys (cascade_child_id, name) VALUES (?, ?) RETURNING id",
		}, *sent)
	})
}

// TestCheckCascadeEndsOnACycle. A child that points back at its parent makes the relations a
// cycle, and the walk visits each entity once, so it ends rather than recursing until the
// stack runs out.
func TestCheckCascadeEndsOnACycle(t *testing.T) {
	for name, policy := range map[string]dbCore.DataSourcePolicy{"owned": {}, "external_schema": externalSchema} {
		t.Run(name, func(t *testing.T) {
			mockDS, sent := cascadeSession(t, policy)
			parent := &CascadeParent{ID: 1, optIn: true}
			parent.Children = []*CascadeChild{{ID: 2, CascadeParentID: 1, optIn: true, Parent: parent}}
			parentSchema, err := keySchema(parent)
			require.NoError(t, err)

			orm := New[*CascadeParent](mockDS.session)
			require.NoError(t, orm.checkCascade(parent, parentSchema, reflect.ValueOf(parent)))
			assert.Empty(t, *sent, "the walk only reads the entities")
		})
	}
}
