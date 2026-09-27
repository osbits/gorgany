//go:build livedb

package e2e

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/db/orm"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// m2mBadge is keyed by a string column that is not called id, and assigned by the caller.
type m2mBadge struct {
	orm.BaseEntity
	Code  string `gorm:"column:code;primaryKey"`
	Label string `gorm:"column:label"`
}

func (m2mBadge) TableName() string { return "m2m_badges" }

type m2mHolder struct {
	orm.BaseEntity
	ID     int64       `gorm:"column:id;primaryKey;autoIncrement"`
	Name   string      `gorm:"column:name"`
	Badges []*m2mBadge `gorm:"many2many:m2m_holder_badges;"`
}

func (m2mHolder) TableName() string { return "m2m_holders" }

func createM2MSchema(t *testing.T, g *gorm.DB, dialect string) {
	t.Helper()
	drop := func() {
		for _, table := range []string{"m2m_holder_badges", "m2m_holders", "m2m_badges"} {
			assert.NoError(t, g.Exec("DROP TABLE IF EXISTS "+table).Error)
		}
	}
	drop()
	t.Cleanup(drop)

	autoPK := "BIGSERIAL PRIMARY KEY"
	switch dialect {
	case "mysql":
		autoPK = "BIGINT AUTO_INCREMENT PRIMARY KEY"
	case "sqlserver":
		autoPK = "BIGINT IDENTITY(1,1) PRIMARY KEY"
	}
	for _, statement := range []string{
		"CREATE TABLE m2m_badges (code VARCHAR(20) NOT NULL PRIMARY KEY, label VARCHAR(50) NOT NULL)",
		fmt.Sprintf("CREATE TABLE m2m_holders (id %s, name VARCHAR(50) NOT NULL)", autoPK),
		"CREATE TABLE m2m_holder_badges (m2m_holder_id BIGINT NOT NULL, m2m_badge_code VARCHAR(20) NOT NULL, " +
			"PRIMARY KEY (m2m_holder_id, m2m_badge_code))",
	} {
		require.NoError(t, g.Exec(statement).Error, "DDL: %s", statement)
	}
}

func badgeCodes(badges []*m2mBadge) []string {
	codes := make([]string, 0, len(badges))
	for _, badge := range badges {
		codes = append(codes, badge.Code)
	}
	sort.Strings(codes)
	return codes
}

// TestManyToManyOnARelatedKeyThatIsNotIDOnEveryEngine: a many-to-many relation to a table
// keyed by a column other than id is saved, loaded and rewritten through the ORM on every
// engine. The load joined on a column called id, which this table does not have; it now joins
// on the key the join row references, through the dialect, so SQL Server brackets the names.
// The related rows' keys are assigned by the caller, which Create reads back from the entity.
func TestManyToManyOnARelatedKeyThatIsNotIDOnEveryEngine(t *testing.T) {
	for _, engine := range ormEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			g := gormOf(t, engine.ds)
			createM2MSchema(t, g, engine.name)
			session, err := engine.ds.NewSession()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, session.Close()) })

			badges := orm.New[*m2mBadge](session)
			for _, code := range []string{"a", "b", "c"} {
				badge := &m2mBadge{Code: code, Label: "badge " + code}
				require.NoError(t, badges.Create(badge), "a key the caller assigned")
				assert.Equal(t, code, badge.Code)
			}

			holders := orm.New[*m2mHolder](session)
			holder := &m2mHolder{Name: "h", Badges: []*m2mBadge{{Code: "a"}, {Code: "b"}}}
			require.NoError(t, holders.Create(holder))
			require.NotZero(t, holder.ID)

			loaded, err := holders.Find(holder.ID)
			require.NoError(t, err)
			require.NotNil(t, loaded)
			require.NoError(t, holders.LoadRelation(loaded, "Badges"))
			assert.Equal(t, []string{"a", "b"}, badgeCodes(loaded.Badges))
			for _, badge := range loaded.Badges {
				assert.Equal(t, "badge "+badge.Code, badge.Label, "the related rows are read, not only their keys")
			}

			loaded.Badges = []*m2mBadge{{Code: "b"}, {Code: "c"}}
			require.NoError(t, holders.Update(loaded))

			reloaded, err := holders.Find(holder.ID)
			require.NoError(t, err)
			require.NoError(t, holders.LoadRelation(reloaded, "Badges"))
			assert.Equal(t, []string{"b", "c"}, badgeCodes(reloaded.Badges), "the stale join row is deleted, the kept ones stay")

			var joinRows int64
			require.NoError(t, g.Raw("SELECT COUNT(*) FROM m2m_holder_badges").Scan(&joinRows).Error)
			assert.Equal(t, int64(2), joinRows)
		})
	}
}

// verifyProbe breaks three of the rules VerifyModel checks, the ones every engine's migrator
// can answer: a NULLable column in a string, a server default Create overwrites, and a column
// the table does not have.
type verifyProbe struct {
	orm.BaseEntity
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Name      string    `gorm:"column:name"`
	Nickname  string    `gorm:"column:nickname"`
	CreatedAt time.Time `gorm:"column:created_at"`
	Missing   string    `gorm:"column:missing"`
}

func (verifyProbe) TableName() string { return "verify_probe" }

// TestVerifyModelOnEveryEngine: VerifyModel reads the columns through each engine's own gorm
// migrator, by table name, and finds the same problems on all three. Only SQL Server reports
// table traits; the checks here need none.
func TestVerifyModelOnEveryEngine(t *testing.T) {
	for _, engine := range ormEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			g := gormOf(t, engine.ds)
			drop := func() { assert.NoError(t, g.Exec("DROP TABLE IF EXISTS verify_probe").Error) }
			drop()
			t.Cleanup(drop)

			ddl := "CREATE TABLE verify_probe (id BIGSERIAL PRIMARY KEY, name VARCHAR(50) NOT NULL, " +
				"nickname VARCHAR(50) NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)"
			switch engine.name {
			case "mysql":
				ddl = "CREATE TABLE verify_probe (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(50) NOT NULL, " +
					"nickname VARCHAR(50) NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)"
			case "sqlserver":
				ddl = "CREATE TABLE verify_probe (id BIGINT IDENTITY(1,1) PRIMARY KEY, name NVARCHAR(50) NOT NULL, " +
					"nickname NVARCHAR(50) NULL, created_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME())"
			}
			require.NoError(t, g.Exec(ddl).Error)

			session, err := engine.ds.NewSession()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, session.Close()) })

			problems, err := orm.VerifyModel(context.Background(), session, &verifyProbe{})
			require.NoError(t, err)
			kinds := make([]string, len(problems))
			for i, problem := range problems {
				kinds[i] = problem.Column + " " + problem.Kind
			}
			assert.Equal(t, []string{
				"nickname " + orm.ProblemNullableIntoValue,
				"created_at " + orm.ProblemServerDefaultWritten,
				"missing " + orm.ProblemMissingColumn,
			}, kinds)
		})
	}
}

// zeroKeyStatus's key is assigned by the caller, and 0 is one of its values.
type zeroKeyStatus struct {
	orm.BaseEntity
	Code  int32  `gorm:"column:code;primaryKey;autoIncrement:false"`
	Level string `gorm:"column:level;default:'info'"`
}

func (zeroKeyStatus) TableName() string      { return "zero_key_statuses" }
func (zeroKeyStatus) TableHasTriggers() bool { return true }

// zeroKeyLine has a composite key whose second part is 0 on a first line.
type zeroKeyLine struct {
	orm.BaseEntity
	OrderID int64  `gorm:"column:order_id;primaryKey"`
	LineNo  int64  `gorm:"column:line_no;primaryKey"`
	Status  string `gorm:"column:status;default:'new'"`
}

func (zeroKeyLine) TableName() string { return "zero_key_lines" }

// keylessEntry maps a table without a primary key.
type keylessEntry struct {
	orm.BaseEntity
	Message string `gorm:"column:message"`
	Level   string `gorm:"column:level;default:'info'"`
}

func (keylessEntry) TableName() string { return "keyless_entries" }

// TestCreateWithAZeroKeyItWroteOnEveryEngine: a key column the INSERT wrote holds what the
// entity holds, zero included, and a table without a key has nothing to lose. Where Create reads
// generated columns without RETURNING — MySQL, and a SQL Server table whose model opts out with
// TableHasTriggers, which the statuses table is on SQL Server here — these Creates wrote their
// row and returned an error, where the same Creates through RETURNING succeeded.
func TestCreateWithAZeroKeyItWroteOnEveryEngine(t *testing.T) {
	for _, engine := range ormEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			g := gormOf(t, engine.ds)
			tables := []string{"zero_key_statuses", "zero_key_lines", "keyless_entries"}
			drop := func() {
				for _, table := range tables {
					assert.NoError(t, g.Exec("DROP TABLE IF EXISTS "+table).Error)
				}
			}
			drop()
			t.Cleanup(drop)
			text := "VARCHAR(20)"
			if engine.name == "sqlserver" {
				text = "NVARCHAR(20)"
			}
			for _, statement := range []string{
				"CREATE TABLE zero_key_statuses (code INT NOT NULL PRIMARY KEY, level " + text + " NOT NULL DEFAULT 'info')",
				"CREATE TABLE zero_key_lines (order_id BIGINT NOT NULL, line_no BIGINT NOT NULL, status " + text +
					" NOT NULL DEFAULT 'new', PRIMARY KEY (order_id, line_no))",
				"CREATE TABLE keyless_entries (message " + text + " NOT NULL, level " + text + " NOT NULL DEFAULT 'info')",
			} {
				require.NoError(t, g.Exec(statement).Error, "DDL: %s", statement)
			}
			if engine.name == "sqlserver" {
				require.NoError(t, g.Exec("CREATE TRIGGER zero_key_statuses_audit ON zero_key_statuses AFTER INSERT AS SET NOCOUNT ON").Error)
			}
			session, err := engine.ds.NewSession()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, session.Close()) })

			count := func(table, where string) int64 {
				var n int64
				require.NoError(t, g.Raw("SELECT COUNT(*) FROM "+table+" WHERE "+where).Scan(&n).Error)
				return n
			}

			status := &zeroKeyStatus{Level: "warn"}
			require.NoError(t, orm.New[*zeroKeyStatus](session).Create(status))
			assert.Equal(t, int64(1), count("zero_key_statuses", "code = 0"))
			assert.Equal(t, "warn", status.Level)

			line := &zeroKeyLine{OrderID: 7, Status: "open"}
			require.NoError(t, orm.New[*zeroKeyLine](session).Create(line))
			assert.Equal(t, int64(1), count("zero_key_lines", "order_id = 7 AND line_no = 0"))
			assert.Equal(t, "open", line.Status)

			entry := &keylessEntry{Message: "started", Level: "info"}
			require.NoError(t, orm.New[*keylessEntry](session).Create(entry))
			assert.Equal(t, int64(1), count("keyless_entries", "message = 'started'"))
		})
	}
}

// digitTag's table starts with a digit, which MySQL takes unquoted and SQL Server's dialect
// brackets.
type digitTag struct {
	orm.BaseEntity
	ID    int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Label string `gorm:"column:label"`
}

func (digitTag) TableName() string { return "2024tags" }

type digitTagHolder struct {
	orm.BaseEntity
	ID   int64       `gorm:"column:id;primaryKey;autoIncrement"`
	Name string      `gorm:"column:name"`
	Tags []*digitTag `gorm:"many2many:digit_tag_holder_tags;"`
}

func (digitTagHolder) TableName() string { return "digit_tag_holders" }

// bookTag's table is not ASCII, which Postgres and MySQL take unquoted.
type bookTag struct {
	orm.BaseEntity
	ID    int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Label string `gorm:"column:label"`
}

func (bookTag) TableName() string { return "bücher" }

type bookTagHolder struct {
	orm.BaseEntity
	ID   int64      `gorm:"column:id;primaryKey;autoIncrement"`
	Name string     `gorm:"column:name"`
	Tags []*bookTag `gorm:"many2many:book_tag_holder_tags;"`
}

func (bookTagHolder) TableName() string { return "book_tag_holders" }

// TestManyToManyOnATableWhoseNameIsNotSimpleOnEveryEngine: Postgres and MySQL render a JOIN's ON
// without a context, where a BinaryCondition binds a name that is not a simple ASCII identifier
// as a value. A many-to-many load on 2024tags then compared the join column with the text
// '2024tags.id', which MySQL read as 2024 and loaded nothing, and on bücher Postgres refused the
// cast. The names are emitted as written again, and SQL Server brackets them.
func TestManyToManyOnATableWhoseNameIsNotSimpleOnEveryEngine(t *testing.T) {
	for _, engine := range ormEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			g := gormOf(t, engine.ds)
			autoPK := map[string]string{
				"postgres":  "BIGSERIAL PRIMARY KEY",
				"mysql":     "BIGINT AUTO_INCREMENT PRIMARY KEY",
				"sqlserver": "BIGINT IDENTITY(1,1) PRIMARY KEY",
			}[engine.name]
			session, err := engine.ds.NewSession()
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, session.Close()) })

			if engine.name != "postgres" {
				related := "2024tags"
				if engine.name == "sqlserver" {
					related = "[2024tags]"
				}
				m2mTables(t, g, related, "digit_tag_holders", "digit_tag_holder_tags", "digit_tag_holder_id", "digit_tag_id", autoPK)
				// A join row of another holder that points at tag 2024, which a join on the text
				// '2024tags.id' would have matched on MySQL.
				require.NoError(t, g.Exec("INSERT INTO digit_tag_holder_tags (digit_tag_holder_id, digit_tag_id) VALUES (9, 2024)").Error)

				tags := orm.New[*digitTag](session)
				for _, label := range []string{"one", "two", "three"} {
					require.NoError(t, tags.Create(&digitTag{Label: label}))
				}
				holder := &digitTagHolder{Name: "h", Tags: []*digitTag{{ID: 1}, {ID: 2}}}
				holders := orm.New[*digitTagHolder](session)
				require.NoError(t, holders.Create(holder))
				loaded, err := holders.Find(holder.ID)
				require.NoError(t, err)
				require.NoError(t, holders.LoadRelation(loaded, "Tags"))
				labels := []string{}
				for _, tag := range loaded.Tags {
					labels = append(labels, tag.Label)
				}
				sort.Strings(labels)
				assert.Equal(t, []string{"one", "two"}, labels)
			}

			if engine.name != "sqlserver" {
				m2mTables(t, g, "bücher", "book_tag_holders", "book_tag_holder_tags", "book_tag_holder_id", "book_tag_id", autoPK)
				tags := orm.New[*bookTag](session)
				for _, label := range []string{"one", "two", "three"} {
					require.NoError(t, tags.Create(&bookTag{Label: label}))
				}
				holder := &bookTagHolder{Name: "h", Tags: []*bookTag{{ID: 2}, {ID: 3}}}
				holders := orm.New[*bookTagHolder](session)
				require.NoError(t, holders.Create(holder))
				loaded, err := holders.Find(holder.ID)
				require.NoError(t, err)
				require.NoError(t, holders.LoadRelation(loaded, "Tags"))
				labels := []string{}
				for _, tag := range loaded.Tags {
					labels = append(labels, tag.Label)
				}
				sort.Strings(labels)
				assert.Equal(t, []string{"three", "two"}, labels)
			}
		})
	}
}

// m2mTables creates a related table, a holder table and their join table, and drops them when
// the case ends. related is written as the engine needs it in DDL.
func m2mTables(t *testing.T, g *gorm.DB, related, holders, join, holderColumn, relatedColumn, autoPK string) {
	t.Helper()
	drop := func() {
		for _, table := range []string{join, holders, related} {
			assert.NoError(t, g.Exec("DROP TABLE IF EXISTS "+table).Error)
		}
	}
	drop()
	t.Cleanup(drop)
	for _, statement := range []string{
		"CREATE TABLE " + related + " (id " + autoPK + ", label VARCHAR(20) NOT NULL)",
		"CREATE TABLE " + holders + " (id " + autoPK + ", name VARCHAR(20) NOT NULL)",
		"CREATE TABLE " + join + " (" + holderColumn + " BIGINT NOT NULL, " + relatedColumn + " BIGINT NOT NULL, " +
			"PRIMARY KEY (" + holderColumn + ", " + relatedColumn + "))",
	} {
		require.NoError(t, g.Exec(statement).Error, "DDL: %s", statement)
	}
}

// pgVersionedRow has a version column a trigger bumps on every write, which is how a Postgres
// table keeps what SQL Server's rowversion does.
type pgVersionedRow struct {
	orm.BaseEntity
	ID      int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name    string `gorm:"column:name"`
	Version int64  `gorm:"column:version;->" grgorm:"readback"`
}

func (pgVersionedRow) TableName() string { return "pg_versioned_rows" }

// TestPostgresGuardedUpdateReadsItsOwnVersion is TestSQLServerGuardedUpdateReadsItsOwnRowVersion
// on Postgres: the UPDATE returns the version it wrote with RETURNING, so a second writer's
// commit before anything that follows cannot stand in for it, and the first writer's next
// guarded Update is the conflict it is.
func TestPostgresGuardedUpdateReadsItsOwnVersion(t *testing.T) {
	engine := postgresAndMySQLEngines(t)[0]
	require.Equal(t, "postgres", engine.name)
	g := gormOf(t, engine.ds)
	drop := func() { assert.NoError(t, g.Exec("DROP TABLE IF EXISTS pg_versioned_rows").Error) }
	drop()
	t.Cleanup(drop)
	for _, statement := range []string{
		"CREATE TABLE pg_versioned_rows (id BIGSERIAL PRIMARY KEY, name VARCHAR(20) NOT NULL, version BIGINT NOT NULL DEFAULT 1)",
		"CREATE OR REPLACE FUNCTION pg_versioned_rows_bump() RETURNS trigger AS $$ BEGIN " +
			"NEW.version := OLD.version + 1; RETURN NEW; END $$ LANGUAGE plpgsql",
		"CREATE TRIGGER pg_versioned_rows_bump BEFORE UPDATE ON pg_versioned_rows FOR EACH ROW EXECUTE FUNCTION pg_versioned_rows_bump()",
	} {
		require.NoError(t, g.Exec(statement).Error, "DDL: %s", statement)
	}
	t.Cleanup(func() { assert.NoError(t, g.Exec("DROP FUNCTION IF EXISTS pg_versioned_rows_bump() CASCADE").Error) })

	plain, err := engine.ds.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, plain.Close()) })
	created := &pgVersionedRow{Name: "start"}
	require.NoError(t, orm.New[*pgVersionedRow](plain).Create(created))
	require.Equal(t, int64(1), created.Version, "the default is read back")

	var otherAffected int64
	session := &interleavingSession{ISession: plain, other: func() {
		res := g.Exec("UPDATE pg_versioned_rows SET name = 'C-wrote-this' WHERE id = ? AND version = 2", created.ID)
		require.NoError(t, res.Error)
		otherAffected = res.RowsAffected
	}}
	rows := orm.New[*pgVersionedRow](session)

	a, err := rows.Find(created.ID)
	require.NoError(t, err)
	a.Name = "A-first"
	a.GetMeta().DirtyColumns = map[string]bool{"name": true}
	a.GetMeta().UpdateGuard = []dbCore.Condition{&dbCore.BinaryCondition{Left: "version", Operator: "=", Right: a.Version}}
	require.NoError(t, rows.Update(a))
	require.Equal(t, int64(1), otherAffected, "the second writer's guarded update landed after the first's")
	assert.Equal(t, int64(2), a.Version, "the version the first writer's own UPDATE wrote")

	a.Name = "A-second"
	a.GetMeta().UpdateGuard = []dbCore.Condition{&dbCore.BinaryCondition{Left: "version", Operator: "=", Right: a.Version}}
	assert.ErrorIs(t, rows.Update(a), orm.ErrRowConflict, "the first writer never saw the second's write")
	var name string
	require.NoError(t, g.Raw("SELECT name FROM pg_versioned_rows WHERE id = ?", created.ID).Row().Scan(&name))
	assert.Equal(t, "C-wrote-this", name)
}
