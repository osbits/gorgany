package orm

import (
	"context"
	"strings"
	"sync"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	v2 "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// The relation loads join a many-to-many table on the related table's referenced column, and
// split an IN list the dialect cannot bind in one statement into several queries whose rows are
// merged. The stale-row DELETE of a many-to-many save cannot be split, so it is refused instead.

// limitedDialect is Postgres with a bind-parameter limit, as SQL Server declares one.
type limitedDialect struct {
	*v2.PostgresDialect
	limit int
}

func (d limitedDialect) MaxBindParameters() int { return d.limit }

func limitedBuilder(limit int) func() dbCore.IQueryBuilder {
	return func() dbCore.IQueryBuilder {
		return v2.NewBuilderWithDialect(limitedDialect{&v2.PostgresDialect{}, limit})
	}
}

type chunkParent struct {
	BaseEntity
	ID       int64         `gorm:"primaryKey"`
	Children []*chunkChild `gorm:"foreignKey:ParentID"`
	Badges   []*chunkBadge `gorm:"many2many:chunk_parent_badges;"`
}

type chunkChild struct {
	BaseEntity
	ID       int64 `gorm:"primaryKey"`
	ParentID int64 `gorm:"column:parent_id"`
}

// chunkBadge is keyed by a string column that is not called id.
type chunkBadge struct {
	BaseEntity
	Code string `gorm:"primaryKey;column:code"`
}

type chunkPet struct {
	BaseEntity
	ID      int64 `gorm:"primaryKey"`
	OwnerID int64 `gorm:"column:owner_id"`
	Owner   *chunkOwner
}

type chunkOwner struct {
	BaseEntity
	ID int64 `gorm:"primaryKey"`
}

// relationOf returns the relationship name of model's schema.
func relationOf(t *testing.T, model any, name string) *schema.Relationship {
	t.Helper()
	s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	require.NoError(t, err)
	rel, ok := s.Relationships.Relations[name]
	require.True(t, ok, "relation %s", name)
	return rel
}

// findRecorder records every builder query the mock executor's Find is handed, and answers each
// with what answer puts into its destination.
type findRecorder struct {
	sql   []string
	args  [][]any
	query []*dbCore.Query
}

func recordFinds(t *testing.T, mockDS *MockDataSource, answer func(args []any, dest interface{})) *findRecorder {
	t.Helper()
	recorder := &findRecorder{}
	mockDS.session.executor.queryOneFunc = func(_ context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
		sql, args, err := q.ToSQL()
		require.NoError(t, err)
		recorder.sql = append(recorder.sql, sql)
		recorder.args = append(recorder.args, args)
		recorder.query = append(recorder.query, q.Build())
		if answer != nil {
			answer(args, dest)
		}
		return dbCore.QueryResult{Found: true}
	}
	return recorder
}

// TestManyToManyJoinUsesRelatedPrimaryKeyColumn: the join compared the join table's column
// with a column called id whatever the related table's key was, so a relation to a table keyed
// by code joined on a column that does not exist. It now joins on the referenced column, and
// for a related table keyed by id Postgres renders what the "?.id = ?.?" RawCondition did.
func TestManyToManyJoinUsesRelatedPrimaryKeyColumn(t *testing.T) {
	t.Run("a key that is not id", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordFinds(t, mockDS, nil)
		require.NoError(t, New[*chunkParent](mockDS.session).LoadRelation(&chunkParent{ID: 4}, "Badges"))
		require.Len(t, recorder.sql, 1)
		assert.Equal(t, "SELECT chunk_badges.* FROM chunk_badges INNER JOIN chunk_parent_badges "+
			"ON chunk_badges.code = chunk_parent_badges.chunk_badge_code WHERE chunk_parent_badges.chunk_parent_id = ?",
			recorder.sql[0])
		assert.Equal(t, []any{int64(4)}, recorder.args[0])
	})

	t.Run("a references tag", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordFinds(t, mockDS, nil)
		require.NoError(t, New[*LanguageOwner](mockDS.session).LoadRelation(&LanguageOwner{ID: 2}, "Languages"))
		require.Len(t, recorder.sql, 1)
		assert.Contains(t, recorder.sql[0], "ON languages.code = owner_languages.language_code")
	})

	t.Run("a key called id renders as before", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordFinds(t, mockDS, nil)
		require.NoError(t, New[*TestManyToManyUser](mockDS.session).LoadRelation(&TestManyToManyUser{ID: 9}, "Roles"))
		require.Len(t, recorder.sql, 1)

		before, beforeArgs, err := v2.NewBuilder().
			Select("test_many_to_many_roles.*").
			From("test_many_to_many_roles").
			InnerJoin("test_user_roles", &dbCore.RawCondition{
				SQL:  "?.id = ?.?",
				Args: []any{"test_many_to_many_roles", "test_user_roles", "test_many_to_many_role_id"},
			}).
			Where(&dbCore.BinaryCondition{Left: "test_user_roles.test_many_to_many_user_id", Operator: "=", Right: 9}).
			ToSQL()
		require.NoError(t, err)
		assert.Equal(t, before, recorder.sql[0])
		assert.Equal(t, beforeArgs, recorder.args[0])
	})

	t.Run("the batch load", func(t *testing.T) {
		mockDS := NewMockDataSource()
		recorder := recordFinds(t, mockDS, nil)
		o := New[*chunkParent](mockDS.session)
		parents := map[interface{}]*chunkParent{int64(1): {ID: 1}}
		require.NoError(t, o.batchLoadRelation(parents, []any{int64(1)}, "Badges", nil))
		require.Len(t, recorder.sql, 1)
		assert.Equal(t, "SELECT chunk_badges.*, chunk_parent_badges.chunk_parent_id as _join_fk FROM chunk_badges "+
			"INNER JOIN chunk_parent_badges ON chunk_badges.code = chunk_parent_badges.chunk_badge_code "+
			"WHERE chunk_parent_badges.chunk_parent_id IN (?)", recorder.sql[0])
	})
}

// digitTag's table starts with a digit, which MySQL takes unquoted and Postgres does not.
type digitTag struct {
	BaseEntity
	ID int64 `gorm:"primaryKey"`
}

func (digitTag) TableName() string { return "2024tags" }

// bookTag's table is not ASCII, which Postgres and MySQL both take unquoted.
type bookTag struct {
	BaseEntity
	ID int64 `gorm:"primaryKey"`
}

func (bookTag) TableName() string { return "bücher" }

// quotedTag's table name is quoted in its TableName, as a model on a mixed-case Postgres table
// writes it.
type quotedTag struct {
	BaseEntity
	ID int64 `gorm:"primaryKey"`
}

func (quotedTag) TableName() string { return `"Zz_Tags"` }

type oddlyNamedTagHolder struct {
	BaseEntity
	ID     int64        `gorm:"primaryKey"`
	Digits []*digitTag  `gorm:"many2many:order_2024tags;"`
	Books  []*bookTag   `gorm:"many2many:order_books;"`
	Quoted []*quotedTag `gorm:"many2many:order_quoted;"`
}

// TestManyToManyJoinKeepsNamesThatAreNotSimple: Postgres and MySQL render a JOIN's ON without a
// context, where a BinaryCondition binds any operand that is not a simple ASCII identifier as a
// value. A join on such a related table then compared the join column with the table's name as
// text: MySQL returned no rows and no error, and Postgres refused the cast. The ON is a
// RawCondition whose "?." names are emitted as written, so for a table keyed by id it is what
// "?.id = ?.?" rendered, on both dialects, for every name.
func TestManyToManyJoinKeepsNamesThatAreNotSimple(t *testing.T) {
	for _, tc := range []struct {
		dialect  string
		builder  func() dbCore.IQueryBuilder
		relation string
		table    string
		join     string
		column   string
	}{
		{"mysql", mysqlBuilder, "Digits", "2024tags", "order_2024tags", "digit_tag_id"},
		{"mysql", mysqlBuilder, "Books", "bücher", "order_books", "book_tag_id"},
		{"postgres", pgBuilder, "Books", "bücher", "order_books", "book_tag_id"},
		{"postgres", pgBuilder, "Quoted", `"Zz_Tags"`, "order_quoted", "quoted_tag_id"},
	} {
		t.Run(tc.dialect+" "+tc.table, func(t *testing.T) {
			mockDS := NewMockDataSource()
			mockDS.session.queryFn = tc.builder
			recorder := recordFinds(t, mockDS, nil)
			require.NoError(t, New[*oddlyNamedTagHolder](mockDS.session).LoadRelation(&oddlyNamedTagHolder{ID: 3}, tc.relation))
			require.Len(t, recorder.sql, 1)

			before, beforeArgs, err := tc.builder().
				Select(tc.table+".*").
				From(tc.table).
				InnerJoin(tc.join, &dbCore.RawCondition{SQL: "?.id = ?.?", Args: []any{tc.table, tc.join, tc.column}}).
				Where(&dbCore.BinaryCondition{Left: tc.join + ".oddly_named_tag_holder_id", Operator: "=", Right: int64(3)}).
				ToSQL()
			require.NoError(t, err)
			assert.Equal(t, before, recorder.sql[0])
			assert.Contains(t, recorder.sql[0], " ON "+tc.table+".id = "+tc.join+"."+tc.column+" ")
			assert.Equal(t, beforeArgs, recorder.args[0])
			assert.Equal(t, []any{int64(3)}, recorder.args[0], "no name is bound as a value")
		})
	}
}

// TestManyToManySelectListIsSplit: the batch load's select list is two items. As one string,
// "t.*, j.c as _join_fk", it is an expression a dialect that quotes names can only emit as
// written; as two, each is a name it recognises. Postgres joins them as it joined the one.
func TestManyToManySelectListIsSplit(t *testing.T) {
	mockDS := NewMockDataSource()
	recorder := recordFinds(t, mockDS, nil)
	o := New[*chunkParent](mockDS.session)
	require.NoError(t, o.batchLoadRelation(map[interface{}]*chunkParent{int64(1): {ID: 1}}, []any{int64(1)}, "Badges", nil))
	require.Len(t, recorder.query, 1)
	assert.Equal(t, []string{"chunk_badges.*", "chunk_parent_badges.chunk_parent_id as _join_fk"},
		recorder.query[0].Select.Fields)
}

// TestInListChunkingRespectsDialectLimit runs each IN site against a dialect that allows three
// bind parameters per statement.
func TestInListChunkingRespectsDialectLimit(t *testing.T) {
	keys := []any{int64(1), int64(2), int64(3), int64(4), int64(5)}

	t.Run("has many", func(t *testing.T) {
		mockDS := NewMockDataSource()
		mockDS.session.queryFn = limitedBuilder(3)
		recorder := recordFinds(t, mockDS, func(args []any, dest interface{}) {
			// One child per parent named in this query's IN; the last arg is the condition's.
			loaded := dest.(*[]interface{})
			for _, arg := range args[:len(args)-1] {
				*loaded = append(*loaded, &chunkChild{ID: arg.(int64) * 10, ParentID: arg.(int64)})
			}
		})

		parents := map[interface{}]*chunkParent{}
		for _, key := range keys {
			parents[key] = &chunkParent{ID: key.(int64)}
		}
		o := New[*chunkParent](mockDS.session)
		require.NoError(t, o.batchLoadRelation(parents, keys, "Children", map[string]interface{}{"kind": "toy"}))

		// One condition value leaves two per IN list.
		require.Len(t, recorder.sql, 3)
		for _, sql := range recorder.sql {
			assert.True(t, strings.HasSuffix(sql, " AND kind = ?"), sql)
		}
		assert.Equal(t, [][]any{{int64(1), int64(2), "toy"}, {int64(3), int64(4), "toy"}, {int64(5), "toy"}}, recorder.args)
		for _, parent := range parents {
			require.Len(t, parent.Children, 1, "every chunk's rows are merged")
			assert.Equal(t, parent.ID*10, parent.Children[0].ID)
		}
	})

	t.Run("belongs to", func(t *testing.T) {
		mockDS := NewMockDataSource()
		mockDS.session.queryFn = limitedBuilder(3)
		recorder := recordFinds(t, mockDS, func(args []any, dest interface{}) {
			loaded := dest.(*[]interface{})
			for _, arg := range args {
				*loaded = append(*loaded, &chunkOwner{ID: arg.(int64)})
			}
		})

		pets := map[interface{}]*chunkPet{}
		for _, key := range keys {
			pets[key] = &chunkPet{ID: key.(int64), OwnerID: key.(int64) + 100}
		}
		o := New[*chunkPet](mockDS.session)
		require.NoError(t, o.batchLoadRelation(pets, keys, "Owner", nil))

		require.Len(t, recorder.sql, 2, "three owner keys per query")
		total := 0
		for _, args := range recorder.args {
			assert.LessOrEqual(t, len(args), 3)
			total += len(args)
		}
		assert.Equal(t, 5, total)
		for _, pet := range pets {
			require.NotNil(t, pet.Owner, "every chunk's rows are merged")
			assert.Equal(t, pet.OwnerID, pet.Owner.ID)
		}
	})

	t.Run("many to many", func(t *testing.T) {
		mockDS := NewMockDataSource()
		mockDS.session.queryFn = limitedBuilder(3)
		recorder := recordFinds(t, mockDS, nil)

		o := New[*chunkParent](mockDS.session)
		parents := map[interface{}]*chunkParent{}
		for _, key := range keys {
			parents[key] = &chunkParent{ID: key.(int64)}
		}
		require.NoError(t, o.batchLoadRelation(parents, keys, "Badges", nil))
		assert.Equal(t, [][]any{{int64(1), int64(2), int64(3)}, {int64(4), int64(5)}}, recorder.args)
	})

	t.Run("the stale join rows of a save", func(t *testing.T) {
		save := func(badges int) ([]string, [][]any, error) {
			mockDS := NewMockDataSource()
			mockDS.session.queryFn = limitedBuilder(3)
			var sent []string
			var sentArgs [][]any
			mockDS.session.executor.execFunc = func(_ context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
				sql, args, err := q.ToSQL()
				require.NoError(t, err)
				sent, sentArgs = append(sent, sql), append(sentArgs, args)
				return dbCore.QueryResult{RowsAffected: 1}
			}
			mockDS.session.executor.countRawFunc = func(context.Context, string, ...interface{}) (int64, error) {
				return 1, nil // every badge is already stored
			}

			parent := &chunkParent{ID: 1}
			parent.SetMeta(&EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}})
			for i := 0; i < badges; i++ {
				parent.Badges = append(parent.Badges, &chunkBadge{Code: string(rune('a' + i))})
			}
			err := New[*chunkParent](mockDS.session).SaveRelations(parent)
			return sent, sentArgs, err
		}

		sent, args, err := save(2)
		require.NoError(t, err)
		require.NotEmpty(t, sent)
		assert.Equal(t, "DELETE FROM chunk_parent_badges WHERE chunk_parent_id = ? AND chunk_badge_code NOT IN (?, ?)", sent[len(sent)-1])
		assert.Equal(t, []any{int64(1), "a", "b"}, args[len(args)-1], "the owner's key and two kept keys fit in three")

		sent, _, err = save(3)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `many-to-many relation "Badges"`)
		assert.Contains(t, err.Error(), "the dialect allows 3 per statement")
		assert.Empty(t, sent, "refused before any join row is written")
	})
}

// TestNoChunkingWithoutLimit: Postgres and MySQL declare no limit, so a load is the one query
// it always was, however many keys it names.
func TestNoChunkingWithoutLimit(t *testing.T) {
	keys := make([]any, 5000)
	parents := map[interface{}]*chunkParent{}
	for i := range keys {
		keys[i] = int64(i + 1)
		parents[keys[i]] = &chunkParent{ID: int64(i + 1)}
	}

	mockDS := NewMockDataSource()
	recorder := recordFinds(t, mockDS, nil)
	require.NoError(t, New[*chunkParent](mockDS.session).batchLoadRelation(parents, keys, "Children", nil))
	require.Len(t, recorder.sql, 1)
	assert.Len(t, recorder.args[0], 5000)
}

// TestChunkValues pins the arithmetic: the chunks together hold every value in order, each
// fits beside the reserved parameters, and no limit is one chunk.
func TestChunkValues(t *testing.T) {
	values := []any{1, 2, 3, 4, 5}
	assert.Equal(t, [][]any{values}, chunkValues(values, 0, 0), "no limit")
	assert.Equal(t, [][]any{values}, chunkValues(values, -1, 3), "a negative limit is none")
	assert.Equal(t, [][]any{values}, chunkValues(values, 6, 1), "fits exactly")
	assert.Equal(t, [][]any{{1, 2}, {3, 4}, {5}}, chunkValues(values, 3, 1))
	assert.Equal(t, [][]any{{1}, {2}, {3}, {4}, {5}}, chunkValues(values, 2, 5),
		"a chunk holds one value when the reserved parameters take the whole limit")

	chunks := chunkValues(values, 3, 1)
	chunks[0] = append(chunks[0], 99)
	assert.Equal(t, []any{1, 2, 3, 4, 5}, values, "a chunk cannot be appended into the next")

	assert.True(t, fitsOneStatement(2, 3, 1))
	assert.False(t, fitsOneStatement(3, 3, 1))
	assert.True(t, fitsOneStatement(1<<20, 0, 1))
}
