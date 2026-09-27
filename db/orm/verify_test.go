package orm

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// VerifyModel against a fake gorm migrator and a fake catalog, one problem kind at a time. The
// live cases in e2e/tests run the same checks against SQL Server's real catalog.

// fakeDialector opens gorm on nothing: the only thing VerifyModel asks of it is the migrator.
type fakeDialector struct{ migrator *fakeMigrator }

func (d fakeDialector) Name() string                                      { return "fake" }
func (d fakeDialector) Initialize(*gorm.DB) error                         { return nil }
func (d fakeDialector) Migrator(*gorm.DB) gorm.Migrator                   { return d.migrator }
func (fakeDialector) DataTypeOf(*schema.Field) string                     { return "" }
func (fakeDialector) DefaultValueOf(*schema.Field) clause.Expression      { return nil }
func (fakeDialector) BindVarTo(w clause.Writer, _ *gorm.Statement, _ any) { _ = w.WriteByte('?') }
func (fakeDialector) QuoteTo(w clause.Writer, s string)                   { _, _ = w.WriteString(s) }
func (fakeDialector) Explain(sql string, _ ...any) string                 { return sql }

// fakeMigrator answers ColumnTypes and records what it was asked for. Every other method is the
// nil embedded interface's, so a VerifyModel that called one would panic.
type fakeMigrator struct {
	gorm.Migrator
	columns []gorm.ColumnType
	err     error
	asked   []any
}

func (m *fakeMigrator) ColumnTypes(value any) ([]gorm.ColumnType, error) {
	m.asked = append(m.asked, value)
	return m.columns, m.err
}

// verifyDataSource is a gorm-backed datasource without a catalog.
type verifyDataSource struct{ driver any }

func (d *verifyDataSource) NewSession() (dbCore.ISession, error) { return nil, errors.New("unused") }
func (d *verifyDataSource) GetDriver() (any, error)              { return d.driver, nil }
func (d *verifyDataSource) Close() error                         { return nil }

// catalogDataSource also reads table traits, as the SQL Server datasource does.
type catalogDataSource struct {
	verifyDataSource
	traits dbCore.TableTraits
	err    error
	asked  []string
}

func (d *catalogDataSource) TableTraits(_ context.Context, table string) (dbCore.TableTraits, error) {
	d.asked = append(d.asked, table)
	return d.traits, d.err
}

func fakeGormDB(t *testing.T, m *fakeMigrator) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(fakeDialector{migrator: m}, &gorm.Config{})
	require.NoError(t, err)
	return db
}

// verifySession is a session on ds speaking a dialect whose RETURNING triggers block, as SQL
// Server's does.
func verifySession(ds dbCore.IDataSource) dbCore.ISession {
	return &MockSession{executor: &MockExecutor{}, ds: ds, queryFn: triggerBlockedBuilder}
}

// column builds a fake column; the options set what the catalog reports about it.
func column(name string, options ...func(*migrator.ColumnType)) gorm.ColumnType {
	c := migrator.ColumnType{
		NameValue:          sql.NullString{String: name, Valid: true},
		NullableValue:      sql.NullBool{Valid: true},
		PrimaryKeyValue:    sql.NullBool{Valid: true},
		AutoIncrementValue: sql.NullBool{Valid: true},
	}
	for _, option := range options {
		option(&c)
	}
	return c
}

func nullable(c *migrator.ColumnType)   { c.NullableValue.Bool = true }
func primaryKey(c *migrator.ColumnType) { c.PrimaryKeyValue.Bool = true }
func identity(c *migrator.ColumnType)   { c.AutoIncrementValue.Bool = true }
func defaulted(value string) func(*migrator.ColumnType) {
	return func(c *migrator.ColumnType) { c.DefaultValueValue = sql.NullString{String: value, Valid: true} }
}

// ordersColumns is [dbo].[2024Orders] as gorm's migrator reports it.
func ordersColumns() []gorm.ColumnType {
	return []gorm.ColumnType{
		column("Id", primaryKey, identity),
		column("CustomerId"),
		column("ManagerId", nullable),
		column("PublicId", defaulted("newsequentialid()")),
		column("Order"),
		column("ClosedAt", nullable),
		column("RowVersion"),
	}
}

// ordersTraits is the same table as its catalog reports it.
func ordersTraits() dbCore.TableTraits {
	return dbCore.TableTraits{
		Columns:          []string{"Id", "CustomerId", "ManagerId", "PublicId", "Order", "ClosedAt", "RowVersion"},
		NullableColumns:  []string{"ManagerId", "ClosedAt"},
		IdentityColumn:   "Id",
		GeneratedColumns: []string{"RowVersion"},
		DefaultColumns:   []string{"PublicId"},
		PrimaryKey:       []string{"Id"},
	}
}

// verifiedOrder maps the table the way the EF mapping rules say to.
type verifiedOrder struct {
	BaseEntity
	Id         int32         `gorm:"column:Id;primaryKey;autoIncrement"`
	CustomerId uuid.UUID     `gorm:"column:CustomerId"`
	ManagerId  uuid.NullUUID `gorm:"column:ManagerId"`
	PublicId   uuid.UUID     `gorm:"column:PublicId;->" grgorm:"readback"`
	Order      int32         `gorm:"column:Order"`
	ClosedAt   *time.Time    `gorm:"column:ClosedAt"`
	RowVersion []byte        `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (verifiedOrder) TableName() string { return "dbo.2024Orders" }

// verifiedAuditedOrder is verifiedOrder on the table once it has triggers.
type verifiedAuditedOrder struct {
	BaseEntity
	Id         int32         `gorm:"column:Id;primaryKey;autoIncrement"`
	CustomerId uuid.UUID     `gorm:"column:CustomerId"`
	ManagerId  uuid.NullUUID `gorm:"column:ManagerId"`
	PublicId   uuid.UUID     `gorm:"column:PublicId;->" grgorm:"readback"`
	Order      int32         `gorm:"column:Order"`
	ClosedAt   *time.Time    `gorm:"column:ClosedAt"`
	RowVersion []byte        `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (verifiedAuditedOrder) TableName() string      { return "dbo.2024Orders" }
func (verifiedAuditedOrder) TableHasTriggers() bool { return true }

// looseOrder breaks each rule once.
type looseOrder struct {
	BaseEntity
	Id         int32     `gorm:"column:Id;primaryKey;autoIncrement:false"`
	CustomerId uuid.UUID `gorm:"column:CustomerId"`
	ManagerId  uuid.UUID `gorm:"column:ManagerId"`
	PublicId   uuid.UUID `gorm:"column:PublicId"`
	Order      int32     `gorm:"column:Order"`
	ClosedAt   time.Time `gorm:"column:ClosedAt"`
	Region     string    `gorm:"column:Region"`
	RowVersion []byte    `gorm:"column:RowVersion"`
}

func (looseOrder) TableName() string { return "dbo.2024Orders" }

// kinds returns each problem as "column kind", in the order VerifyModel returned them.
func kinds(problems []ModelProblem) []string {
	out := make([]string, len(problems))
	for i, problem := range problems {
		out[i] = problem.Column + " " + problem.Kind
	}
	return out
}

// TestVerifyModelFlagsEachProblemKind: looseOrder against a table with triggers and a
// two-column key finds every kind, table problems first, then per column in the model's order.
func TestVerifyModelFlagsEachProblemKind(t *testing.T) {
	m := &fakeMigrator{columns: ordersColumns()}
	traits := ordersTraits()
	traits.InsertTriggers = 2
	traits.PrimaryKey = []string{"Id", "CustomerId"}
	ds := &catalogDataSource{verifyDataSource: verifyDataSource{driver: fakeGormDB(t, m)}, traits: traits}

	problems, err := VerifyModel(context.Background(), verifySession(ds), &looseOrder{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		" " + ProblemTriggersNoOptOut,
		" " + ProblemPrimaryKeyMismatch,
		"Id " + ProblemIdentityNotAuto,
		"ManagerId " + ProblemNullableIntoValue,
		"PublicId " + ProblemServerDefaultWritten,
		"ClosedAt " + ProblemNullableIntoValue,
		"Region " + ProblemMissingColumn,
		"RowVersion " + ProblemGeneratedWritable,
	}, kinds(problems))

	for _, problem := range problems {
		assert.Equal(t, "dbo.2024Orders", problem.Table)
		assert.NotEmpty(t, problem.Detail)
	}
	assert.Contains(t, problems[0].Detail, "2 enabled trigger(s) on INSERT, so every Create fails")
	assert.Contains(t, problems[0].Detail, "TableHasTriggers() returning true")
	assert.Contains(t, problems[1].Detail, "(Id, CustomerId)")
	assert.Contains(t, problems[2].Detail, "primaryKey;autoIncrement")
	assert.Contains(t, problems[3].Detail, "uuid.UUID")
	assert.Contains(t, problems[7].Detail, `gorm:"->"`)

	assert.Empty(t, m.asked, "with a catalog to ask, gorm's migrator is not asked")
	assert.Equal(t, []string{"dbo.2024Orders"}, ds.asked)
}

// TestVerifyModelAcceptsTheEFMapping: the model the mapping rules describe has no problem, and
// the trigger opt-out answers the trigger check.
func TestVerifyModelAcceptsTheEFMapping(t *testing.T) {
	ds := &catalogDataSource{
		verifyDataSource: verifyDataSource{driver: fakeGormDB(t, &fakeMigrator{columns: ordersColumns()})},
		traits:           ordersTraits(),
	}
	problems, err := VerifyModel(context.Background(), verifySession(ds), &verifiedOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems)

	ds.traits.InsertTriggers = 1
	problems, err = VerifyModel(context.Background(), verifySession(ds), &verifiedOrder{})
	require.NoError(t, err)
	assert.Equal(t, []string{" " + ProblemTriggersNoOptOut}, kinds(problems))

	problems, err = VerifyModel(context.Background(), verifySession(ds), &verifiedAuditedOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems, "the model says its table has triggers")

	pg := &MockSession{executor: &MockExecutor{}, ds: ds}
	problems, err = VerifyModel(context.Background(), pg, &verifiedOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems, "triggers do not block Postgres's RETURNING, so they are no problem there")
}

// TestVerifyModelAsksOnlyTheCatalogWhenThereIsOne: gorm.io/driver/sqlserver filters columns by
// schema only for a qualified name, so for an unqualified one it merged the columns of every
// schema's table of that name, and a bracketed name made it fail. The catalog resolves the name
// as the server resolves the ORM's own statements, so with one to ask, the columns come from it
// and the datasource need not be gorm-backed at all.
func TestVerifyModelAsksOnlyTheCatalogWhenThereIsOne(t *testing.T) {
	traits := ordersTraits()
	ds := &catalogDataSource{verifyDataSource: verifyDataSource{driver: "not gorm"}, traits: traits}

	problems, err := VerifyModel(context.Background(), verifySession(ds), &unqualifiedOrder{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"ManagerId " + ProblemNullableIntoValue,
		"Extra " + ProblemMissingColumn,
	}, kinds(problems), "what the catalog says about the table the ORM writes, and nothing else")
	assert.Equal(t, []string{"Orders2024"}, ds.asked, "the name is passed as the model writes it")

	ds.traits.Columns = nil
	_, err = VerifyModel(context.Background(), verifySession(ds), &unqualifiedOrder{})
	assert.ErrorContains(t, err, "found no columns for Orders2024")
}

// unqualifiedOrder names its table without a schema, as a model on the login's default schema
// may.
type unqualifiedOrder struct {
	BaseEntity
	Id        int32     `gorm:"column:Id;primaryKey;autoIncrement"`
	ManagerId uuid.UUID `gorm:"column:ManagerId"`
	Extra     string    `gorm:"column:Extra"`
}

func (unqualifiedOrder) TableName() string { return "Orders2024" }

// TestVerifyModelCountsOnlyTriggersThatBlockRETURNING: SQL Server refuses an OUTPUT clause only
// for a trigger on the statement's own action. Create's INSERT carries one always, so a trigger
// on INSERT needs the opt-out; Update's UPDATE carries one only for a model with read-back
// fields, so a trigger on UPDATE needs it only there; and a trigger on DELETE never does. The
// opt-out was asked for whatever a trigger fired on, and routed a Create that worked through a
// path that cannot read every key back.
func TestVerifyModelCountsOnlyTriggersThatBlockRETURNING(t *testing.T) {
	check := func(model any, insertTriggers, updateTriggers int) []ModelProblem {
		traits := ordersTraits()
		traits.InsertTriggers, traits.UpdateTriggers = insertTriggers, updateTriggers
		ds := &catalogDataSource{verifyDataSource: verifyDataSource{driver: "not gorm"}, traits: traits}
		problems, err := VerifyModel(context.Background(), verifySession(ds), model)
		require.NoError(t, err)
		return problems
	}

	assert.Empty(t, check(&verifiedOrder{}, 0, 0), "a trigger on DELETE only blocks nothing the ORM sends")

	problems := check(&verifiedOrder{}, 0, 1)
	require.Equal(t, []string{" " + ProblemTriggersNoOptOut}, kinds(problems))
	assert.Contains(t, problems[0].Detail, "1 enabled trigger(s) on UPDATE, so every Update fails")
	assert.NotContains(t, problems[0].Detail, "Create fails")

	assert.Empty(t, check(&plainOrder{}, 0, 1), "an Update without read-back fields sends no RETURNING")

	problems = check(&verifiedOrder{}, 2, 1)
	require.Equal(t, []string{" " + ProblemTriggersNoOptOut}, kinds(problems))
	assert.Contains(t, problems[0].Detail, "2 enabled trigger(s) on INSERT, so every Create fails, and 1 enabled "+
		"trigger(s) on UPDATE")

	assert.Empty(t, check(&verifiedAuditedOrder{}, 2, 1), "the model says its table has triggers")
}

// plainOrder maps the table without read-back fields.
type plainOrder struct {
	BaseEntity
	Id         int32         `gorm:"column:Id;primaryKey;autoIncrement"`
	CustomerId uuid.UUID     `gorm:"column:CustomerId"`
	ManagerId  uuid.NullUUID `gorm:"column:ManagerId"`
	Order      int32         `gorm:"column:Order"`
}

func (plainOrder) TableName() string { return "dbo.2024Orders" }

// guidKeyedAuditedOrder's key is a NEWSEQUENTIALID() default, on a table the model says has
// triggers.
type guidKeyedAuditedOrder struct {
	BaseEntity
	Id   uuid.UUID `gorm:"column:Id;primaryKey;->" grgorm:"readback"`
	Name string    `gorm:"column:Name"`
}

func (guidKeyedAuditedOrder) TableName() string      { return "dbo.2024Orders" }
func (guidKeyedAuditedOrder) TableHasTriggers() bool { return true }

// sequenceKeyedAuditedOrder's key is a DEFAULT (NEXT VALUE FOR a sequence), which gorm takes for
// an auto-increment key, on a table the model says has triggers.
type sequenceKeyedAuditedOrder struct {
	BaseEntity
	Id   int64  `gorm:"column:Id;primaryKey"`
	Name string `gorm:"column:Name"`
}

func (sequenceKeyedAuditedOrder) TableName() string      { return "dbo.2024Orders" }
func (sequenceKeyedAuditedOrder) TableHasTriggers() bool { return true }

// TestVerifyModelFlagsKeysCreateCannotReadBack: without RETURNING, the one generated key an
// INSERT reports is the IDENTITY value of its own scope. A key from a default or a sequence, a
// NEWSEQUENTIALID() GUID, and any key on a table with an INSTEAD OF INSERT trigger is never
// reported, so every Create writes its row and fails. The opt-out used to make VerifyModel
// report such a table clean.
func TestVerifyModelFlagsKeysCreateCannotReadBack(t *testing.T) {
	keyedBy := func(identity string, defaults ...string) dbCore.TableTraits {
		return dbCore.TableTraits{
			Columns:        []string{"Id", "Name"},
			IdentityColumn: identity,
			DefaultColumns: defaults,
			PrimaryKey:     []string{"Id"},
			InsertTriggers: 1,
		}
	}
	for name, tc := range map[string]struct {
		model  any
		traits dbCore.TableTraits
		want   string
	}{
		"a NEWSEQUENTIALID() key": {&guidKeyedAuditedOrder{}, keyedBy("", "Id"), "is not the table's IDENTITY"},
		"a sequence key":          {&sequenceKeyedAuditedOrder{}, keyedBy("", "Id"), "is not the table's IDENTITY"},
		"an INSTEAD OF INSERT trigger": {&verifiedAuditedOrder{}, func() dbCore.TableTraits {
			traits := ordersTraits()
			traits.InsertTriggers, traits.InsteadOfInsertTriggers = 1, 1
			return traits
		}(), "INSTEAD OF INSERT trigger"},
	} {
		t.Run(name, func(t *testing.T) {
			ds := &catalogDataSource{verifyDataSource: verifyDataSource{driver: "not gorm"}, traits: tc.traits}
			problems, err := VerifyModel(context.Background(), verifySession(ds), tc.model)
			require.NoError(t, err)
			require.Equal(t, []string{"Id " + ProblemKeyUnreadable}, kinds(problems))
			assert.Contains(t, problems[0].Detail, tc.want)
			assert.Contains(t, problems[0].Detail, "every Create writes its row and fails")
		})
	}

	// Where RETURNING is used, the same keys read back with it, and are no problem.
	ds := &catalogDataSource{verifyDataSource: verifyDataSource{driver: "not gorm"}, traits: keyedBy("", "Id")}
	ds.traits.InsertTriggers = 0
	problems, err := VerifyModel(context.Background(), &MockSession{executor: &MockExecutor{}, ds: ds}, &guidKeyedAuditedOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems)
}

// assignedKeyOrder2024 is assignedKeyOrder as VerifyModel sees it: a key gorm makes
// auto-increment, on a table whose IDENTITY is another column.
type assignedKeyOrder2024 struct {
	BaseEntity
	OrderNo int32  `gorm:"column:OrderNo;primaryKey"`
	Name    string `gorm:"column:Name"`
}

func (assignedKeyOrder2024) TableName() string      { return "dbo.2024Orders" }
func (assignedKeyOrder2024) TableHasTriggers() bool { return true }

// TestVerifyModelFlagsAnAutoIncrementKeyNothingGenerates: gorm makes every single integer key
// auto-increment unless it is tagged autoIncrement:false, and a key the caller assigns on a
// table whose IDENTITY is another column, or that has none, is then one nothing generates.
func TestVerifyModelFlagsAnAutoIncrementKeyNothingGenerates(t *testing.T) {
	for identity, want := range map[string]string{"RowId": "has RowId for its IDENTITY", "": "has no IDENTITY column"} {
		columns := []string{"OrderNo", "Name"}
		if identity != "" {
			columns = append(columns, identity)
		}
		ds := &catalogDataSource{verifyDataSource: verifyDataSource{driver: "not gorm"}, traits: dbCore.TableTraits{
			Columns: columns, IdentityColumn: identity, PrimaryKey: []string{"OrderNo"}, InsertTriggers: 1,
		}}
		problems, err := VerifyModel(context.Background(), verifySession(ds), &assignedKeyOrder2024{})
		require.NoError(t, err)
		require.Equal(t, []string{"OrderNo " + ProblemAutoIncrementNotIdentity}, kinds(problems), identity)
		assert.Contains(t, problems[0].Detail, want)
		assert.Contains(t, problems[0].Detail, "autoIncrement:false")
	}
}

// readbackDefaultsOrder tags its defaulted columns grgorm:"readback" but not gorm:"->", so
// Create still writes them.
type readbackDefaultsOrder struct {
	BaseEntity
	Id       uuid.UUID `gorm:"column:Id;primaryKey" grgorm:"readback"`
	Status   int32     `gorm:"column:Status" grgorm:"readback"`
	Checksum int32     `gorm:"column:Checksum;->" grgorm:"readback"`
}

func (readbackDefaultsOrder) TableName() string { return "dbo.2024Orders" }

// TestVerifyModelDoesNotExemptAWrittenReadbackField: grgorm:"readback" reads a column back and
// does not stop Create writing it, so the zero value still overrides the default — a zero GUID
// into a NEWSEQUENTIALID() key included. The tag used to silence the check; only a field Create
// does not write is exempt.
func TestVerifyModelDoesNotExemptAWrittenReadbackField(t *testing.T) {
	ds := &catalogDataSource{verifyDataSource: verifyDataSource{driver: "not gorm"}, traits: dbCore.TableTraits{
		Columns:        []string{"Id", "Status", "Checksum"},
		DefaultColumns: []string{"Id", "Status", "Checksum"},
		PrimaryKey:     []string{"Id"},
	}}
	problems, err := VerifyModel(context.Background(), verifySession(ds), &readbackDefaultsOrder{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"Id " + ProblemServerDefaultWritten,
		"Status " + ProblemServerDefaultWritten,
	}, kinds(problems))
	assert.Contains(t, problems[0].Detail, `grgorm:"readback" alone does not stop the write`)
}

// TestVerifyModelWithoutTraitsChecksColumnsOnly: a datasource with no catalog to ask gets the
// checks gorm's migrator can answer — existence, NULLability, IDENTITY, defaults and the key —
// and neither the trigger nor the computed-column check, which only the catalog can.
func TestVerifyModelWithoutTraitsChecksColumnsOnly(t *testing.T) {
	ds := &verifyDataSource{driver: fakeGormDB(t, &fakeMigrator{columns: ordersColumns()})}

	problems, err := VerifyModel(context.Background(), verifySession(ds), &looseOrder{})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"Id " + ProblemIdentityNotAuto,
		"ManagerId " + ProblemNullableIntoValue,
		"PublicId " + ProblemServerDefaultWritten,
		"ClosedAt " + ProblemNullableIntoValue,
		"Region " + ProblemMissingColumn,
	}, kinds(problems))

	// The key comes from the migrator's primary-key flags then.
	columns := ordersColumns()
	columns[0] = column("Id", identity)
	columns[1] = column("CustomerId", primaryKey)
	ds = &verifyDataSource{driver: fakeGormDB(t, &fakeMigrator{columns: columns})}
	problems, err = VerifyModel(context.Background(), verifySession(ds), &verifiedOrder{})
	require.NoError(t, err)
	assert.Equal(t, []string{" " + ProblemPrimaryKeyMismatch}, kinds(problems))
	assert.Contains(t, problems[0].Detail, "(CustomerId)")
}

// TestVerifyModelMatchesColumnsIgnoringCase: SQL Server's default collation and MySQL resolve a
// column name whatever its case, so a model spelling one differently maps it.
func TestVerifyModelMatchesColumnsIgnoringCase(t *testing.T) {
	columns := ordersColumns()
	columns[4] = column("order")
	ds := &verifyDataSource{driver: fakeGormDB(t, &fakeMigrator{columns: columns})}
	problems, err := VerifyModel(context.Background(), verifySession(ds), &verifiedOrder{})
	require.NoError(t, err)
	assert.Empty(t, problems)
}

// TestVerifyModelErrors: a check that cannot run is an error, not an empty list of problems.
func TestVerifyModelErrors(t *testing.T) {
	ctx := context.Background()
	gormDS := func(m *fakeMigrator) *verifyDataSource { return &verifyDataSource{driver: fakeGormDB(t, m)} }

	_, err := VerifyModel(ctx, nil, &verifiedOrder{})
	assert.ErrorContains(t, err, "needs a session")

	_, err = VerifyModel(ctx, verifySession(gormDS(&fakeMigrator{columns: ordersColumns()})), nil)
	assert.ErrorContains(t, err, "needs a model")

	_, err = VerifyModel(ctx, verifySession(gormDS(&fakeMigrator{columns: ordersColumns()})), &unparseableEntity{})
	assert.ErrorContains(t, err, "cannot read the schema")

	_, err = VerifyModel(ctx, verifySession(&verifyDataSource{driver: "not gorm"}), &verifiedOrder{})
	assert.ErrorContains(t, err, "not a *gorm.DB")

	failure := errors.New("Invalid object name 'dbo.2024Orders'")
	_, err = VerifyModel(ctx, verifySession(gormDS(&fakeMigrator{err: failure})), &verifiedOrder{})
	assert.ErrorIs(t, err, failure)

	_, err = VerifyModel(ctx, verifySession(gormDS(&fakeMigrator{})), &verifiedOrder{})
	assert.ErrorContains(t, err, "found no columns for dbo.2024Orders")

	catalogFailure := errors.New("catalog unavailable")
	ds := &catalogDataSource{verifyDataSource: *gormDS(&fakeMigrator{columns: ordersColumns()}), err: catalogFailure}
	_, err = VerifyModel(ctx, verifySession(ds), &verifiedOrder{})
	assert.ErrorIs(t, err, catalogFailure)
}

// TestCanHoldNull pins which Go types tell NULL from a value.
func TestCanHoldNull(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  bool
	}{
		{"", false},
		{0, false},
		{time.Time{}, false},
		{uuid.UUID{}, false},
		{struct{ Valid bool }{}, false},
		{new(string), true},
		{[]byte(nil), true},
		{map[string]any(nil), true},
		{sql.NullString{}, true},
		{sql.Null[int64]{}, true},
		{uuid.NullUUID{}, true},
	} {
		assert.Equal(t, tc.want, canHoldNull(reflect.TypeOf(tc.value)), "%T", tc.value)
	}
}
