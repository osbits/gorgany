package v2

import (
	"database/sql/driver"
	"strings"
	"testing"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The catalog read behind orm.VerifyModel, on the fake server. The live cases in e2e/tests run
// it against SQL Server's real catalog views.

var traitsColumns = []string{"column", "is_nullable", "is_identity", "is_generated", "has_default", "key_ordinal",
	"insert_triggers", "instead_of_insert_triggers", "update_triggers"}

// ordersCatalog is [dbo].[2024Orders] as the catalog query reports it: an IDENTITY key, a
// NULLable column, a defaulted GUID, a rowversion, two enabled triggers on INSERT, one of them
// INSTEAD OF, and three on UPDATE.
func ordersCatalog() fakeSet {
	return fakeSet{cols: traitsColumns, rows: [][]driver.Value{
		{"Id", false, true, false, false, int64(1), int64(2), int64(1), int64(3)},
		{"CustomerId", false, false, false, false, int64(0), int64(2), int64(1), int64(3)},
		{"ManagerId", true, false, false, false, int64(0), int64(2), int64(1), int64(3)},
		{"PublicId", false, false, false, true, int64(0), int64(2), int64(1), int64(3)},
		{"Order", false, false, false, false, int64(0), int64(2), int64(1), int64(3)},
		{"RowVersion", false, false, true, false, int64(0), int64(2), int64(1), int64(3)},
	}}
}

// TestTableTraitsReadsTheCatalogInOneSelect: one read-only statement, which passes both guards,
// keyed by OBJECT_ID of the name the way the dialect brackets it, so a digit-leading table
// resolves.
func TestTableTraitsReadsTheCatalogInOneSelect(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{ordersCatalog()}})
	ds := &gormSQLServerDataSource{db: fakeGorm(t, server, true, true), readOnly: true, externalSchema: true}

	var reporter dbCore.TableTraitsReporter = ds
	traits, err := reporter.TableTraits(ctx, "dbo.2024Orders")
	require.NoError(t, err, "the catalog read passes the read-only and external-schema guards")
	assert.Equal(t, dbCore.TableTraits{
		Columns:                 []string{"Id", "CustomerId", "ManagerId", "PublicId", "Order", "RowVersion"},
		NullableColumns:         []string{"ManagerId"},
		InsertTriggers:          2,
		InsteadOfInsertTriggers: 1,
		UpdateTriggers:          3,
		IdentityColumn:          "Id",
		GeneratedColumns:        []string{"RowVersion"},
		DefaultColumns:          []string{"PublicId"},
		PrimaryKey:              []string{"Id"},
	}, traits)

	call := server.only(t)
	assert.Equal(t, "query", call.kind)
	assert.Equal(t, []any{"[dbo].[2024Orders]"}, call.args)
	for _, fragment := range []string{
		"c.[is_nullable] AS [is_nullable]",
		"JOIN sys.trigger_events AS te ON te.[object_id] = tr.[object_id]",
		"tr.[is_disabled] = 0 AND te.[type_desc] = N'INSERT')",
		"AND tr.[is_instead_of_trigger] = 1) AS [instead_of_insert_triggers]",
		"tr.[is_disabled] = 0 AND te.[type_desc] = N'UPDATE')",
		"LEFT JOIN sys.identity_columns AS ic",
		"c.[system_type_id] = 189 OR c.[is_computed] = 1 OR c.[generated_always_type] <> 0",
		"LEFT JOIN sys.default_constraints AS dc",
		"WHERE ix.[is_primary_key] = 1",
		"WHERE c.[object_id] = OBJECT_ID(@p1)",
	} {
		assert.Contains(t, call.query, fragment)
	}
	assert.Equal(t, 1, strings.Count(call.query, "@"), "the one placeholder is the only @, so gorm keeps positional parameters")
}

// TestTableTraitsOrdersACompositeKeyByKeyOrdinal: the key comes back in key order, which is
// not the column order.
func TestTableTraitsOrdersACompositeKeyByKeyOrdinal(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{{cols: traitsColumns, rows: [][]driver.Value{
		{"TagId", false, false, false, false, int64(2), int64(0), int64(0), int64(0)},
		{"Note", true, false, false, false, int64(0), int64(0), int64(0), int64(0)},
		{"OrderId", false, false, false, false, int64(1), int64(0), int64(0), int64(0)},
	}}}})
	ds := &gormSQLServerDataSource{db: fakeGorm(t, server, false, false)}

	traits, err := ds.TableTraits(ctx, "[dbo].[OrderTags]")
	require.NoError(t, err)
	assert.Equal(t, []string{"OrderId", "TagId"}, traits.PrimaryKey)
	assert.Equal(t, []string{"TagId", "Note", "OrderId"}, traits.Columns, "the columns stay in column order")
	assert.Zero(t, traits.InsertTriggers)
	assert.Zero(t, traits.UpdateTriggers)
	assert.Empty(t, traits.IdentityColumn)
	assert.Equal(t, []any{"[dbo].[OrderTags]"}, server.only(t).args, "a bracketed name is passed as it is")
}

// TestTableTraitsOfAMissingTableIsAnError: OBJECT_ID of a table that does not exist is NULL and
// matches no row, which must not read as a table with nothing to say about it.
func TestTableTraitsOfAMissingTableIsAnError(t *testing.T) {
	server := answer(fakeResponse{sets: []fakeSet{{cols: traitsColumns}}})
	ds := &gormSQLServerDataSource{db: fakeGorm(t, server, false, false)}

	_, err := ds.TableTraits(ctx, "dbo.Missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "[dbo].[Missing] is not a table in this database")
}

// TestTableTraitsRefusesANameThatIsNotThisDatabasesTable: the catalog views read are this
// database's, so a three- or four-part name would be looked up in the wrong catalog, and a
// string that is not a name is not sent at all.
func TestTableTraitsRefusesANameThatIsNotThisDatabasesTable(t *testing.T) {
	server := &fakeServer{}
	ds := &gormSQLServerDataSource{db: fakeGorm(t, server, false, false)}

	for _, table := range []string{"", "other.dbo.Orders", "srv.other.dbo.Orders", "Orders; DROP TABLE x", "[Orders", "COUNT(*)"} {
		_, err := ds.TableTraits(ctx, table)
		require.Errorf(t, err, "table %q", table)
		assert.Contains(t, err.Error(), "in this database")
	}
	assert.Empty(t, server.log())
}
