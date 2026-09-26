package guard_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/gorm/guard"
	"github.com/osbits/gorgany/v2/db/sql/gorm/internal/rendered"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// ------------------------------------------------------ a recording SQL driver

// recorder is a fake database/sql driver that logs every statement it receives and answers
// every query with no rows. A guard's refusal has to leave its log empty: a statement it
// refused must not reach the driver at all, not even as the BEGIN of gorm's default
// transaction.
type recorder struct {
	mu  sync.Mutex
	log []string
}

func (r *recorder) Open(string) (driver.Conn, error)             { return &recorderConn{r: r}, nil }
func (r *recorder) Connect(context.Context) (driver.Conn, error) { return &recorderConn{r: r}, nil }
func (r *recorder) Driver() driver.Driver                        { return r }

func (r *recorder) record(statement string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, statement)
}

func (r *recorder) statements() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

type recorderConn struct{ r *recorder }

// Prepare records nothing; the statement records its query each time it runs, as the
// connection does.
func (c *recorderConn) Prepare(query string) (driver.Stmt, error) {
	return &recorderStmt{c: c, query: query}, nil
}
func (c *recorderConn) Close() error { return nil }
func (c *recorderConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *recorderConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.r.record("BEGIN")
	return c, nil
}

func (c *recorderConn) Commit() error   { c.r.record("COMMIT"); return nil }
func (c *recorderConn) Rollback() error { c.r.record("ROLLBACK"); return nil }

func (c *recorderConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.r.record(query)
	return driver.RowsAffected(1), nil
}

func (c *recorderConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.r.record(query)
	return noRows{}, nil
}

type recorderStmt struct {
	c     *recorderConn
	query string
}

func (s *recorderStmt) Close() error  { return nil }
func (s *recorderStmt) NumInput() int { return -1 }

func (s *recorderStmt) Exec([]driver.Value) (driver.Result, error) {
	return s.c.ExecContext(context.Background(), s.query, nil)
}

func (s *recorderStmt) Query([]driver.Value) (driver.Rows, error) {
	return s.c.QueryContext(context.Background(), s.query, nil)
}

type noRows struct{}

func (noRows) Columns() []string         { return []string{"id"} }
func (noRows) Close() error              { return nil }
func (noRows) Next([]driver.Value) error { return io.EOF }

// recordedGorm is a gorm handle, with the Postgres dialect, over a fresh recorder.
func recordedGorm(t *testing.T) (*gorm.DB, *recorder) {
	t.Helper()
	return recordedGormLogging(t, logger.Discard)
}

func recordedGormLogging(t *testing.T, log logger.Interface) (*gorm.DB, *recorder) {
	t.Helper()
	r := &recorder{}
	pool := sql.OpenDB(r)
	t.Cleanup(func() { assert.NoError(t, pool.Close()) })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: log})
	require.NoError(t, err)
	return db, r
}

// warnings is a gorm logger that keeps its warnings and drops everything else.
type warnings struct {
	logger.Interface
	mu   sync.Mutex
	seen []string
}

func (w *warnings) Warn(_ context.Context, format string, args ...interface{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, fmt.Sprintf(format, args...))
}

func (w *warnings) LogMode(logger.LogLevel) logger.Interface { return w }

type widget struct {
	ID   uint
	Name string `gorm:"index"`
	Note string
}

// ------------------------------------------------------------------ read-only

func TestInstallReadOnlyRefusesGormWrites(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))

	var ids []int
	for _, c := range []struct {
		name  string
		write func() error
	}{
		{"Create", func() error { return db.Create(&widget{Name: "a"}).Error }},
		{"Save a new row", func() error { return db.Save(&widget{Name: "a"}).Error }},
		{"Save an existing row", func() error { return db.Save(&widget{ID: 7, Name: "a"}).Error }},
		{"Updates", func() error { return db.Model(&widget{ID: 7}).Updates(map[string]any{"name": "b"}).Error }},
		{"Update", func() error { return db.Model(&widget{ID: 7}).Update("name", "b").Error }},
		{"UpdateColumn", func() error { return db.Model(&widget{ID: 7}).UpdateColumn("name", "b").Error }},
		{"Delete", func() error { return db.Delete(&widget{ID: 7}).Error }},
		{"Exec INSERT", func() error { return db.Exec("INSERT INTO widgets (name) VALUES (?)", "a").Error }},
		{"Exec UPDATE", func() error { return db.Exec("UPDATE widgets SET name = ? WHERE id = ?", "b", 7).Error }},
		{"Exec DELETE", func() error { return db.Exec("DELETE FROM widgets WHERE id = ?", 7).Error }},
		{"Exec DDL", func() error { return db.Exec("DROP TABLE widgets").Error }},
		{"a write through Raw and Scan", func() error { return db.Raw("DELETE FROM widgets RETURNING id").Scan(&ids).Error }},
		{"a data-modifying CTE through Raw", func() error {
			return db.Raw("WITH gone AS (DELETE FROM widgets RETURNING id) SELECT id FROM gone").Scan(&ids).Error
		}},
		{"a DryRun Create", func() error { return db.Session(&gorm.Session{DryRun: true}).Create(&widget{Name: "a"}).Error }},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.ErrorIs(t, c.write(), dbCore.ErrReadOnly)
		})
	}
	assert.Empty(t, r.statements(), "a refused write must not reach the database, not even as a BEGIN")

	// CreateInBatches opens a transaction of its own around the batches, before any callback
	// runs; the first batch is then refused and the transaction rolled back, with nothing
	// written in between.
	assert.ErrorIs(t, db.CreateInBatches([]widget{{Name: "a"}, {Name: "b"}}, 1).Error, dbCore.ErrReadOnly)
	assert.Equal(t, []string{"BEGIN", "ROLLBACK"}, r.statements())
}

func TestInstallReadOnlyAllowsReads(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))

	var (
		widgets []widget
		one     widget
		names   []string
		count   int64
	)
	require.NoError(t, db.Find(&widgets).Error)
	require.NoError(t, db.Where("name = ?", "a").Find(&widgets).Error)
	require.NoError(t, db.Where("lock = ?", 1).Find(&widgets).Error,
		"a query gorm builds from a model is not checked word by word")
	assert.ErrorIs(t, db.First(&one).Error, gorm.ErrRecordNotFound, "the recorder has no rows; what matters is that it ran")
	require.NoError(t, db.Model(&widget{}).Count(&count).Error)
	require.NoError(t, db.Model(&widget{}).Pluck("name", &names).Error)
	require.NoError(t, db.Raw("SELECT name FROM widgets WHERE id = ?", 7).Scan(&names).Error)
	require.NoError(t, db.Raw("WITH recent AS (SELECT id FROM widgets) SELECT id FROM recent").Scan(&names).Error)
	require.NoError(t, db.Exec("SELECT 1").Error)

	rows, err := db.Raw("SELECT id FROM widgets").Rows()
	require.NoError(t, err)
	assert.NoError(t, rows.Close())

	assert.Len(t, r.statements(), 10, "every read reaches the database: %q", r.statements())
}

func TestTheGuardUsesTheShapeCheckOnlyForDialectRenderedSQL(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))

	const keywordColumns = "SELECT lock, copy FROM widgets"
	var ids []int

	assert.ErrorIs(t, db.Raw(keywordColumns).Scan(&ids).Error, dbCore.ErrReadOnly,
		"unmarked SQL gets the deep check, which refuses the words wherever they stand")
	assert.Empty(t, r.statements())

	require.NoError(t, rendered.Mark(db).Raw(keywordColumns).Scan(&ids).Error,
		"marked SQL gets the shape check, which leaves keyword-named columns alone")
	assert.Equal(t, []string{keywordColumns}, r.statements())

	assert.ErrorIs(t, rendered.Mark(db).Raw("SELECT id FROM widgets; DELETE FROM widgets").Scan(&ids).Error,
		dbCore.ErrReadOnly, "the shape check still refuses a second statement")
	assert.ErrorIs(t, rendered.Mark(db).Raw("WITH gone AS (DELETE FROM widgets RETURNING id) SELECT id FROM gone").Scan(&ids).Error,
		dbCore.ErrReadOnly, "and a data-modifying CTE")
	assert.ErrorIs(t, rendered.Mark(db.WithContext(context.Background())).Exec("DELETE FROM widgets").Error,
		dbCore.ErrReadOnly, "and a statement that is not a read")

	assert.ErrorIs(t, db.Raw(keywordColumns).Scan(&ids).Error, dbCore.ErrReadOnly,
		"marking a handle must not mark the handle it came from")
	assert.Equal(t, []string{keywordColumns}, r.statements())
}

// TestTheDialectMarkCannotBeForgedOrInherited: the mark gives a statement the lighter shape
// check, so only gorgany's engines can set it, and only on the statement they run. An app
// cannot set it with gorm's Set, under the key it once had, and a Session or Transaction
// derived from a marked handle does not inherit it, although gorm copies a statement's
// settings into the statements it derives from it.
func TestTheDialectMarkCannotBeForgedOrInherited(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))

	const keywordColumns = "SELECT lock FROM widgets"
	var ids []int
	ctx := context.Background()

	for _, key := range []string{"gorgany:dialect_rendered", "rendered", "mark"} {
		assert.ErrorIs(t, db.Set(key, true).Raw(keywordColumns).Scan(&ids).Error, dbCore.ErrReadOnly,
			"db.Set(%q) must not mark a statement", key)
		assert.ErrorIs(t, db.InstanceSet(key, true).Raw(keywordColumns).Scan(&ids).Error, dbCore.ErrReadOnly,
			"db.InstanceSet(%q) must not mark a statement", key)
	}

	marked := rendered.Mark(db.WithContext(ctx))
	assert.ErrorIs(t, marked.Session(&gorm.Session{}).Raw(keywordColumns).Scan(&ids).Error, dbCore.ErrReadOnly,
		"a Session derived from a marked handle is not marked")
	assert.ErrorIs(t, rendered.Mark(db.WithContext(ctx)).WithContext(ctx).Raw(keywordColumns).Scan(&ids).Error, dbCore.ErrReadOnly,
		"nor is a handle WithContext derives from one")
	err := rendered.Mark(db.WithContext(ctx)).Transaction(func(tx *gorm.DB) error {
		return tx.Raw(keywordColumns).Scan(&ids).Error
	})
	assert.ErrorIs(t, err, dbCore.ErrReadOnly, "nor is a Transaction begun from one")
	assert.Equal(t, []string{"BEGIN", "ROLLBACK"}, r.statements(), "only the transaction's own boundaries reach the database")

	require.NoError(t, rendered.Mark(db.WithContext(ctx)).Raw(keywordColumns).Scan(&ids).Error,
		"the statement Mark marked is still marked")
}

// TestTheShapeCheckOnSQLServerFindsASecondStatementWithoutASemicolon: SQL Server runs
// SELECT … DELETE … as two statements, and a dialect-rendered statement carries an app's raw
// fragments, so under its lexicon a marked statement is read word by word too.
func TestTheShapeCheckOnSQLServerFindsASecondStatementWithoutASemicolon(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconTSQL))

	var ids []int
	ctx := context.Background()
	for _, sql := range []string{
		"SELECT id FROM widgets DELETE FROM widgets",
		"SELECT id FROM widgets UPDATE widgets SET name = 'x' EXEC sp_who",
		"SELECT 1 INSERT widgets VALUES (1)",
		"SELECT NEXT VALUE FOR widget_ids",
	} {
		assert.ErrorIs(t, db.Raw(sql).Scan(&ids).Error, dbCore.ErrReadOnly, "%q unmarked", sql)
		assert.ErrorIs(t, rendered.Mark(db.WithContext(ctx)).Raw(sql).Scan(&ids).Error, dbCore.ErrReadOnly, "%q marked", sql)
	}
	assert.Empty(t, r.statements())

	require.NoError(t, rendered.Mark(db.WithContext(ctx)).Raw("SELECT TOP (1) [Id] FROM [dbo].[2024Orders]").Scan(&ids).Error)
	assert.Len(t, r.statements(), 1)
}

// TestInstallReadOnlyChecksWhatGormBuildsAsItIsSent: a query gorm builds from a model has no
// SQL when the callbacks run, and what an app adds to it reaches the server as written. The
// pool checks it as it is sent, with the shape check: a second statement, an INTO, a locking
// clause or a data-modifying CTE in a fragment is refused, while a keyword-named column is
// not.
func TestInstallReadOnlyChecksWhatGormBuildsAsItIsSent(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))

	var (
		ws   []widget
		rows []map[string]any
	)
	for _, c := range []struct {
		name  string
		query func() error
	}{
		{"Select with INTO", func() error { return db.Select("* INTO widgets_copy").Table("widgets").Find(&rows).Error }},
		{"Select with INTO through Rows", func() error {
			_, err := db.Table("widgets").Select("* INTO widgets_copy").Rows()
			return err
		}},
		{"a second statement in Where", func() error { return db.Where("1=1; DELETE FROM widgets").Find(&ws).Error }},
		{"a second statement in Clauses", func() error {
			return db.Clauses(clause.Expr{SQL: "1=1; DELETE FROM widgets"}).Find(&ws).Error
		}},
		{"a second statement in Table", func() error { return db.Table("widgets; DELETE FROM widgets --").Find(&rows).Error }},
		{"a data-modifying CTE in Table", func() error {
			return db.Table("(WITH gone AS (DELETE FROM widgets RETURNING id) SELECT id FROM gone) AS w").Find(&rows).Error
		}},
		{"clause.Locking", func() error { return db.Clauses(clause.Locking{Strength: "UPDATE"}).Find(&ws).Error }},
		{"FOR SHARE in Clauses", func() error { return db.Clauses(clause.Locking{Strength: "SHARE"}).Find(&ws).Error }},
		{"Count with INTO", func() error {
			var n int64
			return db.Model(&widget{}).Select("count(*) INTO n").Count(&n).Error
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.ErrorIs(t, c.query(), dbCore.ErrReadOnly)
		})
	}
	assert.Empty(t, r.statements(), "a statement refused as it is sent never reaches the database")

	require.NoError(t, db.Where("lock = ? AND copy = ?", 1, 2).Find(&ws).Error,
		"the shape check leaves keyword-named columns in a fragment alone")
	assert.Len(t, r.statements(), 1)
}

// TestInstallReadOnlyOnSQLServerChecksEveryWordOfWhatGormBuilds: there, a fragment can add a
// statement with no ";" before it.
func TestInstallReadOnlyOnSQLServerChecksEveryWordOfWhatGormBuilds(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconTSQL))

	var ws []widget
	assert.ErrorIs(t, db.Where("1=1 DELETE FROM widgets").Find(&ws).Error, dbCore.ErrReadOnly)
	assert.ErrorIs(t, db.Select("id UPDATE widgets SET name = 'x' --").Find(&ws).Error, dbCore.ErrReadOnly)
	assert.Empty(t, r.statements())
	require.NoError(t, db.Where("[name] = ?", "a").Find(&ws).Error)
	assert.Len(t, r.statements(), 1)
}

// TestInstallReadOnlyChecksWhatIsSentOnThePoolDirectly: the Postgres and MySQL executors'
// ExecInsert sends on the statement's pool, past gorm's callbacks, to read the driver's
// result. The pool is guarded, on the handle and in a transaction begun from it.
func TestInstallReadOnlyChecksWhatIsSentOnThePoolDirectly(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))
	ctx := context.Background()

	_, err := db.WithContext(ctx).Statement.ConnPool.ExecContext(ctx, "INSERT INTO widgets (name) VALUES ($1)", "a")
	assert.ErrorIs(t, err, dbCore.ErrReadOnly)
	_, err = db.ConnPool.ExecContext(ctx, "DELETE FROM widgets")
	assert.ErrorIs(t, err, dbCore.ErrReadOnly, "the handle's own pool is the guarded one too")
	_, err = db.ConnPool.PrepareContext(ctx, "UPDATE widgets SET name = 'x'")
	assert.ErrorIs(t, err, dbCore.ErrReadOnly)

	tx := db.Begin()
	require.NoError(t, tx.Error)
	_, err = tx.Statement.ConnPool.ExecContext(ctx, "INSERT INTO widgets (name) VALUES ($1)", "a")
	assert.ErrorIs(t, err, dbCore.ErrReadOnly, "and so is a transaction's")
	sqlDB, err := tx.DB()
	require.NoError(t, err, "gorm still finds the *sql.DB behind a guarded transaction")
	assert.NotNil(t, sqlDB)
	require.NoError(t, tx.Rollback().Error)

	assert.Equal(t, []string{"BEGIN", "ROLLBACK"}, r.statements())
}

// TestARefusedRowReportsTheRefusal: gorm's Row() hands back the *sql.Row its callbacks leave,
// and a refused statement leaves none. The guard leaves one that reports the refusal, so
// Scan and Err return an error errors.Is can tell apart instead of panicking on nil.
func TestARefusedRowReportsTheRefusal(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))

	row := db.Raw("DELETE FROM widgets RETURNING id").Row()
	require.NotNil(t, row)
	var id int
	assert.ErrorIs(t, row.Scan(&id), dbCore.ErrReadOnly)
	assert.ErrorIs(t, row.Err(), dbCore.ErrReadOnly)

	row = db.Table("widgets").Select("id INTO widgets_copy").Row()
	require.NotNil(t, row, "nor when the pool refuses a statement gorm built")
	assert.ErrorIs(t, row.Scan(&id), dbCore.ErrReadOnly)

	_, err := db.Raw("DELETE FROM widgets RETURNING id").Rows()
	assert.ErrorIs(t, err, dbCore.ErrReadOnly, "Rows returns the refusal as its error")
	assert.Empty(t, r.statements())

	external, r2 := recordedGorm(t)
	require.NoError(t, guard.InstallExternalSchema(external, dbCore.LexiconPostgres))
	row = external.Raw("SELECT * INTO widgets_copy FROM widgets").Row()
	require.NotNil(t, row)
	assert.ErrorIs(t, row.Scan(&id), dbCore.ErrExternalSchema)
	assert.Empty(t, r2.statements())

	assert.ErrorIs(t, db.Raw("SELECT id FROM widgets").Row().Scan(&id), sql.ErrNoRows,
		"a Row that is not refused is gorm's own")
}

// TestANestedTransactionStillWorksOnAReadOnlyHandle pins the one statement the read-only
// guard lets through unchecked. gorm sends SAVEPOINT for a nested Transaction, which writes
// nothing and which a read-only transaction accepts; refusing it would break any service
// that wraps its reads in a transaction and calls another that does too.
func TestANestedTransactionStillWorksOnAReadOnlyHandle(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))

	var names []string
	err := db.Transaction(func(tx *gorm.DB) error {
		return tx.Transaction(func(inner *gorm.DB) error {
			return inner.Raw("SELECT name FROM widgets").Scan(&names).Error
		})
	})
	require.NoError(t, err)

	log := strings.Join(r.statements(), "\n")
	assert.Contains(t, log, "SAVEPOINT sp", "the savepoint must reach the database: %q", r.statements())
	assert.Contains(t, log, "SELECT name FROM widgets")

	assert.ErrorIs(t, db.Exec("SAVEPOINT sp1; DELETE FROM widgets").Error, dbCore.ErrReadOnly,
		"only a savepoint exactly as gorm writes one is exempt")
}

// ------------------------------------------------------------ external schema

func TestInstallExternalSchemaRefusesMigratorDDL(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconPostgres))

	dryRun := db.Session(&gorm.Session{DryRun: true})
	for _, c := range []struct {
		name string
		ddl  func() error
	}{
		{"CreateTable", func() error { return db.Migrator().CreateTable(&widget{}) }},
		{"AddColumn", func() error { return db.Migrator().AddColumn(&widget{}, "Note") }},
		{"RenameColumn", func() error { return db.Migrator().RenameColumn(&widget{}, "note", "remark") }},
		{"DropColumn", func() error { return db.Migrator().DropColumn(&widget{}, "Note") }},
		{"DropTable", func() error { return db.Migrator().DropTable(&widget{}) }},
		{"RenameTable", func() error { return db.Migrator().RenameTable("widgets", "gadgets") }},
		{"CreateIndex", func() error { return db.Migrator().CreateIndex(&widget{}, "idx_widgets_name") }},
		{"a DryRun CreateTable", func() error { return dryRun.Migrator().CreateTable(&widget{}) }},
		{"a DryRun AddColumn", func() error { return dryRun.Migrator().AddColumn(&widget{}, "Note") }},
		{"a DryRun RenameColumn", func() error { return dryRun.Migrator().RenameColumn(&widget{}, "note", "remark") }},
		{"Exec DDL", func() error { return db.Exec("ALTER TABLE widgets ADD COLUMN note text").Error }},
		{"SELECT INTO through Raw and Scan", func() error {
			var ids []int
			return db.Raw("SELECT * INTO widgets_copy FROM widgets").Scan(&ids).Error
		}},
		// gorm's Create, Update and Delete send SQL set with Raw verbatim, instead of
		// building their own.
		{"DDL through Raw and Create", func() error { return db.Raw("DROP TABLE widgets").Create(&widget{Name: "a"}).Error }},
		{"DDL through Raw and Update", func() error {
			return db.Model(&widget{ID: 7}).Raw("DROP TABLE widgets").Update("name", "b").Error
		}},
		{"DDL through Raw and Delete", func() error { return db.Raw("DROP TABLE widgets").Delete(&widget{ID: 7}).Error }},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.ErrorIs(t, c.ddl(), dbCore.ErrExternalSchema)
		})
	}
	for _, statement := range r.statements() {
		assert.True(t, strings.HasPrefix(statement, "SELECT"),
			"only the migrator's catalogue reads may reach the database, got %q", statement)
	}
}

func TestInstallExternalSchemaAllowsDataChanges(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconPostgres))

	var widgets []widget
	require.NoError(t, db.Create(&widget{Name: "a"}).Error)
	require.NoError(t, db.Model(&widget{ID: 7}).Updates(map[string]any{"name": "b"}).Error)
	require.NoError(t, db.Save(&widget{ID: 7, Name: "c"}).Error)
	require.NoError(t, db.Delete(&widget{ID: 7}).Error)
	require.NoError(t, db.Find(&widgets).Error)
	require.NoError(t, db.Exec("UPDATE widgets SET note = 'CREATE TABLE x' WHERE id = ?", 7).Error)
	require.NoError(t, db.Raw("INSERT INTO widgets (name) VALUES (?)", "d").Create(&widget{}).Error,
		"SQL set with Raw is checked, not refused, on the write processors too")

	// Each write reaches the database, in gorm's default transaction where gorm opens one.
	assert.Equal(t, []string{
		"BEGIN", `INSERT INTO "widgets" ("name","note") VALUES ($1,$2) RETURNING "id"`, "COMMIT",
		"BEGIN", `UPDATE "widgets" SET "name"=$1 WHERE "id" = $2`, "COMMIT",
		"BEGIN", `UPDATE "widgets" SET "name"=$1,"note"=$2 WHERE "id" = $3`, "COMMIT",
		"BEGIN", `DELETE FROM "widgets" WHERE "widgets"."id" = $1`, "COMMIT",
		`SELECT * FROM "widgets"`,
		"UPDATE widgets SET note = 'CREATE TABLE x' WHERE id = $1",
		"BEGIN", "INSERT INTO widgets (name) VALUES ($1)", "COMMIT",
	}, r.statements())
}

// TestInstallExternalSchemaTakesTheEnginesLexicon runs SQL Server's rules through the same
// Postgres-dialect handle: the lexicon, not the dialector, decides how the text reads.
func TestInstallExternalSchemaTakesTheEnginesLexicon(t *testing.T) {
	db, _ := recordedGorm(t)
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconTSQL))

	assert.NoError(t, db.Exec("CREATE TABLE #scratch (Id int)").Error, "a temp table is the session's, not the schema's")
	assert.NoError(t, db.Exec("DROP TABLE #scratch").Error)
	assert.ErrorIs(t, db.Exec("EXEC sp_rename @objname = ?, @newname = ?, @objtype = 'COLUMN'", "widgets.note", "remark").Error,
		dbCore.ErrExternalSchema)
	assert.ErrorIs(t, db.Exec("EXEC sp_updateextendedproperty 'MS_Description', 'x'").Error, dbCore.ErrExternalSchema)
	assert.ErrorIs(t, db.Exec("DROP TABLE [dbo].[2024Orders], #scratch").Error, dbCore.ErrExternalSchema)

	var ids []int
	assert.ErrorIs(t, db.Raw("SELECT * INTO dbo.Copy FROM widgets").Scan(&ids).Error, dbCore.ErrExternalSchema)
	assert.NoError(t, db.Raw("SELECT * INTO #copy FROM widgets").Scan(&ids).Error)
}

// TestInstallExternalSchemaChecksWhatGormBuildsAsItIsSent: gorm builds a statement from a
// model, and the fragments an app adds to it, in the callback that sends it, so what those
// fragments add reaches the server as written unless the pool checks it as it is sent.
func TestInstallExternalSchemaChecksWhatGormBuildsAsItIsSent(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconPostgres))

	var (
		ws   []widget
		rows []map[string]any
	)
	for _, c := range []struct {
		name string
		ddl  func() error
	}{
		{"Select with INTO", func() error { return db.Select("* INTO widgets_copy").Table("widgets").Find(&rows).Error }},
		{"Select with INTO through Rows", func() error {
			_, err := db.Table("widgets").Select("* INTO widgets_copy2").Rows()
			return err
		}},
		{"DDL in Where", func() error { return db.Where("1=1; DROP TABLE widgets").Find(&ws).Error }},
		{"DDL in Clauses", func() error { return db.Clauses(clause.Expr{SQL: "1=1; DROP TABLE widgets"}).Find(&ws).Error }},
		{"DDL in Table", func() error { return db.Table("widgets; DROP TABLE widgets --").Find(&rows).Error }},
		{"DDL in Joins", func() error { return db.Joins("; DROP TABLE widgets --").Find(&ws).Error }},
		{"DDL in Order", func() error { return db.Order("id; DROP TABLE widgets").Find(&ws).Error }},
		{"DDL in gorm.Expr", func() error { return db.Where("id = ?", gorm.Expr("1; DROP TABLE widgets")).Find(&ws).Error }},
		{"DDL in a transaction", func() error {
			return db.Transaction(func(tx *gorm.DB) error { return tx.Where("1=1; DROP TABLE widgets").Find(&ws).Error })
		}},
		{"DDL on the connection db.Connection pins", func() error {
			return db.Connection(func(conn *gorm.DB) error { return conn.Where("1=1; DROP TABLE widgets").Find(&ws).Error })
		}},
		{"DDL sent on the pool directly", func() error {
			_, err := db.Statement.ConnPool.ExecContext(context.Background(), "DROP TABLE widgets")
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			assert.ErrorIs(t, c.ddl(), dbCore.ErrExternalSchema)
		})
	}
	for _, statement := range r.statements() {
		assert.Contains(t, []string{"BEGIN", "ROLLBACK"}, statement,
			"only a transaction's own boundaries may reach the database, got %q", statement)
	}

	// A write gorm builds is checked as it is sent too. gorm opened its default transaction
	// for it before the pool refused it, and rolls that back.
	before := len(r.statements())
	assert.ErrorIs(t, db.Table("widgets; DROP TABLE widgets --").Create(map[string]any{"name": "a"}).Error, dbCore.ErrExternalSchema)
	assert.Equal(t, []string{"BEGIN", "ROLLBACK"}, r.statements()[before:])
}

func TestInstallExternalSchemaOnSQLServerChecksWhatGormBuilds(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconTSQL))

	var ws []widget
	assert.ErrorIs(t, db.Where("1=1 DROP TABLE widgets").Find(&ws).Error, dbCore.ErrExternalSchema,
		"SQL Server needs no ';' before the DROP")
	assert.Empty(t, r.statements())
}

// TestTheGuardCoversAHandleDerivedBeforeTheInstall: gorm keeps callbacks per gorm.Open, so a
// handle derived before the install gets them, and the pool its statements carry is guarded
// when they reach them.
func TestTheGuardCoversAHandleDerivedBeforeTheInstall(t *testing.T) {
	db, r := recordedGorm(t)
	// WithContext gives the handle a statement of its own, and with it the unguarded pool.
	early := db.WithContext(context.Background())
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconPostgres))

	var ws []widget
	assert.ErrorIs(t, early.Where("1=1; DROP TABLE widgets").Find(&ws).Error, dbCore.ErrExternalSchema)
	assert.ErrorIs(t, early.Transaction(func(tx *gorm.DB) error {
		return tx.Where("1=1; DROP TABLE widgets").Find(&ws).Error
	}), dbCore.ErrExternalSchema)
	assert.Equal(t, []string{"BEGIN", "ROLLBACK"}, r.statements())
}

// TestTheGuardChecksWhatALaterCallbackSends: gorm puts a Before("*") callback registered
// after the install ahead of the guard's, so the guard's callback has not run when it does.
// What it sends goes out on the guarded pool all the same.
func TestTheGuardChecksWhatALaterCallbackSends(t *testing.T) {
	db, r := recordedGorm(t)
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconPostgres))

	var sent error
	require.NoError(t, db.Callback().Raw().Before("*").Register("test:later", func(tx *gorm.DB) {
		_, sent = tx.Statement.ConnPool.ExecContext(tx.Statement.Context, tx.Statement.SQL.String())
	}))

	assert.ErrorIs(t, db.Exec("DROP TABLE widgets").Error, dbCore.ErrExternalSchema)
	assert.ErrorIs(t, sent, dbCore.ErrExternalSchema, "the later callback's own send is refused by the pool")
	assert.Empty(t, r.statements())
}

// TestAMySQLDropTableLeavesNoSessionChangeBehind: gorm's MySQL Migrator().DropTable pins a
// connection, switches foreign_key_checks off, drops, and switches it back on. Refusing only
// the DROP would return that connection to the pool with the owner's constraints off, for
// the next write on it; the guard refuses the first statement instead.
func TestAMySQLDropTableLeavesNoSessionChangeBehind(t *testing.T) {
	r := &recorder{}
	pool := sql.OpenDB(r)
	pool.SetMaxOpenConns(1)
	t.Cleanup(func() { assert.NoError(t, pool.Close()) })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconMySQL))

	err = db.Migrator().DropTable(&widget{})
	assert.ErrorIs(t, err, dbCore.ErrExternalSchema)
	assert.ErrorContains(t, err, "SET FOREIGN_KEY_CHECKS statement refused")
	assert.Empty(t, r.statements())
}

// TestTheGuardWorksWithPreparedStatements: with PrepareStmt gorm sends through a
// prepared-statement layer of its own, which it finds by type — it unwraps it for a
// SAVEPOINT, which cannot be prepared — so the guard guards the pool inside it.
func TestTheGuardWorksWithPreparedStatements(t *testing.T) {
	open := func(t *testing.T, prepared bool) (*gorm.DB, *recorder) {
		r := &recorder{}
		pool := sql.OpenDB(r)
		t.Cleanup(func() { assert.NoError(t, pool.Close()) })
		db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Discard, PrepareStmt: prepared})
		require.NoError(t, err)
		return db, r
	}
	var ws []widget

	readOnly, r := open(t, false)
	require.NoError(t, guard.InstallReadOnly(readOnly, dbCore.LexiconPostgres))
	assert.ErrorIs(t, readOnly.Session(&gorm.Session{PrepareStmt: true}).Where("1=1; DELETE FROM widgets").Find(&ws).Error,
		dbCore.ErrReadOnly, "a Session that turns PrepareStmt on prepares on the guarded pool")
	assert.Empty(t, r.statements())

	db, r := open(t, true)
	require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconPostgres))
	assert.ErrorIs(t, db.Where("1=1; DROP TABLE widgets").Find(&ws).Error, dbCore.ErrExternalSchema)
	assert.Empty(t, r.statements())

	err := db.Transaction(func(tx *gorm.DB) error {
		return tx.Transaction(func(inner *gorm.DB) error {
			if err := inner.Find(&ws).Error; err != nil {
				return err
			}
			return inner.Where("1=1; DROP TABLE widgets").Find(&ws).Error
		})
	})
	assert.ErrorIs(t, err, dbCore.ErrExternalSchema, "a transaction's statements are checked as they are sent")
	log := strings.Join(r.statements(), "\n")
	assert.Contains(t, log, "SAVEPOINT sp", "a nested transaction still takes its savepoint: %q", r.statements())
	assert.Contains(t, log, "ROLLBACK TO SAVEPOINT sp", "and rolls back to it: %q", r.statements())
	assert.Contains(t, log, `SELECT * FROM "widgets"`)
	assert.NotContains(t, log, "DROP")
}

// ------------------------------------------------------------------ installing

func TestInstallIsIdempotent(t *testing.T) {
	warned := &warnings{Interface: logger.Discard}
	db, r := recordedGormLogging(t, warned)
	for i := 0; i < 2; i++ {
		require.NoError(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))
		require.NoError(t, guard.InstallExternalSchema(db, dbCore.LexiconPostgres))
	}
	require.NoError(t, guard.InstallReadOnly(db.Session(&gorm.Session{}), dbCore.LexiconPostgres),
		"a handle that shares db's callbacks is the same install")

	err := db.Create(&widget{Name: "a"}).Error
	require.ErrorIs(t, err, dbCore.ErrReadOnly)
	assert.Equal(t, 1, strings.Count(err.Error(), "refused"), "a second install must not register a second callback: %v", err)

	err = db.Exec("DROP TABLE widgets").Error
	require.Error(t, err)
	assert.Equal(t, 1, strings.Count(err.Error(), "refused"),
		"one refusal per statement, so errors.Is finds it; gorm keeps only the last of several errors unwrapped: %v", err)
	// gorm puts each Before("*") callback ahead of those already there, so the guard
	// installed last, external_schema's, runs first, and the read-only guard does nothing to
	// a statement that already failed.
	assert.ErrorIs(t, err, dbCore.ErrExternalSchema)
	assert.NotErrorIs(t, err, dbCore.ErrReadOnly)

	assert.Empty(t, r.statements())
	assert.Empty(t, warned.seen, "gorm keeps one callback per name but warns about each duplicate; a second install must not register one")
}

func TestInstallRefusesAHandleWithoutCallbacks(t *testing.T) {
	for _, db := range []*gorm.DB{nil, {}, {Config: &gorm.Config{}}} {
		assert.Error(t, guard.InstallReadOnly(db, dbCore.LexiconPostgres))
		assert.Error(t, guard.InstallExternalSchema(db, dbCore.LexiconPostgres))
	}
}
