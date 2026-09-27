package db

import (
	"database/sql"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// db:diff records each statement the migrator executes and writes it into a migration whose Up
// runs it with dbGorm.Exec and no arguments. Whatever the migrator bound as an argument is
// therefore not in the draft, and a statement that bound one reaches db:migrate with a
// placeholder nothing fills. These tests diff a set of domains that use every feature the
// migrator turns into DDL here — keys, sizes, indexes, unique columns, defaults, a comment, a
// check, a has-many, a belongs-to and a many-to-many — on each dialect db:diff runs on, and
// assert that no recorded statement has a placeholder left in it.
//
// They run over the recording driver rather than a gorm DryRun session. In DryRun gorm
// answers Row() with a nil row, and the SQL Server migrator scans it to ask whether a table
// exists, so a dry run of this diff panics before it records anything. The recording driver
// answers every such question with no rows, so every table is new and every statement that
// creates one is recorded.

type draftAuthor struct {
	ID        uint
	Name      string  `gorm:"size:120;index"`
	Email     string  `gorm:"unique"`
	Nickname  *string `gorm:"default:'anon'"`
	Note      string  `gorm:"comment:shown to editors"`
	Score     int     `gorm:"default:0;not null"`
	Active    bool    `gorm:"default:true"`
	CreatedAt time.Time
	Posts     []draftPost `gorm:"foreignKey:AuthorID"`
	Tags      []draftTag  `gorm:"many2many:draft_author_tags"`
}

type draftPost struct {
	ID       uint
	AuthorID uint
	Author   *draftAuthor
	Title    string `gorm:"check:title <> ''"`
}

type draftTag struct {
	ID    uint
	Label string `gorm:"unique"`
}

// draftDiff runs the diff's own steps for the draft domains on dialector, over the recording
// driver, and returns what db:diff would write into the migration together with the SQL each
// statement was executed as.
func draftDiff(t *testing.T, dialector func(*sql.DB) gorm.Dialector) (drafted []string, executed []string) {
	t.Helper()

	conn := sql.OpenDB(&recording{})
	t.Cleanup(func() { assert.NoError(t, conn.Close()) })

	// Configured as the Postgres and SQL Server datasources configure their own handles: CREATE
	// TABLE declares no foreign key, and each is drafted by migrateModelConstraints.
	gormDb, err := gorm.Open(dialector(conn), &gorm.Config{
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err)

	tx := gormDb.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { assert.NoError(t, tx.Rollback().Error) })

	t.Cleanup(recordStatements(tx, &drafted))
	require.NoError(t, tx.Callback().Raw().Register("test:executed", func(tx *gorm.DB) {
		executed = append(executed, tx.Statement.SQL.String())
	}))

	cmd := DiffCommand{modelStructAlreadyAdded: map[string]bool{}, pivotTables: map[string]bool{}}
	for _, model := range []any{&draftAuthor{}, &draftPost{}, &draftTag{}} {
		require.NoError(t, cmd.migrateModel(model, tx))
	}
	for _, model := range []any{draftAuthor{}, draftPost{}, draftTag{}} {
		require.NoError(t, cmd.migrateModelConstraints(reflect.TypeOf(model), &drafted, tx.Migrator(), tx.Dialector.Name()))
	}
	require.NotEmpty(t, drafted)
	return drafted, executed
}

var draftDialects = map[string]func(*sql.DB) gorm.Dialector{
	"postgres":  func(conn *sql.DB) gorm.Dialector { return postgres.New(postgres.Config{Conn: conn}) },
	"sqlserver": func(conn *sql.DB) gorm.Dialector { return sqlserver.New(sqlserver.Config{Conn: conn}) },
}

// placeholder matches a bind placeholder of any of the engines: SQL Server's @p1, Postgres's
// $1, and the ? MySQL and gorm itself use.
var placeholder = regexp.MustCompile(`@p\d+|\$\d+|\?`)

// TestDiffDraftsHaveNoPlaceholders is the property: every drafted statement runs as it stands.
func TestDiffDraftsHaveNoPlaceholders(t *testing.T) {
	for name, dialector := range draftDialects {
		t.Run(name, func(t *testing.T) {
			drafted, _ := draftDiff(t, dialector)

			for _, statement := range drafted {
				assert.NotRegexpf(t, placeholder, statement,
					"the draft runs this with no arguments, so a placeholder in it is never filled")
			}
		})
	}
}

// TestSQLServerCommentsAreDraftedWithTheirNamesInline: SQL Server stores a column comment with
// sp_addextendedproperty, and its migrator passes the schema, table and column as @p1, @p2 and
// @p3. The draft used to keep the placeholders and lose the values; they are names, and are
// written in as literals.
func TestSQLServerCommentsAreDraftedWithTheirNamesInline(t *testing.T) {
	drafted, executed := draftDiff(t, draftDialects["sqlserver"])

	var comment, bound string
	for i, statement := range drafted {
		if strings.Contains(statement, "sp_addextendedproperty") {
			comment, bound = statement, executed[i]
		}
	}
	require.NotEmpty(t, comment, "the comment must be drafted: %v", drafted)

	assert.Contains(t, bound, "@p1", "the migrator does bind the names")
	assert.Contains(t, comment, "N'shown to editors'")
	assert.Contains(t, comment, "'TABLE', 'draft_authors', 'COLUMN', 'note'")
	assert.NotContains(t, comment, "@p")
}

// TestPostgresDraftsAreTheExecutedStatements: the Postgres migrator binds nothing, so what it
// drafts is still exactly what it executed, byte for byte.
func TestPostgresDraftsAreTheExecutedStatements(t *testing.T) {
	drafted, executed := draftDiff(t, draftDialects["postgres"])

	assert.Equal(t, executed, drafted)
}

// TestSQLServerDraftsAreTheExecutedStatementsApartFromComments: the comment is the one
// statement on SQL Server that binds anything, so it is the only one inlining changes.
func TestSQLServerDraftsAreTheExecutedStatementsApartFromComments(t *testing.T) {
	drafted, executed := draftDiff(t, draftDialects["sqlserver"])
	require.Len(t, drafted, len(executed))

	for i := range drafted {
		if strings.Contains(executed[i], "sp_addextendedproperty") {
			continue
		}
		assert.Equal(t, executed[i], drafted[i])
	}
}

// TestADraftStatementBindingANonStringIsRefused: a bound value that is not a name has no
// literal known to stand in for it, so the statement fails where it ran and the diff stops,
// rather than drafting it with a placeholder or a guess.
func TestADraftStatementBindingANonStringIsRefused(t *testing.T) {
	tx := &gorm.DB{
		Config:    &gorm.Config{Dialector: sqlserver.New(sqlserver.Config{})},
		Statement: &gorm.Statement{Vars: []any{"dbo", []byte{0x01}}},
	}
	tx.Statement.SQL.WriteString("EXEC sp_probe @p1, @p2")

	_, err := draftStatement(tx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "EXEC sp_probe @p1, @p2")
	assert.Contains(t, err.Error(), "[]uint8")
	assert.Contains(t, err.Error(), "by hand")
}

// TestADraftStatementWithoutArgumentsIsUntouched: nothing bound, nothing rewritten, even where
// the SQL itself contains what looks like a placeholder.
func TestADraftStatementWithoutArgumentsIsUntouched(t *testing.T) {
	for name, dialector := range map[string]gorm.Dialector{
		"postgres":  postgres.New(postgres.Config{}),
		"sqlserver": sqlserver.New(sqlserver.Config{}),
	} {
		t.Run(name, func(t *testing.T) {
			tx := &gorm.DB{Config: &gorm.Config{Dialector: dialector}, Statement: &gorm.Statement{}}
			tx.Statement.SQL.WriteString(`CREATE TABLE "t" ("c" text CHECK (c <> '$1 @p1 ?'))`)

			statement, err := draftStatement(tx)

			require.NoError(t, err)
			assert.Equal(t, `CREATE TABLE "t" ("c" text CHECK (c <> '$1 @p1 ?'))`, statement)
		})
	}
}

// TestTheRecorderRecordsOnlyTheDiffsTransaction: gorm keeps callbacks on the handle the
// transaction was begun from, which every session of the datasource shares, not on the
// transaction. So the recorder sees every statement run on the datasource while it is
// registered. One another session runs on the pool during the diff is not the diff's: it must be
// neither drafted nor refused, although it binds a number, which a statement of the diff's own
// may not. And once the recording stops, nothing is recorded or refused, on the transaction or
// off it.
func TestTheRecorderRecordsOnlyTheDiffsTransaction(t *testing.T) {
	for name, dialector := range draftDialects {
		t.Run(name, func(t *testing.T) {
			conn := sql.OpenDB(&recording{})
			t.Cleanup(func() { assert.NoError(t, conn.Close()) })
			gormDb, err := gorm.Open(dialector(conn), &gorm.Config{Logger: logger.Discard})
			require.NoError(t, err)

			tx := gormDb.Begin()
			require.NoError(t, tx.Error)
			t.Cleanup(func() { assert.NoError(t, tx.Rollback().Error) })

			var drafted []string
			stop := recordStatements(tx, &drafted)

			require.NoError(t, tx.Exec("CREATE TABLE probe (id int)").Error)
			require.NoError(t, gormDb.Exec("SELECT 1 WHERE 1 = ?", 1).Error,
				"a statement on the pool during the diff is not the diff's")

			stop()

			require.NoError(t, gormDb.Exec("SELECT 1 WHERE 1 = ?", 1).Error, "nor is one after it")
			require.NoError(t, tx.Exec("SELECT 1 WHERE 1 = ?", 1).Error,
				"nor one on the transaction once the recording has stopped")
			assert.Equal(t, []string{"CREATE TABLE probe (id int)"}, drafted)
		})
	}
}

// TestTheDatasourceBindsArgumentsAgainAfterADiff: the recorder used to stay registered on the
// datasource's handle after db:diff returned, and since it refuses a statement that binds
// anything but a string, every later Exec on that datasource in the same process that bound a
// number, a time or a bool failed with db:diff's error, on Postgres as on SQL Server.
func TestTheDatasourceBindsArgumentsAgainAfterADiff(t *testing.T) {
	inDiffProject(t)
	withArgs(t, "db:diff")
	dbContext := recordedDiffContext(t, &recording{})
	cmd := DiffCommand{
		domainContext: &stubDomainContext{domains: map[string]any{"example.com/app/pkg/domain.Widget": diffWidget{}}},
		dbContext:     dbContext,
	}
	gormDb, ok := dbContext.sources["default"].(*fakeDataSource).driver.(*gorm.DB)
	require.True(t, ok)
	require.NoError(t, gormDb.Exec("SELECT 1 WHERE 1 = ?", 1).Error, "before the diff")

	printed(t, func() { require.NotPanics(t, func() { cmd.Execute(nil) }) })

	assert.NoError(t, gormDb.Exec("SELECT 1 WHERE 1 = ?", 1).Error, "after the diff")
	assert.NoError(t, gormDb.Exec("SELECT 1 WHERE ? < ?", time.Now(), true).Error)
}
