//go:build livedb

// The ORM and orm.VerifyModel against SQL Server tables where a model and its table most easily
// disagree about keys and triggers: a key the caller assigns beside an IDENTITY that is not the
// key, keys from a default or a sequence, triggers on each action, a table shadowed by one of
// the same name in another schema, and a second writer committing between a write and its
// read-back.
//
// They run through gateSQLServer like the rest of live_sqlserver_test.go; see there for how to
// start the engine.
package e2e

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/osbits/gorgany/v2/db/orm"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// efAssignedOrder's key is assigned by the caller. gorm makes it auto-increment all the same,
// as it makes every single integer key not tagged autoIncrement:false, and the table's
// IDENTITY is another column the model does not map.
type efAssignedOrder struct {
	orm.BaseEntity
	OrderNo    int32  `gorm:"column:OrderNo;primaryKey"`
	Name       string `gorm:"column:Name"`
	RowVersion []byte `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efAssignedOrder) TableName() string      { return "dbo.2024AssignedOrders" }
func (efAssignedOrder) TableHasTriggers() bool { return true }

// efTaggedAssignedOrder is efAssignedOrder tagged the way VerifyModel says to.
type efTaggedAssignedOrder struct {
	orm.BaseEntity
	OrderNo    int32  `gorm:"column:OrderNo;primaryKey;autoIncrement:false"`
	Name       string `gorm:"column:Name"`
	RowVersion []byte `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efTaggedAssignedOrder) TableName() string      { return "dbo.2024AssignedOrders" }
func (efTaggedAssignedOrder) TableHasTriggers() bool { return true }

// valueByOrderNo reads one column of the [dbo].[2024AssignedOrders] row with OrderNo orderNo.
func valueByOrderNo(t *testing.T, g *gorm.DB, column string, orderNo int32, dest any) {
	t.Helper()
	require.NoError(t, g.Raw("SELECT "+column+" FROM [dbo].[2024AssignedOrders] WHERE [OrderNo] = ?", orderNo).
		Row().Scan(dest))
}

// TestSQLServerCreateKeepsAnAssignedKeyBesideAnIdentity: on a trigger table Create reads the key
// with SCOPE_IDENTITY(), which is the IDENTITY column's value, here RowId's. It used to replace
// the OrderNo the INSERT had just written with it, key the read-back on another order's row,
// hand the entity that row's OrderNo and rowversion, and let the next guarded Update overwrite
// that order. The key the INSERT wrote stays the entity's, and VerifyModel names the tag that
// says the key is not generated.
func TestSQLServerCreateKeepsAnAssignedKeyBesideAnIdentity(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"[dbo].[2024AssignedOrders]", "[dbo].[2024AssignedAudit]"},
		"CREATE TABLE [dbo].[2024AssignedAudit] ([Id] int IDENTITY(1,1) NOT NULL PRIMARY KEY, [Note] nvarchar(50) NOT NULL)",
		"CREATE TABLE [dbo].[2024AssignedOrders] ([OrderNo] int NOT NULL CONSTRAINT [PK_2024AssignedOrders] PRIMARY KEY, "+
			"[RowId] int IDENTITY(1,1) NOT NULL, [Name] nvarchar(60) NOT NULL, [RowVersion] rowversion NOT NULL)",
		"CREATE TRIGGER [dbo].[TR_2024AssignedOrders_Audit] ON [dbo].[2024AssignedOrders] AFTER INSERT, UPDATE AS "+
			"INSERT INTO [dbo].[2024AssignedAudit] ([Note]) VALUES (N'changed')",
		"INSERT INTO [dbo].[2024AssignedOrders] ([OrderNo], [Name]) VALUES (2, N'someone else''s')",
	)
	session := mssqlSession(t, ds)
	orders := orm.New[*efAssignedOrder](session)

	mine := &efAssignedOrder{OrderNo: 50, Name: "mine"}
	require.NoError(t, orders.Create(mine))
	assert.Equal(t, int32(50), mine.OrderNo, "the key the INSERT wrote, not the IDENTITY value SCOPE_IDENTITY() reports")
	var version []byte
	valueByOrderNo(t, g, "[RowVersion]", 50, &version)
	assert.Equal(t, version, mine.RowVersion, "the rowversion is order 50's")

	guardedOn(mine, mine.RowVersion, "Name")
	mine.Name = "edited by the owner of order 50"
	require.NoError(t, orders.Update(mine))
	var name string
	valueByOrderNo(t, g, "[Name]", 50, &name)
	assert.Equal(t, "edited by the owner of order 50", name)
	valueByOrderNo(t, g, "[Name]", 2, &name)
	assert.Equal(t, "someone else's", name, "the other order is untouched")

	ctx := context.Background()
	problems, err := orm.VerifyModel(ctx, session, &efAssignedOrder{})
	require.NoError(t, err)
	require.Equal(t, []string{"OrderNo " + orm.ProblemAutoIncrementNotIdentity}, efProblemKinds(problems))
	assert.Contains(t, problems[0].Detail, "has RowId for its IDENTITY")
	problems, err = orm.VerifyModel(ctx, session, &efTaggedAssignedOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems)
}

// efTicket's key is a NEWSEQUENTIALID() default, read back with the rowversion.
type efTicket struct {
	orm.BaseEntity
	Id         uuid.UUID `gorm:"column:Id;primaryKey;->"`
	Name       string    `gorm:"column:Name"`
	RowVersion []byte    `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efTicket) TableName() string { return "dbo.2024Tickets" }

// efPlainTicket maps the same table with no read-back field, so its Update sends no OUTPUT.
type efPlainTicket struct {
	orm.BaseEntity
	Id   uuid.UUID `gorm:"column:Id;primaryKey;->"`
	Name string    `gorm:"column:Name"`
}

func (efPlainTicket) TableName() string { return "dbo.2024Tickets" }

// efAuditedTicket is efTicket with the trigger opt-out.
type efAuditedTicket struct {
	orm.BaseEntity
	Id         uuid.UUID `gorm:"column:Id;primaryKey;->"`
	Name       string    `gorm:"column:Name"`
	RowVersion []byte    `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efAuditedTicket) TableName() string      { return "dbo.2024Tickets" }
func (efAuditedTicket) TableHasTriggers() bool { return true }

// TestSQLServerOnlyTriggersOnTheStatementsActionBlockOutput: SQL Server refuses an OUTPUT clause
// only for a trigger on the statement's own action. VerifyModel counted every enabled trigger,
// said every Create failed on a table whose triggers fire on DELETE, and asked for the opt-out,
// which sends Create down a path that cannot read a NEWSEQUENTIALID() key back. It now counts a
// trigger on INSERT against Create, one on UPDATE against the Update of a model with read-back
// fields, and one on DELETE against nothing; and it says the opt-out cannot read that key.
func TestSQLServerOnlyTriggersOnTheStatementsActionBlockOutput(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"[dbo].[2024Tickets]", "[dbo].[2024TicketAudit]"},
		"CREATE TABLE [dbo].[2024TicketAudit] ([Id] int IDENTITY(1,1) NOT NULL PRIMARY KEY, [Note] nvarchar(50) NOT NULL)",
		"CREATE TABLE [dbo].[2024Tickets] ([Id] uniqueidentifier NOT NULL CONSTRAINT [DF_2024Tickets_Id] DEFAULT NEWSEQUENTIALID() "+
			"CONSTRAINT [PK_2024Tickets] PRIMARY KEY, [Name] nvarchar(50) NOT NULL, [RowVersion] rowversion NOT NULL)",
		"CREATE TRIGGER [dbo].[TR_2024Tickets_Delete] ON [dbo].[2024Tickets] AFTER DELETE AS "+
			"INSERT INTO [dbo].[2024TicketAudit] ([Note]) VALUES (N'deleted')",
	)
	session := mssqlSession(t, ds)
	ctx := context.Background()
	tickets := orm.New[*efTicket](session)

	problems, err := orm.VerifyModel(ctx, session, &efTicket{})
	require.NoError(t, err)
	assert.Empty(t, problems, "a trigger on DELETE blocks no OUTPUT the ORM sends")

	ticket := &efTicket{Name: "first"}
	require.NoError(t, tickets.Create(ticket), "the INSERT's OUTPUT is not refused")
	assert.NotEqual(t, uuid.Nil, ticket.Id)
	guardedOn(ticket, ticket.RowVersion, "Name")
	ticket.Name = "second"
	require.NoError(t, tickets.Update(ticket), "nor is the UPDATE's")

	require.NoError(t, g.Exec("CREATE TRIGGER [dbo].[TR_2024Tickets_Update] ON [dbo].[2024Tickets] AFTER UPDATE AS "+
		"INSERT INTO [dbo].[2024TicketAudit] ([Note]) VALUES (N'updated')").Error)

	problems, err = orm.VerifyModel(ctx, session, &efTicket{})
	require.NoError(t, err)
	require.Equal(t, []string{" " + orm.ProblemTriggersNoOptOut}, efProblemKinds(problems))
	assert.Contains(t, problems[0].Detail, "1 enabled trigger(s) on UPDATE, so every Update fails")
	assert.NotContains(t, problems[0].Detail, "Create fails")

	guardedOn(ticket, ticket.RowVersion, "Name")
	ticket.Name = "third"
	err = tickets.Update(ticket)
	assert.Equal(t, int32(334), sqlServerErrorNumber(err), "as VerifyModel said: %v", err)
	require.NoError(t, tickets.Create(&efTicket{Name: "fourth"}), "and Create still works")

	problems, err = orm.VerifyModel(ctx, session, &efPlainTicket{})
	require.NoError(t, err)
	assert.Empty(t, problems, "an Update without read-back fields sends no OUTPUT")

	problems, err = orm.VerifyModel(ctx, session, &efAuditedTicket{})
	require.NoError(t, err)
	require.Equal(t, []string{"Id " + orm.ProblemKeyUnreadable}, efProblemKinds(problems),
		"the opt-out cannot read a NEWSEQUENTIALID() key back")
	before := countWhere(t, g, "[dbo].[2024Tickets]", "1 = 1")
	err = orm.New[*efAuditedTicket](session).Create(&efAuditedTicket{Name: "fifth"})
	require.Error(t, err, "as VerifyModel said")
	assert.Contains(t, err.Error(), "the row exists")
	assert.Equal(t, before+1, countWhere(t, g, "[dbo].[2024Tickets]", "1 = 1"))
}

// efSequencedOrder's key is a DEFAULT (NEXT VALUE FOR a sequence), which gorm takes for an
// auto-increment key, on a table the model says has triggers.
type efSequencedOrder struct {
	orm.BaseEntity
	Id   int64  `gorm:"column:Id;primaryKey"`
	Name string `gorm:"column:Name"`
}

func (efSequencedOrder) TableName() string      { return "dbo.2024SequencedOrders" }
func (efSequencedOrder) TableHasTriggers() bool { return true }

// efRedirectedOrder is on a table whose INSTEAD OF INSERT trigger does the insert itself.
type efRedirectedOrder struct {
	orm.BaseEntity
	Id         int32  `gorm:"column:Id;primaryKey;autoIncrement"`
	Name       string `gorm:"column:Name"`
	RowVersion []byte `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efRedirectedOrder) TableName() string      { return "dbo.2024RedirectedOrders" }
func (efRedirectedOrder) TableHasTriggers() bool { return true }

// TestSQLServerVerifyModelFlagsKeysTheOptOutCannotReadBack: with the opt-out Create learns a
// generated key only from SCOPE_IDENTITY(), which is NULL for a key from a sequence default,
// and for any key when an INSTEAD OF INSERT trigger inserts the row in its own scope. VerifyModel
// reported both tables clean, and every Create wrote its row and failed. It now names the key.
func TestSQLServerVerifyModelFlagsKeysTheOptOutCannotReadBack(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	require.NoError(t, g.Exec("DROP TABLE IF EXISTS [dbo].[2024SequencedOrders]").Error)
	require.NoError(t, g.Exec("DROP SEQUENCE IF EXISTS [dbo].[OrderNumbers]").Error)
	require.NoError(t, g.Exec("CREATE SEQUENCE [dbo].[OrderNumbers] AS bigint START WITH 1000").Error)
	t.Cleanup(func() { assert.NoError(t, g.Exec("DROP SEQUENCE IF EXISTS [dbo].[OrderNumbers]").Error) })
	resetMSSQLTables(t, g, []string{"[dbo].[2024SequencedOrders]", "[dbo].[2024RedirectedOrders]"},
		"CREATE TABLE [dbo].[2024SequencedOrders] ([Id] bigint NOT NULL CONSTRAINT [DF_2024SequencedOrders_Id] "+
			"DEFAULT (NEXT VALUE FOR [dbo].[OrderNumbers]) CONSTRAINT [PK_2024SequencedOrders] PRIMARY KEY, "+
			"[Name] nvarchar(50) NOT NULL)",
		"CREATE TRIGGER [dbo].[TR_2024SequencedOrders] ON [dbo].[2024SequencedOrders] AFTER INSERT AS SET NOCOUNT ON",
		"CREATE TABLE [dbo].[2024RedirectedOrders] ([Id] int IDENTITY(1,1) NOT NULL CONSTRAINT [PK_2024RedirectedOrders] "+
			"PRIMARY KEY, [Name] nvarchar(50) NOT NULL, [RowVersion] rowversion NOT NULL)",
		"CREATE TRIGGER [dbo].[TR_2024RedirectedOrders] ON [dbo].[2024RedirectedOrders] INSTEAD OF INSERT AS BEGIN "+
			"SET NOCOUNT ON; INSERT INTO [dbo].[2024RedirectedOrders] ([Name]) SELECT [Name] FROM inserted; END",
	)
	session := mssqlSession(t, ds)
	ctx := context.Background()

	for _, tc := range []struct {
		name   string
		model  orm.EntityWithMeta
		table  string
		detail string
	}{
		{"a sequence default", &efSequencedOrder{Name: "n"}, "[dbo].[2024SequencedOrders]", "is not the table's IDENTITY"},
		{"an INSTEAD OF INSERT trigger", &efRedirectedOrder{Name: "n"}, "[dbo].[2024RedirectedOrders]", "INSTEAD OF INSERT trigger"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems, err := orm.VerifyModel(ctx, session, tc.model)
			require.NoError(t, err)
			require.Equal(t, []string{"Id " + orm.ProblemKeyUnreadable}, efProblemKinds(problems))
			assert.Contains(t, problems[0].Detail, tc.detail)

			err = orm.New[orm.EntityWithMeta](session).Create(tc.model)
			require.Error(t, err, "as VerifyModel said")
			assert.Contains(t, err.Error(), "the row exists")
			assert.Equal(t, int64(1), countWhere(t, g, tc.table, "1 = 1"))
		})
	}
}

// shadowedOrder names its table without a schema, as a model on the login's default schema
// may, and maps a column only [archive]'s table of that name has.
type shadowedOrder struct {
	orm.BaseEntity
	Id    int32  `gorm:"column:Id;primaryKey;autoIncrement"`
	Notes string `gorm:"column:Notes"`
	Extra string `gorm:"column:Extra"`
}

func (shadowedOrder) TableName() string { return "Orders2024" }

// bracketedOrder is shadowedOrder naming its table the way T-SQL writes it.
type bracketedOrder struct {
	orm.BaseEntity
	Id    int32  `gorm:"column:Id;primaryKey;autoIncrement"`
	Notes string `gorm:"column:Notes"`
	Extra string `gorm:"column:Extra"`
}

func (bracketedOrder) TableName() string { return "[dbo].[Orders2024]" }

// TestSQLServerVerifyModelReadsTheTableTheORMWrites: gorm.io/driver/sqlserver filters columns
// by schema only for a qualified name, so for Orders2024 it merged in [archive].[Orders2024]'s
// columns, which hid a missing column and a NULLable one; and a bracketed name made it fail.
// VerifyModel reads the columns from the catalog by OBJECT_ID, as the server resolves the
// ORM's own statements, which write [dbo].[Orders2024].
func TestSQLServerVerifyModelReadsTheTableTheORMWrites(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	require.NoError(t, g.Exec("DROP TABLE IF EXISTS [archive].[Orders2024]").Error)
	require.NoError(t, g.Exec("DROP SCHEMA IF EXISTS [archive]").Error)
	require.NoError(t, g.Exec("CREATE SCHEMA [archive]").Error)
	t.Cleanup(func() { assert.NoError(t, g.Exec("DROP SCHEMA IF EXISTS [archive]").Error) })
	resetMSSQLTables(t, g, []string{"[dbo].[Orders2024]", "[archive].[Orders2024]"},
		"CREATE TABLE [dbo].[Orders2024] ([Id] int IDENTITY(1,1) NOT NULL PRIMARY KEY, [Notes] nvarchar(50) NULL)",
		"CREATE TABLE [archive].[Orders2024] ([Id] int NOT NULL PRIMARY KEY, [Notes] nvarchar(50) NOT NULL, "+
			"[Extra] nvarchar(50) NOT NULL)",
	)
	session := mssqlSession(t, ds)
	ctx := context.Background()

	want := []string{"Notes " + orm.ProblemNullableIntoValue, "Extra " + orm.ProblemMissingColumn}
	for _, model := range []any{&shadowedOrder{}, &bracketedOrder{}} {
		problems, err := orm.VerifyModel(ctx, session, model)
		require.NoError(t, err, "%T", model)
		assert.Equal(t, want, efProblemKinds(problems), "%T", model)
	}

	err := orm.New[*shadowedOrder](session).Create(&shadowedOrder{Notes: "n", Extra: "x"})
	assert.Equal(t, int32(207), sqlServerErrorNumber(err), "the ORM does write [dbo]'s table, which has no Extra: %v", err)
}

// efVersionedOrder is a table with a rowversion and no triggers, whose Update reads it back.
type efVersionedOrder struct {
	orm.BaseEntity
	Id         int32  `gorm:"column:Id;primaryKey;autoIncrement"`
	Name       string `gorm:"column:Name"`
	RowVersion []byte `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efVersionedOrder) TableName() string { return "dbo.2024VersionedOrders" }

// interleavingSession runs another writer's statement right after the next UPDATE the ORM sends
// through it returns, the moment a read-back that is a statement of its own would read that
// write instead of its own.
type interleavingSession struct {
	dbCore.ISession
	once  sync.Once
	other func()
}

func (s *interleavingSession) Executor() dbCore.IQueryExecutor {
	return &interleavingExecutor{IQueryExecutor: s.ISession.Executor(), session: s}
}

type interleavingExecutor struct {
	dbCore.IQueryExecutor
	session *interleavingSession
}

func (e *interleavingExecutor) afterUpdate(q dbCore.IQueryBuilder) {
	if built := q.Build(); built != nil && built.Update != nil && e.session.other != nil {
		e.session.once.Do(e.session.other)
	}
}

func (e *interleavingExecutor) Exec(ctx context.Context, q dbCore.IQueryBuilder) dbCore.QueryResult {
	res := e.IQueryExecutor.Exec(ctx, q)
	e.afterUpdate(q)
	return res
}

func (e *interleavingExecutor) Find(ctx context.Context, q dbCore.IQueryBuilder, dest interface{}) dbCore.QueryResult {
	res := e.IQueryExecutor.Find(ctx, q, dest)
	e.afterUpdate(q)
	return res
}

// TestSQLServerGuardedUpdateReadsItsOwnRowVersion: a second writer commits a guarded update
// between the ORM's UPDATE and anything that follows it. The read-back used to be a SELECT of its
// own, which took the second writer's rowversion, so the first writer's next guarded Update
// matched and overwrote the second writer's change. The UPDATE now returns its own rowversion
// with OUTPUT, so that next Update is the conflict it is.
func TestSQLServerGuardedUpdateReadsItsOwnRowVersion(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"[dbo].[2024VersionedOrders]"},
		"CREATE TABLE [dbo].[2024VersionedOrders] ([Id] int IDENTITY(1,1) NOT NULL PRIMARY KEY, "+
			"[Name] nvarchar(50) NOT NULL, [RowVersion] rowversion NOT NULL)")
	plain := mssqlSession(t, ds)
	created := &efVersionedOrder{Name: "start"}
	require.NoError(t, orm.New[*efVersionedOrder](plain).Create(created))

	var otherAffected int64
	session := &interleavingSession{ISession: plain, other: func() {
		var current []byte
		require.NoError(t, g.Raw("SELECT [RowVersion] FROM [dbo].[2024VersionedOrders] WHERE [Id] = ?", created.Id).
			Row().Scan(&current))
		res := g.Exec("UPDATE [dbo].[2024VersionedOrders] SET [Name] = N'C-wrote-this' WHERE [Id] = ? AND [RowVersion] = ?",
			created.Id, current)
		require.NoError(t, res.Error)
		otherAffected = res.RowsAffected
	}}
	orders := orm.New[*efVersionedOrder](session)

	a, err := orders.Find(created.Id)
	require.NoError(t, err)
	a.Name = "A-first"
	guardedOn(a, a.RowVersion, "Name")
	require.NoError(t, orders.Update(a))
	require.Equal(t, int64(1), otherAffected, "the second writer's guarded update landed after the first's")

	a.Name = "A-second"
	guardedOn(a, a.RowVersion, "Name")
	assert.ErrorIs(t, orders.Update(a), orm.ErrRowConflict, "the first writer never saw the second's write")
	assert.Equal(t, "C-wrote-this", serverText(t, g, "[dbo].[2024VersionedOrders]", "[Name]", created.Id))
}

// efDefaultsOrder tags defaulted columns grgorm:"readback" but not gorm:"->", so Create writes
// them anyway.
type efDefaultsOrder struct {
	orm.BaseEntity
	Id        uuid.UUID    `gorm:"column:Id;primaryKey" grgorm:"readback"`
	CreatedAt sql.NullTime `gorm:"column:CreatedAt" grgorm:"readback"`
	Status    int32        `gorm:"column:Status" grgorm:"readback"`
}

func (efDefaultsOrder) TableName() string { return "dbo.2024DefaultsOrders" }

// TestSQLServerVerifyModelFlagsAWrittenReadbackDefault: grgorm:"readback" does not stop Create
// writing a field, so it overrides the column's default with the zero value, a zero GUID into a
// NEWSEQUENTIALID() key included. VerifyModel let the tag silence the check.
func TestSQLServerVerifyModelFlagsAWrittenReadbackDefault(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	resetMSSQLTables(t, g, []string{"[dbo].[2024DefaultsOrders]"},
		"CREATE TABLE [dbo].[2024DefaultsOrders] ([Id] uniqueidentifier NOT NULL DEFAULT NEWSEQUENTIALID() PRIMARY KEY, "+
			"[CreatedAt] datetime2 NULL DEFAULT SYSUTCDATETIME(), [Status] int NOT NULL DEFAULT 7)")
	problems, err := orm.VerifyModel(context.Background(), mssqlSession(t, ds), &efDefaultsOrder{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"Id " + orm.ProblemServerDefaultWritten,
		"CreatedAt " + orm.ProblemServerDefaultWritten,
		"Status " + orm.ProblemServerDefaultWritten,
	}, efProblemKinds(problems))
	for _, problem := range problems {
		assert.True(t, strings.Contains(problem.Detail, `grgorm:"readback" alone does not stop the write`), problem.Detail)
	}
}
