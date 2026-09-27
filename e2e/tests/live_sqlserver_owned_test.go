//go:build livedb

// The SQL Server cases for SQL Server as an owned `default`: the sessions migrations in both
// directions, db:migrate up and down, the session sweep, and db:diff. They are behind the
// `livedb` tag and the SQL Server gate for the same reasons as live_sqlserver_test.go, whose
// helpers they use; its header says how to start the engine and run them. The db:migrate
// cases reuse the Postgres cases' assertions in live_db_test.go, and db:seed's are beside
// db:migrate up's in live_sqlserver_test.go.
package e2e

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	dbCmd "github.com/osbits/gorgany/v2/command/db"
	"github.com/osbits/gorgany/v2/db"
	"github.com/osbits/gorgany/v2/db/migration"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	pgv2 "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	"github.com/osbits/gorgany/v2/model"
	"github.com/osbits/gorgany/v2/provider"
	"github.com/osbits/gorgany/v2/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ------------------------------------------------------------------ helpers

// mssqlColumnType returns the column's type as INFORMATION_SCHEMA reports it, and its maximum
// length in characters (-1 for MAX, 0 for a type without one), or "" when there is no column.
func mssqlColumnType(t *testing.T, g *gorm.DB, table, column string) (string, int64) {
	t.Helper()

	var row struct {
		DataType  string
		MaxLength int64
	}
	require.NoError(t, g.Raw(
		"SELECT DATA_TYPE AS data_type, COALESCE(CHARACTER_MAXIMUM_LENGTH, 0) AS max_length "+
			"FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_NAME = ? AND COLUMN_NAME = ?",
		table, column).Scan(&row).Error)
	return row.DataType, row.MaxLength
}

// versionDefaults counts the DEFAULT constraints on sessions.version.
func versionDefaults(t *testing.T, g *gorm.DB) int64 {
	t.Helper()

	var n int64
	require.NoError(t, g.Raw(
		"SELECT COUNT(*) FROM sys.default_constraints AS dc "+
			"JOIN sys.columns AS c ON c.object_id = dc.parent_object_id AND c.column_id = dc.parent_column_id "+
			"WHERE dc.parent_object_id = OBJECT_ID(N'sessions') AND c.name = N'version'").Scan(&n).Error)
	return n
}

// ------------------------------------------------------ the sessions migrations

// TestT15_SessionsMigrationOnSQLServer is the T15 round trip on SQL Server, with both sessions
// migrations and the version column's rollback, which on SQL Server has to drop the column's
// DEFAULT constraint first.
func TestT15_SessionsMigrationOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)

	drop := func() { assert.NoError(t, g.Exec("DROP TABLE IF EXISTS [sessions]").Error) }
	drop()
	t.Cleanup(drop)

	sessions := migration.NewSessionsMigration()
	version := migration.NewSessionsVersionMigration()

	require.NoError(t, sessions.Up()(g), "the sessions migration must run on SQL Server")
	assert.True(t, g.Migrator().HasTable("sessions"))
	for _, index := range []string{migration.SessionsExpiryIndex, migration.SessionsUserIDIndex} {
		assert.Truef(t, g.Migrator().HasIndex(migration.SessionsTableModelFor("sqlserver"), index),
			"index %s must exist", index)
	}

	// Unicode keys and bag, and an expiry that carries its offset.
	for column, want := range map[string]struct {
		dataType  string
		maxLength int64
	}{
		"id":         {"nvarchar", 255},
		"user_id":    {"nvarchar", 255},
		"attributes": {"nvarchar", -1},
		"expiry":     {"datetimeoffset", 0},
	} {
		dataType, maxLength := mssqlColumnType(t, g, "sessions", column)
		assert.Equalf(t, want.dataType, dataType, "type of %s", column)
		assert.Equalf(t, want.maxLength, maxLength, "length of %s", column)
	}

	before := describeTable(t, g, "sessions")
	require.NoError(t, sessions.Up()(g), "a second run must be a no-op")
	assert.Equal(t, before, describeTable(t, g, "sessions"))

	require.NoError(t, version.Up()(g))
	assert.True(t, g.Migrator().HasColumn(migration.SessionsVersionTableModel(), migration.SessionsVersionColumn))
	assert.Equal(t, int64(1), versionDefaults(t, g), "NOT NULL DEFAULT 0 gives the column a named constraint")
	require.NoError(t, version.Up()(g), "a second run must be a no-op")

	// A row whose user id and attributes lie outside any single-byte code page survives.
	const userID, attributes = "Zoë 東京 Ω", `{"name":"Ярослава"}`
	require.NoError(t, g.Exec(
		"INSERT INTO [sessions] ([id], [user_id], [expiry], [created_at], [last_activity], [attributes]) VALUES (?, ?, ?, ?, ?, ?)",
		"s1", userID, time.Now().Add(time.Hour), time.Now(), time.Now(), attributes).Error)
	var stored struct {
		UserID     string
		Attributes string
		Version    int64
	}
	require.NoError(t, g.Raw("SELECT [user_id], [attributes], [version] FROM [sessions] WHERE [id] = 's1'").Scan(&stored).Error)
	assert.Equal(t, userID, stored.UserID)
	assert.Equal(t, attributes, stored.Attributes)
	assert.Zero(t, stored.Version, "an existing row reads as version 0")

	// What the constraint drop is for: without it SQL Server refuses the DROP COLUMN.
	err := g.Exec("ALTER TABLE [sessions] DROP COLUMN [version]").Error
	require.Error(t, err, "a column a DEFAULT constraint depends on cannot be dropped")
	assert.Contains(t, []int32{5074, 4922}, sqlServerErrorNumber(err), "%v", err)

	// Down in a transaction that rolls back changes nothing: the dynamic constraint drop is
	// transactional DDL like the rest.
	tx := g.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, version.Down()(tx))
	assert.False(t, tx.Migrator().HasColumn(migration.SessionsVersionTableModel(), migration.SessionsVersionColumn))
	require.NoError(t, tx.Rollback().Error)
	assert.True(t, g.Migrator().HasColumn(migration.SessionsVersionTableModel(), migration.SessionsVersionColumn))
	assert.Equal(t, int64(1), versionDefaults(t, g))

	// Down committed, as db:migrate down runs it.
	tx = g.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, version.Down()(tx), "the version rollback must succeed on SQL Server")
	require.NoError(t, tx.Commit().Error)
	assert.False(t, g.Migrator().HasColumn(migration.SessionsVersionTableModel(), migration.SessionsVersionColumn))
	assert.Zero(t, versionDefaults(t, g), "the constraint goes with its column")
	require.NoError(t, version.Down()(g), "a second rollback is a no-op")

	// And the column can come back.
	require.NoError(t, version.Up()(g))
	assert.Equal(t, int64(1), versionDefaults(t, g))

	require.NoError(t, sessions.Down()(g))
	assert.False(t, g.Migrator().HasTable("sessions"))
}

// ------------------------------------------------ db:migrate on a SQL Server default

// TestT16_MigrateTargetsOnlyTheSelectedDatasourceOnSQLServer is T16 with SQL Server as the
// `default` and Postgres as the second datasource: `--datasource=creatio` sends SQL Server
// nothing, and the run on `default` migrates SQL Server alone and records it there.
func TestT16_MigrateTargetsOnlyTheSelectedDatasourceOnSQLServer(t *testing.T) {
	ms := mssqlDataSource(t, mssqlConfig())
	pg := waitForDatasource(t, func() (dbCore.IDataSource, error) { return pgv2.NewDataSource(pgConfig()) })
	t.Cleanup(func() { assert.NoError(t, pg.Close()) })

	assertMigrateTargetsOnlyTheSelectedDatasource(t,
		migrateTarget{config: mssqlConfig(), gorm: gormOf(t, ms), engine: "sqlserver"},
		migrateTarget{config: pgConfig(), gorm: gormOf(t, pg), engine: "postgres"})
}

// TestT17_MigrateDownActuallyRollsBackOnSQLServer is T17 on a SQL Server `default`. `down` runs
// a migration's Down and deletes its row in one transaction, as `up` runs Up and writes the
// row, and SQL Server's DDL is part of that transaction.
func TestT17_MigrateDownActuallyRollsBackOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	assertMigrateDownActuallyRollsBack(t, gormOf(t, ds), mssqlConfig)
}

// TestT17_StepsRollsBackSeveralAtOnceOnSQLServer covers --steps=n on SQL Server, where the
// newest rows are read with OFFSET … FETCH rather than LIMIT.
func TestT17_StepsRollsBackSeveralAtOnceOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	assertStepsRollsBackSeveralAtOnce(t, gormOf(t, ds), mssqlConfig)
}

// TestT17_RollbackIsTransactionalOnSQLServer: a Down that fails leaves the row and the schema
// as they were. On SQL Server the failing statement dooms the transaction by itself, since
// every connection runs SET XACT_ABORT ON.
func TestT17_RollbackIsTransactionalOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	assertRollbackIsTransactional(t, gormOf(t, ds), mssqlConfig)
}

// TestT17_SessionsMigrationsRollBackThroughDbMigrateOnSQLServer runs the two sessions
// migrations the way an app runs them: through db:migrate, on a SQL Server `default`, wrapped
// with OnOwnedDefault as DbProvider.Boot wraps them. It then rolls them back a step at a time.
// The version migration's Down drops the column's DEFAULT constraint and then the column
// inside the transaction db:migrate opens for it, and its row goes in the same transaction.
func TestT17_SessionsMigrationsRollBackThroughDbMigrateOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)

	reset := func() {
		assert.NoError(t, g.Exec("DROP TABLE IF EXISTS [sessions]").Error)
		assert.NoError(t, g.Migrator().DropTable(&db.Migration{}))
	}
	reset()
	t.Cleanup(reset)

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{"default": mssqlConfig()})
	t.Cleanup(func() { viper.Set("databases", previous) })

	sessions := dbCmd.OnOwnedDefault(migration.NewSessionsMigration())
	version := dbCmd.OnOwnedDefault(migration.NewSessionsVersionMigration())
	hasVersion := func() bool {
		return g.Migrator().HasColumn(migration.SessionsVersionTableModel(), migration.SessionsVersionColumn)
	}

	runMigrateUp(t, "default", sessions, version)
	require.True(t, g.Migrator().HasTable("sessions"), "db:migrate up must create sessions on SQL Server")
	require.True(t, hasVersion())
	assert.Equal(t, int64(1), versionDefaults(t, g))
	assert.Equal(t, int64(2), appliedCount(t, g))

	runMigrateDown(t, "default", 1, sessions, version)
	assert.False(t, hasVersion(), "the version migration's Down must have run")
	assert.Zero(t, versionDefaults(t, g), "and taken the column's DEFAULT constraint with it")
	assert.True(t, g.Migrator().HasTable("sessions"), "one step rolls back one migration")
	assert.False(t, isApplied(t, g, version.Name()))
	assert.True(t, isApplied(t, g, sessions.Name()))

	runMigrateDown(t, "default", 1, sessions, version)
	assert.False(t, g.Migrator().HasTable("sessions"))
	assert.Zero(t, appliedCount(t, g))

	// Both apply again, as on a fresh install.
	runMigrateUp(t, "default", sessions, version)
	assert.True(t, hasVersion())
	assert.Equal(t, int64(2), appliedCount(t, g))
}

// sqlServerColumns lists the columns of table, a name as a statement would resolve it, in order.
func sqlServerColumns(t *testing.T, g *gorm.DB, table string) []string {
	t.Helper()

	var columns []string
	require.NoError(t, g.Raw("SELECT name FROM sys.columns WHERE object_id = OBJECT_ID(?) ORDER BY column_id",
		table).Scan(&columns).Error)
	return columns
}

// TestTheOwnedTablesAreLookedUpInTheirOwnSchemaOnSQLServer: an owned `default` may hold other
// schemas beside gorgany's, an externally owned (EF) one among them, and gorm's SQL Server
// migrator asks whether a table or a column exists in any schema of the database, with a
// collation that folds case. A Sessions table with a Version column in another schema made the
// sessions migration skip its CREATE TABLE and then fail creating an index on a sessions table
// that was not there, and made the version migration's Up do nothing and its Down drop a column
// that was not there. A Migrations or Seeders table in another schema made db:migrate and
// db:seed fail before they ran anything, reading their own bookkeeping table, which did not
// exist. Each now looks where its unqualified DDL lands, and leaves the other schema's tables
// as they were.
func TestTheOwnedTablesAreLookedUpInTheirOwnSchemaOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)

	reset := func() {
		for _, statement := range []string{
			"DROP TABLE IF EXISTS [sessions]",
			"DROP TABLE IF EXISTS [migrations]",
			"DROP TABLE IF EXISTS [seeders]",
			"DROP TABLE IF EXISTS [legacy].[Sessions]",
			"DROP TABLE IF EXISTS [legacy].[migrations]",
			"DROP TABLE IF EXISTS [legacy].[Seeders]",
			"DROP SCHEMA IF EXISTS [legacy]",
		} {
			assert.NoError(t, g.Exec(statement).Error)
		}
	}
	reset()
	t.Cleanup(reset)
	require.NoError(t, g.Exec("CREATE SCHEMA [legacy]").Error)
	require.NoError(t, g.Exec("CREATE TABLE [legacy].[Sessions] ([Id] int PRIMARY KEY, [Version] rowversion)").Error)
	// Named in lower case, as gorgany's own is, since db:migrate down's lookup of its table
	// compared the name exactly; the other two are folded by the collation.
	require.NoError(t, g.Exec("CREATE TABLE [legacy].[migrations] ([MigrationId] nvarchar(150) PRIMARY KEY)").Error)
	require.NoError(t, g.Exec("CREATE TABLE [legacy].[Seeders] ([SeederId] nvarchar(150) PRIMARY KEY)").Error)
	legacyColumns := map[string][]string{
		"legacy.Sessions":   {"Id", "Version"},
		"legacy.migrations": {"MigrationId"},
		"legacy.Seeders":    {"SeederId"},
	}

	t.Run("the sessions migrations", func(t *testing.T) {
		sessions := migration.NewSessionsMigration()
		version := migration.NewSessionsVersionMigration()

		require.NoError(t, sessions.Up()(g))
		assert.Equal(t, []string{"id", "user_id", "expiry", "created_at", "last_activity", "attributes"},
			sqlServerColumns(t, g, "dbo.sessions"))
		for _, index := range []string{migration.SessionsExpiryIndex, migration.SessionsUserIDIndex} {
			assert.Truef(t, g.Migrator().HasIndex(migration.SessionsTableModelFor("sqlserver"), index),
				"index %s must exist", index)
		}

		require.NoError(t, version.Up()(g))
		assert.Contains(t, sqlServerColumns(t, g, "dbo.sessions"), "version")

		require.NoError(t, version.Down()(g))
		assert.NotContains(t, sqlServerColumns(t, g, "dbo.sessions"), "version")
		require.NoError(t, version.Down()(g), "with the column gone, a second rollback is a no-op")

		require.NoError(t, sessions.Down()(g))
		assert.Empty(t, sqlServerColumns(t, g, "dbo.sessions"))
		require.NoError(t, sessions.Down()(g), "with the table gone, a second rollback is a no-op")
	})

	t.Run("db:migrate and db:seed", func(t *testing.T) {
		previous := viper.Get("databases")
		viper.Set("databases", map[string]any{"default": mssqlConfig()})
		t.Cleanup(func() { viper.Set("databases", previous) })

		probe := scopedMigration{name: "legacy_probe", target: "default", table: "legacy_probe"}
		runMigrateDown(t, "default", 1, probe)
		assert.Empty(t, sqlServerColumns(t, g, "dbo.migrations"), "down has nothing to roll back, and creates nothing")

		runMigrateUp(t, "default", probe)
		t.Cleanup(func() { assert.NoError(t, g.Exec("DROP TABLE IF EXISTS [legacy_probe]").Error) })
		assert.True(t, isApplied(t, g, "legacy_probe"), "db:migrate must record it in its own table")

		runSeed(t, "default", probeSeeder{name: "legacy_seed"})
		assert.True(t, isSeeded(t, g, "legacy_seed"), "db:seed must record it in its own table")

		// A second run finds its own tables and leaves them as they are.
		runMigrateUp(t, "default", probe)
		runSeed(t, "default", probeSeeder{name: "legacy_seed"})
		assert.Equal(t, []string{"name", "date"}, sqlServerColumns(t, g, "dbo.migrations"))
		assert.Equal(t, []string{"name", "date"}, sqlServerColumns(t, g, "dbo.seeders"))
	})

	for table, columns := range legacyColumns {
		assert.Equalf(t, columns, sqlServerColumns(t, g, table), "%s must be as it was", table)
	}
}

// ------------------------------------------------------------------ the sweep

// TestTheSessionSweepBatchesOnSQLServer runs BatchedExpiredDeleteSQLFor("sqlserver") through
// the session executor, as DeleteExpired does: T-SQL has neither LIMIT nor NOW(), so the
// portable statement would be a syntax error here.
func TestTheSessionSweepBatchesOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	sweepProbe(t, g, 25)

	batches := runBatchedSweep(t, ds, 10)

	assert.Equal(t, 3, batches, "25 expired rows at 10 per batch is three statements")
	assert.Equal(t, int64(1), countSessions(t, g), "the unexpired session must survive")
}

// TestDeleteExpiredPicksTSQLThroughTheRealRepository runs the repository itself, wired by the
// container over a SQL Server `default`, so the statement is chosen by the dialect the real
// datasource reports.
//
// Two rows are written with an offset other than the server's. The one that expired half an
// hour ago is written at +05:00, where its wall-clock time is still ahead of the server's; the
// one that expires in half an hour is written at -08:00, where its wall-clock time is long
// past. Compared as wall-clock times they would be swept the wrong way round. Compared as
// instants, which datetimeoffset against SYSDATETIMEOFFSET() is, the first goes and the second
// stays.
func TestDeleteExpiredPicksTSQLThroughTheRealRepository(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	sweepProbe(t, g, 25)

	east, west := time.FixedZone("UTC+5", 5*3600), time.FixedZone("UTC-8", -8*3600)
	for id, expiry := range map[string]time.Time{
		"expired-east": time.Now().Add(-30 * time.Minute).In(east),
		"live-west":    time.Now().Add(30 * time.Minute).In(west),
	} {
		require.NoError(t, g.Exec(
			"INSERT INTO [sessions] ([id], [user_id], [expiry], [created_at], [last_activity]) VALUES (?, '', ?, ?, ?)",
			id, expiry, time.Now(), time.Now()).Error)
	}

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{"default": mssqlConfig()})
	t.Cleanup(func() { viper.Set("databases", previous) })

	c := service.NewContainer()
	provider.NewDbProvider().Register(c)
	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))
	t.Cleanup(func() { assert.NoError(t, dbContext.GetDataSource(core.DefaultKeyInRegistrar).Close()) })

	repo := &auth.DbSessionRepository{}
	require.NoError(t, c.Make(repo))

	previousBatch := auth.SessionSweepBatchSize
	auth.SessionSweepBatchSize = 10
	t.Cleanup(func() { auth.SessionSweepBatchSize = previousBatch })

	require.NoError(t, repo.DeleteExpired(), "the sweep must run on a SQL Server default")

	var left []string
	require.NoError(t, g.Raw("SELECT [id] FROM [sessions] ORDER BY [id]").Scan(&left).Error)
	assert.Equal(t, []string{"live", "live-west"}, left)
}

// ------------------------------------------------------------------- db:diff

// TestSQLServerDDLRollsBackInATransaction is the property TransactionalDDLDialects lists SQL
// Server for, shown on the statements themselves rather than through db:diff: CREATE TABLE,
// CREATE INDEX, ALTER TABLE … ADD, an added foreign key and a column comment, run in one
// transaction, are all gone after its rollback, and the table that existed before keeps
// exactly the columns it had.
func TestSQLServerDDLRollsBackInATransaction(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"ddl_rollback_child", "ddl_rollback_new", "ddl_rollback_existing"},
		"CREATE TABLE ddl_rollback_existing ([id] bigint IDENTITY(1,1) PRIMARY KEY)")
	before := describeTable(t, g, "ddl_rollback_existing")

	tx := g.Begin()
	require.NoError(t, tx.Error)
	for _, statement := range []string{
		"CREATE TABLE ddl_rollback_new ([id] bigint IDENTITY(1,1) PRIMARY KEY, [name] nvarchar(40) NULL)",
		"CREATE INDEX idx_ddl_rollback_new_name ON ddl_rollback_new ([name])",
		"ALTER TABLE ddl_rollback_existing ADD [label] nvarchar(80) NULL",
		"CREATE TABLE ddl_rollback_child ([id] bigint PRIMARY KEY, [new_id] bigint NULL)",
		"ALTER TABLE ddl_rollback_child ADD CONSTRAINT fk_ddl_rollback_child_new " +
			"FOREIGN KEY ([new_id]) REFERENCES ddl_rollback_new ([id])",
		"EXEC sp_addextendedproperty @name = N'MS_Description', @value = N'rolled back', " +
			"@level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', " +
			"@level1name = N'ddl_rollback_existing', @level2type = N'COLUMN', @level2name = N'label'",
	} {
		if err := tx.Exec(statement).Error; err != nil {
			assert.NoError(t, tx.Rollback().Error)
			require.NoError(t, err, statement)
		}
	}
	require.True(t, tx.Migrator().HasTable("ddl_rollback_new"), "inside the transaction the table exists")
	require.NoError(t, tx.Rollback().Error)

	assert.False(t, g.Migrator().HasTable("ddl_rollback_new"), "CREATE TABLE must have rolled back")
	assert.False(t, g.Migrator().HasTable("ddl_rollback_child"), "and the table with the foreign key")
	assert.Equal(t, before, describeTable(t, g, "ddl_rollback_existing"), "ALTER TABLE … ADD must have rolled back")
}

// The domains the db:diff case drafts. Between them they use what db:diff turns into DDL on
// SQL Server: an IDENTITY key, a bounded string with an index, a unique column, a column
// comment (stored with sp_addextendedproperty, whose arguments the migrator binds), defaults, a
// named check, a has-many foreign key, a column added to a table that exists, and the struct
// column of an extended table.

type diffMssqlAuthor struct {
	ID        uint
	Name      string `gorm:"size:120;index"`
	Email     string `gorm:"unique"`
	Note      string `gorm:"size:200;comment:shown to editors"`
	Score     int    `gorm:"default:0;not null"`
	Active    bool   `gorm:"default:true"`
	CreatedAt time.Time
	Posts     []diffMssqlPost `gorm:"foreignKey:AuthorID"`
}

type diffMssqlPost struct {
	ID       uint
	AuthorID uint
	Title    string `gorm:"size:200;check:chk_diff_mssql_posts_title,title <> ''"`
}

// diffMssqlWidget's table exists before the diff, without the label column.
type diffMssqlWidget struct {
	ID    uint
	Label string `gorm:"size:80"`
}

type diffMssqlParent struct {
	ID   uint
	Kind string `gorm:"size:40"`
}

type diffMssqlChild struct {
	diffMssqlParent `grgorm:"extends"`
	Note            string `gorm:"size:40"`
}

// diffMssqlTables are the case's tables, in an order they can be dropped in.
var diffMssqlTables = []string{"diff_mssql_posts", "diff_mssql_authors", "diff_mssql_children", "diff_mssql_parents", "diff_mssql_widgets"}

// draftedUpStatements returns the statements of the one migration db:diff drafted under dir.
func draftedUpStatements(t *testing.T, dir string) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(dir, dbCmd.MigrationDir, "*.go"))
	require.NoError(t, err)
	require.Len(t, files, 1, "db:diff must draft one migration")

	file, err := parser.ParseFile(token.NewFileSet(), files[0], nil, 0)
	require.NoError(t, err)

	var statements []string
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Up" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok && statements == nil {
				for _, element := range lit.Elts {
					basic, ok := element.(*ast.BasicLit)
					require.True(t, ok, "each statement is a string literal")
					value, err := strconv.Unquote(basic.Value)
					require.NoError(t, err)
					statements = append(statements, value)
				}
				return false
			}
			return true
		})
		return false
	})
	require.NotEmpty(t, statements, "the draft must hold its statements in Up")
	return statements
}

// TestDiffRollsBackAndItsDraftRunsOnSQLServer is the live proof behind putting SQL Server in
// TransactionalDDLDialects, run through the real db:diff: the CREATE TABLE and ALTER TABLE it
// runs to find the differences leave the database as they found it, and the migration it
// drafts then runs on that database, statement by statement, with no arguments, as the draft's
// Up runs them.
func TestDiffRollsBackAndItsDraftRunsOnSQLServer(t *testing.T) {
	ds := mssqlDataSource(t, mssqlConfig())
	g := gormOf(t, ds)

	reset := func() {
		for _, table := range diffMssqlTables {
			assert.NoError(t, g.Exec("DROP TABLE IF EXISTS ["+table+"]").Error)
		}
	}
	reset()
	t.Cleanup(reset)
	require.NoError(t, g.Exec("CREATE TABLE [diff_mssql_widgets] ([id] bigint IDENTITY(1,1) PRIMARY KEY)").Error)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "pkg", "domain"), 0o755))
	t.Chdir(dir)

	previousArgs := os.Args
	os.Args = []string{"cli", "db:diff"}
	t.Cleanup(func() { os.Args = previousArgs })

	previous := viper.Get("databases")
	viper.Set("databases", map[string]any{"default": mssqlConfig()})
	t.Cleanup(func() { viper.Set("databases", previous) })

	c := service.NewContainer()
	provider.NewDbProvider().Register(c)
	var dbContext core.IDBContext
	require.NoError(t, c.Make(&dbContext))
	t.Cleanup(func() { assert.NoError(t, dbContext.GetDataSource(core.DefaultKeyInRegistrar).Close()) })

	domains := &model.DomainContext{}
	for key, domain := range map[string]any{
		"example.com/app/pkg/domain.Author": diffMssqlAuthor{},
		"example.com/app/pkg/domain.Post":   diffMssqlPost{},
		"example.com/app/pkg/domain.Widget": diffMssqlWidget{},
		"example.com/app/pkg/domain.Parent": diffMssqlParent{},
		"example.com/app/pkg/domain.Child":  diffMssqlChild{},
	} {
		domains.RegisterDomain(key, domain)
	}
	require.NoError(t, c.Singleton(func() core.IDomainContext { return domains }))

	cmd := &dbCmd.DiffCommand{}
	require.NoError(t, c.Make(cmd))
	require.NotPanics(t, func() { cmd.Execute(ctxBackground()) }, "db:diff must run on a SQL Server datasource")

	// The recorder went with the diff. It used to stay on the datasource's handle, refusing every
	// later statement there that bound anything but a string.
	diffed, err := dbContext.GetDataSource(core.DefaultKeyInRegistrar).GetDriver()
	require.NoError(t, err)
	assert.NoError(t, diffed.(*gorm.DB).Exec("SELECT 1 WHERE 1 = ?", 1).Error,
		"the datasource db:diff ran on must bind a number again once it returns")

	// Nothing the diff ran is left behind.
	for _, table := range diffMssqlTables[:4] {
		assert.Falsef(t, g.Migrator().HasTable(table), "the diff's CREATE TABLE %s must have rolled back", table)
	}
	dataType, _ := mssqlColumnType(t, g, "diff_mssql_widgets", "label")
	assert.Empty(t, dataType, "the diff's ALTER TABLE must have rolled back")

	statements := draftedUpStatements(t, dir)
	assert.Contains(t, statements, "ALTER TABLE [diff_mssql_parents] ADD [model_struct] nvarchar(255) NULL")

	// The draft runs as its Up would run it.
	tx := g.Begin()
	require.NoError(t, tx.Error)
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			assert.NoError(t, tx.Rollback().Error)
			require.NoError(t, err, "every drafted statement must run on SQL Server: %s", statement)
		}
	}
	require.NoError(t, tx.Commit().Error)

	for _, table := range diffMssqlTables {
		assert.Truef(t, g.Migrator().HasTable(table), "the draft must create %s", table)
	}
	for table, column := range map[string]string{
		"diff_mssql_widgets": "label",
		"diff_mssql_parents": "model_struct",
		"diff_mssql_authors": "note",
	} {
		dataType, _ := mssqlColumnType(t, g, table, column)
		assert.Equalf(t, "nvarchar", dataType, "%s.%s", table, column)
	}

	var comment string
	require.NoError(t, g.Raw(
		"SELECT CAST(value AS nvarchar(4000)) FROM sys.extended_properties "+
			"WHERE major_id = OBJECT_ID(N'diff_mssql_authors') AND name = N'MS_Description' "+
			"AND minor_id = COLUMNPROPERTY(major_id, N'note', 'ColumnId')").Scan(&comment).Error)
	assert.Equal(t, "shown to editors", comment, "the comment's names were drafted inline")

	var foreignKeys int64
	require.NoError(t, g.Raw(
		"SELECT COUNT(*) FROM sys.foreign_keys WHERE parent_object_id = OBJECT_ID(N'diff_mssql_posts') "+
			"AND referenced_object_id = OBJECT_ID(N'diff_mssql_authors')").Scan(&foreignKeys).Error)
	assert.Equal(t, int64(1), foreignKeys, "the has-many foreign key was drafted")
}
