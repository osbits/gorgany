package v2

import (
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/osbits/gorgany/v2/db/orm"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEFShapes pins the SQL the ORM sends for an EF Core-shaped model, as go-mssqldb is handed
// it: the dialect writes "?", gorm rewrites each to @pN. The table is digit-leading and
// schema-qualified, a column is a reserved word, the key is an IDENTITY, a GUID has a server
// default, and a rowversion guards the updates, which is how an app maps a table EF Core owns
// (efOrder below). The live cases in e2e/tests/live_sqlserver_ef_test.go run the same
// statements against a real server.

// efOrder maps [dbo].[2024Orders] the way the EF mapping rules say to.
type efOrder struct {
	orm.BaseEntity
	Id         int32         `gorm:"column:Id;primaryKey;autoIncrement"`
	CustomerId uuid.UUID     `gorm:"column:CustomerId"`
	ManagerId  uuid.NullUUID `gorm:"column:ManagerId"`
	PublicId   uuid.UUID     `gorm:"column:PublicId;->" grgorm:"readback"`
	Order      int32         `gorm:"column:Order"`
	ClosedAt   *time.Time    `gorm:"column:ClosedAt"`
	Notes      *string       `gorm:"column:Notes"`
	RowVersion []byte        `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efOrder) TableName() string { return "dbo.2024Orders" }

// efAuditedOrder is efOrder on the table once it has an enabled trigger.
type efAuditedOrder struct {
	orm.BaseEntity
	Id         int32         `gorm:"column:Id;primaryKey;autoIncrement"`
	CustomerId uuid.UUID     `gorm:"column:CustomerId"`
	ManagerId  uuid.NullUUID `gorm:"column:ManagerId"`
	PublicId   uuid.UUID     `gorm:"column:PublicId;->" grgorm:"readback"`
	Order      int32         `gorm:"column:Order"`
	ClosedAt   *time.Time    `gorm:"column:ClosedAt"`
	Notes      *string       `gorm:"column:Notes"`
	RowVersion []byte        `gorm:"column:RowVersion;->" grgorm:"readback"`
}

func (efAuditedOrder) TableName() string      { return "dbo.2024Orders" }
func (efAuditedOrder) TableHasTriggers() bool { return true }

// efOrderTag is an EF join table: every key column is tagged primaryKey.
type efOrderTag struct {
	orm.BaseEntity
	OrderId int32 `gorm:"column:OrderId;primaryKey;autoIncrement:false"`
	TagId   int32 `gorm:"column:TagId;primaryKey;autoIncrement:false"`
}

func (efOrderTag) TableName() string { return "dbo.OrderTags" }

var (
	efCustomer  = uuid.MustParse("3f2504e0-4f89-41d3-9a0c-0305e82c3301")
	efPublic    = uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	efVersion   = []byte{0, 0, 0, 0, 0, 0, 7, 209}
	efVersion2  = []byte{0, 0, 0, 0, 0, 0, 7, 210}
	efOrderCols = []string{"Id", "CustomerId", "ManagerId", "PublicId", "Order", "ClosedAt", "Notes", "RowVersion"}
)

// efServer answers the ORM's statements by their shape, as SQL Server would for a row with Id
// 42, and records them.
func efServer(guardMatches bool) *fakeServer {
	return &fakeServer{respond: func(query string) fakeResponse {
		switch {
		case strings.HasPrefix(query, "SELECT COUNT(*)"):
			return fakeResponse{sets: []fakeSet{{cols: []string{""}, rows: [][]driver.Value{{int64(1)}}}}}
		case strings.HasPrefix(query, "SELECT TOP (1) * "):
			return fakeResponse{sets: []fakeSet{{cols: efOrderCols, rows: [][]driver.Value{
				{int64(42), efCustomer[:], nil, efPublic[:], int64(3), nil, nil, efVersion},
			}}}}
		case strings.HasPrefix(query, "SELECT TOP (1) [PublicId], [RowVersion]"):
			return fakeResponse{sets: []fakeSet{{cols: []string{"PublicId", "RowVersion"},
				rows: [][]driver.Value{{efPublic[:], efVersion2}}}}}
		case strings.HasPrefix(query, "SELECT TOP (1) [Id]"):
			return fakeResponse{sets: []fakeSet{{cols: []string{"Id"}, rows: [][]driver.Value{{int64(42)}}}}}
		case strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, " OUTPUT INSERTED."):
			// The row the UPDATE matched, as it is after the write; none when the guard lost.
			if !guardMatches && strings.Contains(query, "[RowVersion] = ") {
				return fakeResponse{sets: []fakeSet{{cols: []string{"PublicId", "RowVersion"}}}}
			}
			return fakeResponse{sets: []fakeSet{{cols: []string{"PublicId", "RowVersion"},
				rows: [][]driver.Value{{efPublic[:], efVersion2}}}}}
		case strings.Contains(query, " OUTPUT INSERTED."):
			return fakeResponse{sets: []fakeSet{{cols: []string{"Id", "PublicId", "RowVersion"},
				rows: [][]driver.Value{{int64(42), efPublic[:], efVersion}}}}}
		case strings.HasSuffix(query, identityTail):
			return fakeResponse{sets: []fakeSet{{cols: identityColumns, rows: [][]driver.Value{{int64(42), int64(1)}}}}}
		case strings.HasSuffix(query, rowCountTail):
			if !guardMatches && strings.Contains(query, "[RowVersion] = ") {
				return fakeResponse{sets: []fakeSet{tail(0)}}
			}
			return fakeResponse{sets: []fakeSet{tail(1)}}
		}
		return fakeResponse{err: errors.New("efServer: unexpected statement " + query)}
	}}
}

// efSession is a session on server, opened the way the datasource opens one.
func efSession(t *testing.T, server *fakeServer) dbCore.ISession {
	t.Helper()
	ds := &gormSQLServerDataSource{db: fakeGorm(t, server, false, false)}
	session, err := ds.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, session.Close()) })
	return session
}

// sent returns the SQL of every statement server received.
func sent(server *fakeServer) []string {
	var queries []string
	for _, call := range server.statements() {
		queries = append(queries, call.query)
	}
	return queries
}

// loadedEFOrder is the row with Id 42 as Find returns it.
func loadedEFOrder(t *testing.T, session dbCore.ISession) *efOrder {
	t.Helper()
	order, err := orm.New[*efOrder](session).Find(int32(42))
	require.NoError(t, err)
	require.NotNil(t, order)
	return order
}

func TestEFShapes(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		server := efServer(true)
		count, err := orm.New[*efOrder](efSession(t, server)).Count()
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
		assert.Equal(t, []string{"SELECT COUNT(*) FROM [dbo].[2024Orders]"}, sent(server))
	})

	t.Run("find", func(t *testing.T) {
		server := efServer(true)
		order := loadedEFOrder(t, efSession(t, server))
		assert.Equal(t, []string{"SELECT TOP (1) * FROM [dbo].[2024Orders] WHERE [Id] = @p1"}, sent(server))
		assert.Equal(t, int32(42), order.Id)
		assert.Equal(t, efCustomer, order.CustomerId)
		assert.False(t, order.ManagerId.Valid, "a NULL uniqueidentifier reads as an invalid NullUUID")
		assert.Nil(t, order.ClosedAt)
		assert.Equal(t, efVersion, order.RowVersion)
	})

	t.Run("create", func(t *testing.T) {
		server := efServer(true)
		order := &efOrder{CustomerId: efCustomer, Order: 3}
		require.NoError(t, orm.New[*efOrder](efSession(t, server)).Create(order))

		assert.Equal(t, []string{"INSERT INTO [dbo].[2024Orders] ([CustomerId], [ManagerId], [Order], [ClosedAt], [Notes]) " +
			"OUTPUT INSERTED.[Id], INSERTED.[PublicId], INSERTED.[RowVersion] VALUES (@p1, @p2, @p3, @p4, @p5)"}, sent(server),
			"the IDENTITY key is left to the server, and the read-only columns are read back, never written")
		assert.Equal(t, int32(42), order.Id)
		assert.Equal(t, efPublic, order.PublicId)
		assert.Equal(t, efVersion, order.RowVersion)
	})

	t.Run("create on a trigger table", func(t *testing.T) {
		server := efServer(true)
		order := &efAuditedOrder{CustomerId: efCustomer, Order: 3}
		require.NoError(t, orm.New[*efAuditedOrder](efSession(t, server)).Create(order))

		assert.Equal(t, []string{
			"INSERT INTO [dbo].[2024Orders] ([CustomerId], [ManagerId], [Order], [ClosedAt], [Notes]) " +
				"VALUES (@p1, @p2, @p3, @p4, @p5); SELECT CAST(SCOPE_IDENTITY() AS BIGINT) AS [id], ROWCOUNT_BIG() AS [affected]",
			"SELECT TOP (1) [PublicId], [RowVersion] FROM [dbo].[2024Orders] WHERE [Id] = @p1",
		}, sent(server), "no OUTPUT, which the trigger would make the server refuse (Msg 334)")
		assert.Equal(t, int32(42), order.Id)
		assert.Equal(t, efPublic, order.PublicId)
		assert.Equal(t, efVersion2, order.RowVersion)
		assert.Equal(t, []any{int64(42)}, server.statements()[1].args, "the read-back is keyed on SCOPE_IDENTITY()")
	})

	t.Run("guarded dirty update", func(t *testing.T) {
		server := efServer(true)
		session := efSession(t, server)
		order := loadedEFOrder(t, session)

		order.Order = 4
		meta := order.GetMeta()
		meta.DirtyColumns = map[string]bool{"Order": true}
		meta.UpdateGuard = []dbCore.Condition{&dbCore.BinaryCondition{Left: "RowVersion", Operator: "=", Right: order.RowVersion}}
		require.NoError(t, orm.New[*efOrder](session).Update(order))

		assert.Equal(t, []string{
			"SELECT TOP (1) * FROM [dbo].[2024Orders] WHERE [Id] = @p1",
			"UPDATE [dbo].[2024Orders] SET [Order] = @p1 OUTPUT INSERTED.[PublicId], INSERTED.[RowVersion] " +
				"WHERE [Id] = @p2 AND [RowVersion] = @p3",
		}, sent(server), "the new rowversion is read in the UPDATE itself, so no later write can stand in for it")
		assert.Equal(t, efVersion2, order.RowVersion, "the new rowversion is read back, so the next guarded update can use it")
	})

	t.Run("guarded dirty update on a trigger table", func(t *testing.T) {
		server := efServer(true)
		session := efSession(t, server)
		order := &efAuditedOrder{Id: 42, Order: 4, RowVersion: efVersion}
		order.SetMeta(&orm.EntityMeta{IsLoaded: true, LoadedColumns: map[string]bool{}})
		meta := order.GetMeta()
		meta.DirtyColumns = map[string]bool{"Order": true}
		meta.UpdateGuard = []dbCore.Condition{&dbCore.BinaryCondition{Left: "RowVersion", Operator: "=", Right: order.RowVersion}}
		require.NoError(t, orm.New[*efAuditedOrder](session).Update(order))

		assert.Equal(t, []string{
			"UPDATE [dbo].[2024Orders] SET [Order] = @p1 WHERE [Id] = @p2 AND [RowVersion] = @p3; SELECT ROWCOUNT_BIG() AS [affected]",
			"SELECT TOP (1) [PublicId], [RowVersion] FROM [dbo].[2024Orders] WHERE [Id] = @p1",
		}, sent(server), "no OUTPUT, which a trigger on UPDATE would make the server refuse, so a SELECT re-reads")
		assert.Equal(t, efVersion2, order.RowVersion)
	})

	t.Run("guarded update that lost", func(t *testing.T) {
		server := efServer(false)
		session := efSession(t, server)
		order := loadedEFOrder(t, session)

		order.Order = 4
		meta := order.GetMeta()
		meta.DirtyColumns = map[string]bool{"Order": true}
		meta.UpdateGuard = []dbCore.Condition{&dbCore.BinaryCondition{Left: "RowVersion", Operator: "=", Right: order.RowVersion}}
		err := orm.New[*efOrder](session).Update(order)
		require.ErrorIs(t, err, orm.ErrRowConflict)

		assert.Equal(t, "SELECT TOP (1) [Id] FROM [dbo].[2024Orders] WHERE [Id] = @p1", sent(server)[2],
			"a guard that matched nothing is told from a row that is gone by one probe")
		assert.Len(t, sent(server), 3, "nothing is read back after a write that did not happen")
		assert.Equal(t, efVersion, order.RowVersion, "and the entity keeps the version it held")
	})

	t.Run("full-row update", func(t *testing.T) {
		server := efServer(true)
		session := efSession(t, server)
		order := loadedEFOrder(t, session)

		order.GetMeta().UpdateGuard = []dbCore.Condition{&dbCore.BinaryCondition{Left: "RowVersion", Operator: "=", Right: order.RowVersion}}
		require.NoError(t, orm.New[*efOrder](session).Update(order))
		assert.Equal(t, "UPDATE [dbo].[2024Orders] SET [ClosedAt] = @p1, [CustomerId] = @p2, [ManagerId] = @p3, [Notes] = @p4, "+
			"[Order] = @p5 OUTPUT INSERTED.[PublicId], INSERTED.[RowVersion] WHERE [Id] = @p6 AND [RowVersion] = @p7",
			sent(server)[1], "neither the key nor a read-only column is in the SET list")
	})

	t.Run("delete", func(t *testing.T) {
		server := efServer(true)
		require.NoError(t, orm.New[*efOrder](efSession(t, server)).Delete(&efOrder{Id: 42}))
		assert.Equal(t, []string{"DELETE FROM [dbo].[2024Orders] WHERE [Id] = @p1; SELECT ROWCOUNT_BIG() AS [affected]"}, sent(server))
	})

	t.Run("composite delete", func(t *testing.T) {
		server := efServer(true)
		require.NoError(t, orm.New[*efOrderTag](efSession(t, server)).Delete(&efOrderTag{OrderId: 42, TagId: 7}))
		assert.Equal(t, []string{"DELETE FROM [dbo].[OrderTags] WHERE [OrderId] = @p1 AND [TagId] = @p2; SELECT ROWCOUNT_BIG() AS [affected]"},
			sent(server))
		assert.Equal(t, []any{int32(42), int32(7)}, server.statements()[0].args)
	})
}

// efTag is keyed by a string column that is not called id, on a digit-leading dbo table.
type efTag struct {
	orm.BaseEntity
	Code string `gorm:"column:Code;primaryKey"`
}

func (efTag) TableName() string { return "dbo.2024Tags" }

// efTaggedOrder links orders to tags through a join table.
type efTaggedOrder struct {
	orm.BaseEntity
	Id   int32    `gorm:"column:Id;primaryKey;autoIncrement"`
	Tags []*efTag `gorm:"many2many:order_2024tags;"`
}

func (efTaggedOrder) TableName() string { return "dbo.2024Orders" }

// TestEFManyToManyJoinIsBracketed: the ON of a many-to-many load names the related table's key
// and the join column as "?." identifiers, which SQL Server's rendering context brackets, so a
// digit-leading, schema-qualified related table joins on its own key column.
func TestEFManyToManyJoinIsBracketed(t *testing.T) {
	server := &fakeServer{respond: func(string) fakeResponse {
		return fakeResponse{sets: []fakeSet{{cols: []string{"Code"}}}}
	}}
	require.NoError(t, orm.New[*efTaggedOrder](efSession(t, server)).LoadRelation(&efTaggedOrder{Id: 42}, "Tags"))

	statements := server.statements()
	require.Len(t, statements, 1)
	assert.Contains(t, statements[0].query,
		" INNER JOIN [order_2024tags] ON [dbo].[2024Tags].[Code] = [order_2024tags].[ef_tag_code] ")
	assert.Equal(t, []any{int32(42)}, statements[0].args, "no name is bound as a value")
}
