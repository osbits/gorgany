//go:build livedb

// The ORM against an EF Core-shaped schema on SQL Server: an IDENTITY key, a uniqueidentifier
// foreign key, a NEWSEQUENTIALID() default, a rowversion, a reserved-word column, a nullable
// datetime2 and a decimal, on a digit-leading dbo table in a hyphenated database, and a table
// with a trigger. db/sql/gorm/sqlserver/v2's TestEFShapes pins the SQL these cases send; they
// prove the server takes it, and that orm.VerifyModel reads the real catalog.
//
// They run through gateSQLServer like the rest of live_sqlserver_test.go; see there for how to
// start the engine.
package e2e

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/osbits/gorgany/v2/db/orm"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	sqlserverv2 "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// efCustomerID is the one customer every order references. Written as text by the DDL and
// bound as a uuid.UUID by the cases, so guid conversion has to be right for them to match.
var efCustomerID = uuid.MustParse("3f2504e0-4f89-41d3-9a0c-0305e82c3301")

// efLiveOrder maps [dbo].[2024Orders] the way the EF mapping rules say to.
type efLiveOrder struct {
	orm.BaseEntity
	Id         int32         `gorm:"column:Id;primaryKey;autoIncrement"`
	CustomerId uuid.UUID     `gorm:"column:CustomerId"`
	ManagerId  uuid.NullUUID `gorm:"column:ManagerId"`
	PublicId   uuid.UUID     `gorm:"column:PublicId;->" grgorm:"readback"`
	Order      int32         `gorm:"column:Order"`
	ClosedAt   *time.Time    `gorm:"column:ClosedAt"`
	Total      string        `gorm:"column:Total"`
	Notes      *string       `gorm:"column:Notes"`
	RowVersion []byte        `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efLiveOrder) TableName() string        { return "dbo.2024Orders" }
func (efLiveOrder) DbConnectionName() string { return "legacy" }

// efLooseOrder maps the same table breaking each rule VerifyModel checks once.
type efLooseOrder struct {
	orm.BaseEntity
	Id         int32     `gorm:"column:Id;primaryKey;autoIncrement:false"`
	CustomerId uuid.UUID `gorm:"column:CustomerId"`
	ManagerId  uuid.UUID `gorm:"column:ManagerId"`
	PublicId   uuid.UUID `gorm:"column:PublicId"`
	Order      int32     `gorm:"column:Order"`
	ClosedAt   time.Time `gorm:"column:ClosedAt"`
	Region     string    `gorm:"column:Region"`
	RowVersion []byte    `gorm:"column:RowVersion"`
}

func (efLooseOrder) TableName() string { return "dbo.2024Orders" }

// efWrongKeyOrder keys the table by PublicId, which is not its primary key.
type efWrongKeyOrder struct {
	orm.BaseEntity
	Id       int32     `gorm:"column:Id;->"`
	PublicId uuid.UUID `gorm:"column:PublicId;primaryKey;->"`
}

func (efWrongKeyOrder) TableName() string { return "dbo.2024Orders" }

// efProbe admits the case through the SQL Server gate and returns a datasource on the
// Gorgany-EF-Probe database, created through master when it is missing, with overrides applied
// to the default config. The database is left in place: TestSQLServerHyphenatedDatabaseName
// drops and recreates it, so it is made sure of every time rather than once per run.
func efProbe(t *testing.T, overrides map[string]any) dbCore.IDataSource {
	t.Helper()
	mssqlDataSource(t, mssqlConfig())

	master, err := sqlserverv2.NewDataSource(mssqlMasterConfig())
	require.NoError(t, err)
	literal, bracketed := mssqlDatabaseNames(mssqlProbeDatabase)
	createErr := gormOf(t, master).Exec("IF DB_ID(N'" + literal + "') IS NULL CREATE DATABASE " + bracketed).Error
	require.NoError(t, master.Close())
	require.NoError(t, createErr)

	cfg := mssqlConfigFor(mssqlProbeDatabase)
	for key, value := range overrides {
		cfg[key] = value
	}
	ds, err := sqlserverv2.NewDataSource(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })
	return ds
}

// efOrdersSchema creates [dbo].[Customers] and [dbo].[2024Orders] as an EF Core migration
// would, with one customer, and drops both when the case ends. [Legacy] is a column no model
// maps.
func efOrdersSchema(t *testing.T, g *gorm.DB) {
	t.Helper()
	resetMSSQLTables(t, g, []string{"[dbo].[2024Orders]", "[dbo].[Customers]"},
		"CREATE TABLE [dbo].[Customers] ([Id] uniqueidentifier NOT NULL CONSTRAINT [PK_Customers] PRIMARY KEY, "+
			"[Name] nvarchar(100) NOT NULL)",
		"CREATE TABLE [dbo].[2024Orders] ("+
			"[Id] int IDENTITY(1,1) NOT NULL CONSTRAINT [PK_2024Orders] PRIMARY KEY, "+
			"[CustomerId] uniqueidentifier NOT NULL CONSTRAINT [FK_2024Orders_Customers_CustomerId] "+
			"REFERENCES [dbo].[Customers] ([Id]), "+
			"[ManagerId] uniqueidentifier NULL, "+
			"[PublicId] uniqueidentifier NOT NULL CONSTRAINT [DF_2024Orders_PublicId] DEFAULT NEWSEQUENTIALID(), "+
			"[Order] int NOT NULL, "+
			"[ClosedAt] datetime2(7) NULL, "+
			"[Total] decimal(18,2) NOT NULL, "+
			"[Notes] nvarchar(max) NULL, "+
			"[Legacy] nvarchar(50) NULL, "+
			"[RowVersion] rowversion NOT NULL)",
		"INSERT INTO [dbo].[Customers] ([Id], [Name]) VALUES ('"+strings.ToUpper(efCustomerID.String())+"', N'Probe')",
	)
}

// serverText reads one value of the row with Id id as the server's own text.
func serverText(t *testing.T, g *gorm.DB, table, expression string, id int32) string {
	t.Helper()
	var text sql.NullString
	require.NoError(t, g.Raw("SELECT CAST("+expression+" AS nvarchar(100)) FROM "+table+" WHERE [Id] = ?", id).
		Row().Scan(&text))
	if !text.Valid {
		return "NULL"
	}
	return text.String
}

// rowVersionOf reads the rowversion of the row with Id id.
func rowVersionOf(t *testing.T, g *gorm.DB, table string, id int32) []byte {
	t.Helper()
	var version []byte
	require.NoError(t, g.Raw("SELECT [RowVersion] FROM "+table+" WHERE [Id] = ?", id).Row().Scan(&version))
	return version
}

// guardedOn sets meta to write only Order, and only while the row still has version.
func guardedOn(entity orm.EntityWithMeta, version []byte, dirty ...string) {
	meta := entity.GetMeta()
	meta.DirtyColumns = map[string]bool{}
	for _, column := range dirty {
		meta.DirtyColumns[column] = true
	}
	meta.UpdateGuard = []dbCore.Condition{&dbCore.BinaryCondition{Left: "RowVersion", Operator: "=", Right: version}}
}

// TestSQLServerEFShapedTable: Create, Find, All and Count, a guarded Update whose rowversion
// changes and is read back, the conflict a stale copy gets, and Delete, on the EF-shaped
// table; and gorm's migrator on it, in the hyphenated database, by its dbo name.
func TestSQLServerEFShapedTable(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	efOrdersSchema(t, g)
	orders := orm.New[*efLiveOrder](mssqlSession(t, ds))

	order := &efLiveOrder{CustomerId: efCustomerID, Order: 3, Total: "12.50"}
	require.NoError(t, orders.Create(order))
	require.NotZero(t, order.Id, "the IDENTITY key comes back through OUTPUT INSERTED")
	assert.NotEqual(t, uuid.Nil, order.PublicId, "the NEWSEQUENTIALID() default is read back")
	assert.True(t, strings.EqualFold(order.PublicId.String(), serverText(t, g, "[dbo].[2024Orders]", "[PublicId]", order.Id)),
		"the default reads back as the GUID the server shows")
	assert.Len(t, order.RowVersion, 8)
	assert.Equal(t, rowVersionOf(t, g, "[dbo].[2024Orders]", order.Id), order.RowVersion)

	zurich, err := time.LoadLocation("Europe/Zurich")
	require.NoError(t, err)
	closed := time.Date(2026, 9, 27, 10, 0, 0, 0, zurich)
	manager := uuid.MustParse("01234567-89ab-cdef-0123-456789abcdef")
	note := "second"
	second := &efLiveOrder{CustomerId: efCustomerID, ManagerId: uuid.NullUUID{UUID: manager, Valid: true},
		Order: 5, ClosedAt: &closed, Total: "0.05", Notes: &note}
	require.NoError(t, orders.Create(second))
	assert.Equal(t, "2026-09-27 08:00:00.0000000", serverText(t, g, "[dbo].[2024Orders]", "[ClosedAt]", second.Id),
		"a datetime2 holds the instant in UTC")

	found, err := orders.Find(order.Id)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, efCustomerID, found.CustomerId)
	assert.False(t, found.ManagerId.Valid, "a NULL uniqueidentifier reads as an invalid NullUUID")
	assert.Nil(t, found.ClosedAt)
	assert.Nil(t, found.Notes)
	assert.Equal(t, "12.50", found.Total)
	assert.Equal(t, int32(3), found.Order)
	assert.Equal(t, order.PublicId, found.PublicId)
	assert.Equal(t, order.RowVersion, found.RowVersion)

	foundSecond, err := orders.Find(second.Id)
	require.NoError(t, err)
	require.NotNil(t, foundSecond.ClosedAt)
	assert.True(t, closed.Equal(*foundSecond.ClosedAt), "%s read back as %s", closed, foundSecond.ClosedAt)
	assert.Equal(t, uuid.NullUUID{UUID: manager, Valid: true}, foundSecond.ManagerId)
	assert.Equal(t, "0.05", foundSecond.Total)

	all, err := orders.All()
	require.NoError(t, err)
	assert.Len(t, all, 2)
	count, err := orders.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	stale, err := orders.Find(order.Id)
	require.NoError(t, err)

	before := found.RowVersion
	found.Order = 4
	guardedOn(found, found.RowVersion, "Order")
	require.NoError(t, orders.Update(found))
	assert.NotEqual(t, before, found.RowVersion, "the write changed the rowversion")
	assert.Equal(t, rowVersionOf(t, g, "[dbo].[2024Orders]", order.Id), found.RowVersion, "and it was read back")
	assert.Equal(t, "4", serverText(t, g, "[dbo].[2024Orders]", "[Order]", order.Id))

	stale.Order = 9
	guardedOn(stale, stale.RowVersion, "Order")
	assert.ErrorIs(t, orders.Update(stale), orm.ErrRowConflict, "a copy read before the write lost")
	assert.Equal(t, "4", serverText(t, g, "[dbo].[2024Orders]", "[Order]", order.Id))

	found.Order = 6
	guardedOn(found, found.RowVersion, "Order")
	require.NoError(t, orders.Update(found), "the read-back rowversion guards the next write")

	require.NoError(t, orders.Delete(found))
	count, err = orders.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

}

// TestSQLServerEFShapedSchema: gorm's migrator on the EF-shaped table, by its dbo name in the
// hyphenated database, beside a table of the same name in another schema, which is common in a
// schema EF Core owns. HasTable and ColumnTypes given the model find the table, but
// ColumnTypes given the model reads the columns of both schemas' tables, since
// gorm.io/driver/sqlserver splits the schema off the model's table; given the name it reads
// dbo's alone. orm.VerifyModel asks neither: it reads the columns from the catalog by
// OBJECT_ID, and is not misled.
func TestSQLServerEFShapedSchema(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	efOrdersSchema(t, g)

	require.NoError(t, g.Exec("DROP TABLE IF EXISTS [sales].[2024Orders]").Error)
	require.NoError(t, g.Exec("DROP SCHEMA IF EXISTS [sales]").Error)
	require.NoError(t, g.Exec("CREATE SCHEMA [sales]").Error)
	t.Cleanup(func() { assert.NoError(t, g.Exec("DROP SCHEMA IF EXISTS [sales]").Error) })
	resetMSSQLTables(t, g, []string{"[sales].[2024Orders]"},
		"CREATE TABLE [sales].[2024Orders] ([Id] int NOT NULL PRIMARY KEY, [Region] nvarchar(20) NULL)")

	migrator := g.Migrator()
	assert.True(t, migrator.HasTable(&efLiveOrder{}))
	assert.False(t, migrator.HasTable(&efMissingTable{}))

	names := func(columns []gorm.ColumnType) map[string]bool {
		out := map[string]bool{}
		for _, column := range columns {
			out[column.Name()] = true
		}
		return out
	}
	byName, err := migrator.ColumnTypes("dbo.2024Orders")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"Id": true, "CustomerId": true, "ManagerId": true, "PublicId": true, "Order": true,
		"ClosedAt": true, "Total": true, "Notes": true, "Legacy": true, "RowVersion": true}, names(byName))
	for _, column := range byName {
		if column.Name() == "ManagerId" || column.Name() == "CustomerId" {
			nullable, ok := column.Nullable()
			require.True(t, ok)
			assert.Equal(t, column.Name() == "ManagerId", nullable, column.Name())
		}
	}

	byModel, err := migrator.ColumnTypes(&efLiveOrder{})
	require.NoError(t, err)
	assert.True(t, names(byModel)["Region"], "given the model, sales.2024Orders lends dbo.2024Orders its Region column")

	ctx := context.Background()
	session := mssqlSession(t, ds)
	problems, err := orm.VerifyModel(ctx, session, &efLiveOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems)
	problems, err = orm.VerifyModel(ctx, session, &efLooseOrder{})
	require.NoError(t, err)
	assert.Equal(t, efLooseOrderProblems, efProblemKinds(problems), "Region is still missing from dbo.2024Orders")
}

// TestSQLServerUpdateKeepsUntouchedNullableColumns: an Update with DirtyColumns writes only
// those, so what another writer put into a column the entity did not change — mapped or not —
// survives it. A full-row Update writes the entity's copy back, NULL included, which is why the
// EF write pattern is DirtyColumns plus a rowversion guard.
func TestSQLServerUpdateKeepsUntouchedNullableColumns(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	efOrdersSchema(t, g)
	orders := orm.New[*efLiveOrder](mssqlSession(t, ds))

	order := &efLiveOrder{CustomerId: efCustomerID, Order: 1, Total: "1.00"}
	require.NoError(t, orders.Create(order))
	loaded, err := orders.Find(order.Id)
	require.NoError(t, err)

	require.NoError(t, g.Exec("UPDATE [dbo].[2024Orders] SET [Notes] = N'other writer', [Legacy] = N'kept' WHERE [Id] = ?",
		order.Id).Error)

	loaded.Order = 2
	loaded.GetMeta().DirtyColumns = map[string]bool{"Order": true}
	require.NoError(t, orders.Update(loaded))
	assert.Equal(t, "other writer", serverText(t, g, "[dbo].[2024Orders]", "[Notes]", order.Id))
	assert.Equal(t, "kept", serverText(t, g, "[dbo].[2024Orders]", "[Legacy]", order.Id))
	assert.Equal(t, "2", serverText(t, g, "[dbo].[2024Orders]", "[Order]", order.Id))

	loaded.GetMeta().DirtyColumns = nil
	require.NoError(t, orders.Update(loaded))
	assert.Equal(t, "NULL", serverText(t, g, "[dbo].[2024Orders]", "[Notes]", order.Id),
		"a full-row Update writes the entity's NULL over the other writer's value")
	assert.Equal(t, "kept", serverText(t, g, "[dbo].[2024Orders]", "[Legacy]", order.Id), "an unmapped column is never written")
}

// efProblemKinds returns each problem as "column kind".
func efProblemKinds(problems []orm.ModelProblem) []string {
	out := make([]string, len(problems))
	for i, problem := range problems {
		out[i] = problem.Column + " " + problem.Kind
	}
	return out
}

// efLooseOrderProblems is what VerifyModel finds in efLooseOrder on this table.
var efLooseOrderProblems = []string{
	"Id " + orm.ProblemIdentityNotAuto,
	"ManagerId " + orm.ProblemNullableIntoValue,
	"PublicId " + orm.ProblemServerDefaultWritten,
	"ClosedAt " + orm.ProblemNullableIntoValue,
	"Region " + orm.ProblemMissingColumn,
	"RowVersion " + orm.ProblemGeneratedWritable,
}

// TestVerifyModelFlagsEFShapeMismatches: the model the mapping rules describe is clean, and
// each rule the loose model breaks is found from SQL Server's catalog.
func TestVerifyModelFlagsEFShapeMismatches(t *testing.T) {
	ds := efProbe(t, nil)
	efOrdersSchema(t, gormOf(t, ds))
	session := mssqlSession(t, ds)
	ctx := context.Background()

	traits, err := ds.(dbCore.TableTraitsReporter).TableTraits(ctx, "dbo.2024Orders")
	require.NoError(t, err)
	assert.Equal(t, dbCore.TableTraits{
		Columns: []string{"Id", "CustomerId", "ManagerId", "PublicId", "Order", "ClosedAt", "Total", "Notes",
			"Legacy", "RowVersion"},
		NullableColumns:  []string{"ManagerId", "ClosedAt", "Notes", "Legacy"},
		IdentityColumn:   "Id",
		GeneratedColumns: []string{"RowVersion"},
		DefaultColumns:   []string{"PublicId"},
		PrimaryKey:       []string{"Id"},
	}, traits)

	problems, err := orm.VerifyModel(ctx, session, &efLiveOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems)

	problems, err = orm.VerifyModel(ctx, session, &efLooseOrder{})
	require.NoError(t, err)
	assert.Equal(t, efLooseOrderProblems, efProblemKinds(problems))

	problems, err = orm.VerifyModel(ctx, session, &efWrongKeyOrder{})
	require.NoError(t, err)
	require.Equal(t, []string{" " + orm.ProblemPrimaryKeyMismatch}, efProblemKinds(problems))
	assert.Contains(t, problems[0].Detail, "is (Id) and the model's is (PublicId)")

	_, err = orm.VerifyModel(ctx, session, &efMissingTable{})
	assert.Error(t, err, "a table that is not there is an error, not a model without problems")
}

// efMissingTable maps a table the probe database does not have.
type efMissingTable struct {
	orm.BaseEntity
	Id int32 `gorm:"column:Id;primaryKey;autoIncrement"`
}

func (efMissingTable) TableName() string { return "dbo.2024Missing" }

// TestVerifyModelRunsOnAReadOnlyDatasource: VerifyModel only reads, so it runs, and finds the
// same, on a datasource with read_only and external_schema, whose guards refuse every write and
// every DDL statement.
func TestVerifyModelRunsOnAReadOnlyDatasource(t *testing.T) {
	owner := efProbe(t, nil)
	efOrdersSchema(t, gormOf(t, owner))
	readOnly := efProbe(t, map[string]any{"read_only": true, "external_schema": true})
	require.True(t, dbCore.IsReadOnly(readOnly))
	session := mssqlSession(t, readOnly)
	ctx := context.Background()

	problems, err := orm.VerifyModel(ctx, session, &efLiveOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems)

	problems, err = orm.VerifyModel(ctx, session, &efLooseOrder{})
	require.NoError(t, err)
	assert.Equal(t, efLooseOrderProblems, efProblemKinds(problems))

	assert.ErrorIs(t, orm.New[*efLiveOrder](session).Create(&efLiveOrder{CustomerId: efCustomerID, Total: "1"}),
		dbCore.ErrReadOnly, "and the datasource does refuse writes")
}

// efShipment is a model on a table with a trigger that does not say so.
type efShipment struct {
	orm.BaseEntity
	Id         int32  `gorm:"column:Id;primaryKey;autoIncrement"`
	Carrier    string `gorm:"column:Carrier"`
	RowVersion []byte `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efShipment) TableName() string { return "dbo.2024Shipments" }

// efAuditedShipment is the same model with the opt-out.
type efAuditedShipment struct {
	orm.BaseEntity
	Id         int32  `gorm:"column:Id;primaryKey;autoIncrement"`
	Carrier    string `gorm:"column:Carrier"`
	RowVersion []byte `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efAuditedShipment) TableName() string      { return "dbo.2024Shipments" }
func (efAuditedShipment) TableHasTriggers() bool { return true }

// efShipmentsSchema creates [dbo].[2024Shipments] with a trigger that, on every INSERT and
// UPDATE statement, writes one row into [dbo].[ShipmentAudit] — whose IDENTITY starts at 1000,
// so a key read from the wrong scope cannot pass for the shipment's.
func efShipmentsSchema(t *testing.T, g *gorm.DB) {
	t.Helper()
	resetMSSQLTables(t, g, []string{"[dbo].[2024Shipments]", "[dbo].[ShipmentAudit]"},
		"CREATE TABLE [dbo].[ShipmentAudit] ([Id] int IDENTITY(1000,1) NOT NULL PRIMARY KEY, [Note] nvarchar(50) NOT NULL)",
		"CREATE TABLE [dbo].[2024Shipments] ([Id] int IDENTITY(1,1) NOT NULL CONSTRAINT [PK_2024Shipments] PRIMARY KEY, "+
			"[Carrier] nvarchar(50) NOT NULL, [RowVersion] rowversion NOT NULL)",
		"CREATE TRIGGER [dbo].[TR_2024Shipments_Audit] ON [dbo].[2024Shipments] AFTER INSERT, UPDATE AS "+
			"INSERT INTO [dbo].[ShipmentAudit] ([Note]) VALUES (N'changed')",
	)
}

// TestSQLServerTriggerTableCreate: on a table with a trigger, Create's OUTPUT is refused (Msg
// 334), with a hint naming the opt-out. With it, Create reads the key with SCOPE_IDENTITY(),
// which is the shipment's own key and not the audit row's, and the rowversion with a SELECT.
func TestSQLServerTriggerTableCreate(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	efShipmentsSchema(t, g)
	session := mssqlSession(t, ds)
	ctx := context.Background()

	err := orm.New[*efShipment](session).Create(&efShipment{Carrier: "north"})
	require.Error(t, err)
	assert.Equal(t, int32(334), sqlServerErrorNumber(err))
	assert.Contains(t, err.Error(), "TableHasTriggers() returning true")
	assert.Equal(t, int64(0), countWhere(t, g, "[dbo].[2024Shipments]", "1 = 1"), "the refused INSERT wrote nothing")

	shipment := &efAuditedShipment{Carrier: "north"}
	require.NoError(t, orm.New[*efAuditedShipment](session).Create(shipment))
	var newest int32
	require.NoError(t, g.Raw("SELECT MAX([Id]) FROM [dbo].[2024Shipments]").Scan(&newest).Error)
	assert.Equal(t, newest, shipment.Id, "the key is the shipment's row")
	assert.Less(t, shipment.Id, int32(1000), "and not the audit row's")
	assert.Equal(t, int64(1), countWhere(t, g, "[dbo].[ShipmentAudit]", "[Id] = 1000"), "the trigger did write its row")
	assert.Equal(t, rowVersionOf(t, g, "[dbo].[2024Shipments]", shipment.Id), shipment.RowVersion)

	problems, err := orm.VerifyModel(ctx, session, &efShipment{})
	require.NoError(t, err)
	require.Equal(t, []string{" " + orm.ProblemTriggersNoOptOut}, efProblemKinds(problems))
	assert.Contains(t, problems[0].Detail, "1 enabled trigger(s) on INSERT, so every Create fails")
	assert.Contains(t, problems[0].Detail, "1 enabled trigger(s) on UPDATE, so every Update fails")

	problems, err = orm.VerifyModel(ctx, session, &efAuditedShipment{})
	require.NoError(t, err)
	assert.Empty(t, problems)
}

// TestSQLServerGuardedUpdateWithUnconditionalTriggerStillConflicts: the trigger writes an audit
// row for every UPDATE statement, the one that matched nothing included, so the driver's row
// count for a lost guarded update is 1. The ORM reads the UPDATE's own count, and reports the
// conflict.
func TestSQLServerGuardedUpdateWithUnconditionalTriggerStillConflicts(t *testing.T) {
	ds := efProbe(t, nil)
	g := gormOf(t, ds)
	efShipmentsSchema(t, g)
	shipments := orm.New[*efAuditedShipment](mssqlSession(t, ds))

	created := &efAuditedShipment{Carrier: "north"}
	require.NoError(t, shipments.Create(created))
	fresh, err := shipments.Find(created.Id)
	require.NoError(t, err)
	stale, err := shipments.Find(created.Id)
	require.NoError(t, err)

	fresh.Carrier = "south"
	guardedOn(fresh, fresh.RowVersion, "Carrier")
	require.NoError(t, shipments.Update(fresh))
	assert.Equal(t, rowVersionOf(t, g, "[dbo].[2024Shipments]", created.Id), fresh.RowVersion)

	audited := countWhere(t, g, "[dbo].[ShipmentAudit]", "1 = 1")
	stale.Carrier = "west"
	guardedOn(stale, stale.RowVersion, "Carrier")
	assert.ErrorIs(t, shipments.Update(stale), orm.ErrRowConflict)
	assert.Equal(t, audited+1, countWhere(t, g, "[dbo].[ShipmentAudit]", "1 = 1"),
		"the trigger ran for the UPDATE that matched nothing, and wrote its row")
	assert.Equal(t, "south", serverText(t, g, "[dbo].[2024Shipments]", "[Carrier]", created.Id))
}
