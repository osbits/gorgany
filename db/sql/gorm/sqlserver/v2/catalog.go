package v2

import (
	"context"
	"database/sql"
	"fmt"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"gorm.io/gorm"
)

var _ dbCore.TableTraitsReporter = (*gormSQLServerDataSource)(nil)

// tableTraitsQuery reads what orm.VerifyModel needs to know about one table from this
// database's catalog, one row per column in column order: whether it allows NULL, whether it is
// the IDENTITY column, whether the server computes it, whether it has a DEFAULT constraint, its
// place in the primary key (0 when it is not in it), and on every row the table's counts of
// enabled DML triggers that fire on INSERT, of those that are INSTEAD OF, and of those that
// fire on UPDATE.
//
// It is one SELECT, so it passes the read-only guard on a read_only datasource, and it reads
// only catalog views, so it passes the external-schema guard too. A computed column is
// sys.columns.is_computed; a rowversion is system type 189 (timestamp); a temporal table's
// period columns have generated_always_type other than 0. A trigger's events are its rows in
// sys.trigger_events: SQL Server refuses a statement's OUTPUT clause only for a trigger on that
// statement's own action (Msg 334), so a trigger that fires on UPDATE or DELETE alone does not
// block an INSERT's.
//
// The table is found with OBJECT_ID, given its name bracketed the way the dialect writes it,
// so a digit-leading name such as [dbo].[2024Orders] resolves and cannot be misread as
// something else, and an unqualified name resolves to the table the ORM's own statements
// write, in the login's default schema, never a table of that name in another schema. An
// unknown table makes OBJECT_ID NULL, which matches no row.
//
// No "@" appears in it: gorm would switch the statement to named parameters (see batch.go).
const tableTraitsQuery = `SELECT c.[name] AS [column],
	c.[is_nullable] AS [is_nullable],
	CAST(CASE WHEN ic.[column_id] IS NULL THEN 0 ELSE 1 END AS bit) AS [is_identity],
	CAST(CASE WHEN c.[system_type_id] = 189 OR c.[is_computed] = 1 OR c.[generated_always_type] <> 0
		THEN 1 ELSE 0 END AS bit) AS [is_generated],
	CAST(CASE WHEN dc.[object_id] IS NULL THEN 0 ELSE 1 END AS bit) AS [has_default],
	CAST(ISNULL(pk.[key_ordinal], 0) AS int) AS [key_ordinal],
	(SELECT COUNT(*) FROM sys.triggers AS tr JOIN sys.trigger_events AS te ON te.[object_id] = tr.[object_id]
		WHERE tr.[parent_id] = c.[object_id] AND tr.[is_disabled] = 0 AND te.[type_desc] = N'INSERT')
		AS [insert_triggers],
	(SELECT COUNT(*) FROM sys.triggers AS tr JOIN sys.trigger_events AS te ON te.[object_id] = tr.[object_id]
		WHERE tr.[parent_id] = c.[object_id] AND tr.[is_disabled] = 0 AND te.[type_desc] = N'INSERT'
		AND tr.[is_instead_of_trigger] = 1) AS [instead_of_insert_triggers],
	(SELECT COUNT(*) FROM sys.triggers AS tr JOIN sys.trigger_events AS te ON te.[object_id] = tr.[object_id]
		WHERE tr.[parent_id] = c.[object_id] AND tr.[is_disabled] = 0 AND te.[type_desc] = N'UPDATE')
		AS [update_triggers]
FROM sys.columns AS c
LEFT JOIN sys.identity_columns AS ic ON ic.[object_id] = c.[object_id] AND ic.[column_id] = c.[column_id]
LEFT JOIN sys.default_constraints AS dc ON dc.[parent_object_id] = c.[object_id] AND dc.[parent_column_id] = c.[column_id]
LEFT JOIN (SELECT ixc.[object_id], ixc.[column_id], ixc.[key_ordinal]
		FROM sys.indexes AS ix
		JOIN sys.index_columns AS ixc ON ixc.[object_id] = ix.[object_id] AND ixc.[index_id] = ix.[index_id]
		WHERE ix.[is_primary_key] = 1) AS pk
	ON pk.[object_id] = c.[object_id] AND pk.[column_id] = c.[column_id]
WHERE c.[object_id] = OBJECT_ID(?)
ORDER BY c.[column_id]`

// TableTraits reads table's columns, their NULLability, its triggers, IDENTITY, computed and
// defaulted columns and primary key from this database's catalog (see dbCore.TableTraits), implementing
// dbCore.TableTraitsReporter.
func (ds *gormSQLServerDataSource) TableTraits(ctx context.Context, table string) (dbCore.TableTraits, error) {
	return readTableTraits(ctx, ds.db, table)
}

// readTableTraits runs tableTraitsQuery for table on db.
//
// table is a table or schema.table name as a model's TableName() writes it. A name of three or
// four parts is refused: the catalog views read are this database's, so another database's
// table would be looked up in the wrong catalog. So is a string that is not a name at all.
func readTableTraits(ctx context.Context, db *gorm.DB, table string) (dbCore.TableTraits, error) {
	parts, ok := splitParts(table)
	if !ok || !isIdentifier(table) || len(parts) > 2 {
		return dbCore.TableTraits{}, fmt.Errorf("sqlserver: cannot read the catalog of %q: it must be a "+
			"table name such as Orders or dbo.2024Orders, in this database", table)
	}

	rows, err := db.WithContext(ctx).Raw(tableTraitsQuery, quoteIdentifier(table)).Rows()
	if err != nil {
		return dbCore.TableTraits{}, fmt.Errorf("sqlserver: cannot read the catalog of %s: %w", table, wrapServerError(err))
	}
	defer func() { _ = rows.Close() }()

	var (
		traits  dbCore.TableTraits
		keys    = map[int]string{}
		maxKey  int
		columns int
	)
	for rows.Next() {
		var (
			name                                       string
			nullable, identity, generated, withDefault bool
			keyOrdinal                                 int
			insertTriggers, insteadOf, updateTriggers  sql.NullInt64
		)
		if err := rows.Scan(&name, &nullable, &identity, &generated, &withDefault, &keyOrdinal,
			&insertTriggers, &insteadOf, &updateTriggers); err != nil {
			return dbCore.TableTraits{}, fmt.Errorf("sqlserver: cannot read the catalog of %s: %w", table, err)
		}
		columns++
		traits.Columns = append(traits.Columns, name)
		if nullable {
			traits.NullableColumns = append(traits.NullableColumns, name)
		}
		traits.InsertTriggers = int(insertTriggers.Int64)
		traits.InsteadOfInsertTriggers = int(insteadOf.Int64)
		traits.UpdateTriggers = int(updateTriggers.Int64)
		if identity {
			traits.IdentityColumn = name
		}
		if generated {
			traits.GeneratedColumns = append(traits.GeneratedColumns, name)
		}
		if withDefault {
			traits.DefaultColumns = append(traits.DefaultColumns, name)
		}
		if keyOrdinal > 0 {
			keys[keyOrdinal] = name
			maxKey = max(maxKey, keyOrdinal)
		}
	}
	if err := rows.Err(); err != nil {
		return dbCore.TableTraits{}, fmt.Errorf("sqlserver: cannot read the catalog of %s: %w", table, wrapServerError(err))
	}
	if columns == 0 {
		return dbCore.TableTraits{}, fmt.Errorf("sqlserver: %s is not a table in this database, or this "+
			"login cannot see it", quoteIdentifier(table))
	}
	for ordinal := 1; ordinal <= maxKey; ordinal++ {
		if name, ok := keys[ordinal]; ok {
			traits.PrimaryKey = append(traits.PrimaryKey, name)
		}
	}
	return traits, nil
}
