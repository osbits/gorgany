package db

import (
	"reflect"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// gormWithDialector is a *gorm.DB that knows its dialect and has no connection, so
// anything that tried to run SQL on it would panic rather than pass.
func gormWithDialector(dialector gorm.Dialector) *gorm.DB {
	return &gorm.DB{Config: &gorm.Config{Dialector: dialector}}
}

// TestDiffRefusesADialectThatCommitsDDL is the regression for db:diff on MySQL. It
// ran CREATE TABLE and ALTER TABLE inside a transaction and rolled back, but MySQL
// commits DDL implicitly, so the diff applied the schema changes to the database it
// was comparing, and the migration it wrote then failed there with "Table ... already
// exists". It must now stop before it runs anything.
func TestDiffRefusesADialectThatCommitsDDL(t *testing.T) {
	withArgs(t, "db:diff", "--datasource=legacy")

	cmd := DiffCommand{dbContext: &fakeDBContext{sources: map[string]dbCore.IDataSource{
		"default": &fakeDataSource{driver: gormWithDialector(postgres.New(postgres.Config{}))},
		"legacy":  &fakeDataSource{driver: gormWithDialector(mysql.New(mysql.Config{}))},
	}}}

	defer func() {
		r := recover()
		require.NotNil(t, r, "db:diff must refuse a MySQL datasource")
		err, ok := r.(error)
		require.Truef(t, ok, "expected the refusal error, got %v", r)
		assert.Contains(t, err.Error(), `datasource "legacy"`)
		assert.Contains(t, err.Error(), "(mysql)")
		assert.Contains(t, err.Error(), "commits DDL immediately")
		assert.Contains(t, err.Error(), "by hand")
	}()
	cmd.Execute(nil)
}

func TestRequireTransactionalDDL(t *testing.T) {
	t.Run("postgres rolls DDL back", func(t *testing.T) {
		assert.NoError(t, requireTransactionalDDL(gormWithDialector(postgres.New(postgres.Config{})), "default"))
	})

	t.Run("mysql does not", func(t *testing.T) {
		err := requireTransactionalDDL(gormWithDialector(mysql.New(mysql.Config{})), "default")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "(mysql)")
		assert.Contains(t, err.Error(), "db:diff supports postgres")
	})

	// A dialect nobody has vouched for is refused: the list is an allow-list.
	t.Run("an unknown dialect is refused", func(t *testing.T) {
		require.Error(t, requireTransactionalDDL(&gorm.DB{}, "default"))
		require.Error(t, requireTransactionalDDL(nil, "default"))
	})
}

// columnMigrator answers HasColumn from a fixed set and records every question. Any
// other migrator call panics on the nil embedded interface; HasConstraint reports true
// so that no constraint DDL is attempted.
type columnMigrator struct {
	gorm.Migrator
	existing map[string]bool
	asked    []string
}

func (m *columnMigrator) HasColumn(value any, field string) bool {
	table, _ := value.(string)
	m.asked = append(m.asked, table+"."+field)
	return m.existing[table+"."+field]
}

func (m *columnMigrator) HasConstraint(any, string) bool { return true }

type diffParent struct {
	ID string
}

type diffChild struct {
	diffParent `grgorm:"extends"`
	Name       string
}

// TestStructColumnIsCheckedOnTheSelectedDatasource: the struct-model column check used
// to query information_schema through db.Builder(), which is always `default`, so a
// diff of another datasource looked at the wrong database. It must ask the migrator it
// was given, which belongs to the selected datasource.
func TestStructColumnIsCheckedOnTheSelectedDatasource(t *testing.T) {
	t.Run("missing column is added", func(t *testing.T) {
		migrator := &columnMigrator{}
		var statements []string

		cmd := DiffCommand{modelStructAlreadyAdded: map[string]bool{}}
		require.NoError(t, cmd.migrateModelConstraints(reflect.TypeOf(diffChild{}), &statements, migrator))

		assert.Equal(t, []string{"diff_parents.model_struct"}, migrator.asked)
		assert.Equal(t, []string{"ALTER TABLE diff_parents ADD COLUMN IF NOT EXISTS model_struct varchar(255)"}, statements)
	})

	t.Run("existing column is left alone", func(t *testing.T) {
		migrator := &columnMigrator{existing: map[string]bool{"diff_parents.model_struct": true}}
		var statements []string

		cmd := DiffCommand{modelStructAlreadyAdded: map[string]bool{}}
		require.NoError(t, cmd.migrateModelConstraints(reflect.TypeOf(diffChild{}), &statements, migrator))

		assert.Empty(t, statements)
	})

	// It checked for the configured column and then added model_struct, so a configured
	// name was never the one added, and every later diff proposed it again.
	t.Run("configured column name is the one added", func(t *testing.T) {
		viper.Set("gorm.model.embed.structColumn", "kind")
		t.Cleanup(func() { viper.Set("gorm.model.embed.structColumn", "") })

		migrator := &columnMigrator{}
		var statements []string

		cmd := DiffCommand{modelStructAlreadyAdded: map[string]bool{}}
		require.NoError(t, cmd.migrateModelConstraints(reflect.TypeOf(diffChild{}), &statements, migrator))

		assert.Equal(t, []string{"diff_parents.kind"}, migrator.asked)
		assert.Equal(t, []string{"ALTER TABLE diff_parents ADD COLUMN IF NOT EXISTS kind varchar(255)"}, statements)
	})
}
