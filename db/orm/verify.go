package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// ModelProblem is one way a model disagrees with the table it maps, as VerifyModel found it.
//
// Table is the model's table, and Column the column the problem is about, or "" for one about
// the whole table: its triggers or its primary key. Kind is one of the Problem constants, for a
// caller that acts on some kinds and not others. Detail says what goes wrong and what to change
// in the model, in a sentence an operator can read.
type ModelProblem struct {
	Table  string
	Column string
	Kind   string
	Detail string
}

// The kinds of ModelProblem. Each names a disagreement that makes a write fail at the server,
// or, worse, succeed and store the wrong value.
const (
	// ProblemMissingColumn: the model maps a field to a column the table does not have. Every
	// read of the model fails, and so does every write that names the column.
	ProblemMissingColumn = "missing_column"

	// ProblemNullableIntoValue: a NULLable column is mapped to a Go type that cannot hold NULL,
	// such as string, int or time.Time. A NULL reads as the zero value, and the next full-row
	// Update writes that zero value back over the NULL.
	ProblemNullableIntoValue = "nullable_column_mapped_to_non_nullable_type"

	// ProblemGeneratedWritable: a column the server computes — a rowversion, a computed column,
	// a GENERATED ALWAYS period column — is mapped to a field the ORM writes. The server refuses
	// the write (on SQL Server, Msg 271 or 272).
	ProblemGeneratedWritable = "generated_column_without_read_only_tag"

	// ProblemIdentityNotAuto: an IDENTITY (auto-increment) column is mapped to a field Create
	// writes. Create sends the field's zero value, which the server refuses (on SQL Server, Msg
	// 544), and never reads the generated key back.
	ProblemIdentityNotAuto = "identity_without_autoincrement"

	// ProblemAutoIncrementNotIdentity: a primary key field is autoIncrement — as gorm makes every
	// single integer key not tagged autoIncrement:false — but its column is not the table's
	// IDENTITY and has no default, so nothing on the server generates it. Create leaves a zero
	// key out of the INSERT for the server to fill, and the server refuses the row.
	ProblemAutoIncrementNotIdentity = "autoincrement_without_identity"

	// ProblemServerDefaultWritten: a column with a server default is mapped to a field Create
	// writes, so the default never applies: an INSERT from an entity that left the field unset
	// stores its zero value. A grgorm:"readback" tag does not stop the write; gorm:"->" does.
	ProblemServerDefaultWritten = "server_default_written_by_insert"

	// ProblemTriggersNoOptOut: the table has enabled triggers that fire on a statement the ORM
	// sends with RETURNING, the dialect's RETURNING is refused on such a table (on SQL Server,
	// Msg 334), and the model does not implement TableWithTriggers. With a trigger on INSERT
	// every Create fails; with one on UPDATE, every Update of a model with grgorm:"readback"
	// fields, which Update reads with RETURNING.
	ProblemTriggersNoOptOut = "enabled_triggers_without_TableHasTriggers"

	// ProblemKeyUnreadable: Create reads generated columns without RETURNING on this table — the
	// engine has none, or the model opts out with TableWithTriggers — and a primary key column the
	// server generates cannot be learned that way. Without RETURNING the only generated key an
	// INSERT reports is its IDENTITY (auto-increment) value, taken in the INSERT's own scope, so
	// a key from a default or a sequence, a NEWSEQUENTIALID() GUID, and any key on a table with
	// an INSTEAD OF INSERT trigger, which inserts the row in the trigger's scope, is never
	// reported. Every Create then writes its row and fails, and a retry writes it again.
	ProblemKeyUnreadable = "generated_key_unreadable_without_returning"

	// ProblemPrimaryKeyMismatch: the model's primary key is not the table's. Update, Delete and
	// Find address rows by the model's key, so they address rows by the wrong columns.
	ProblemPrimaryKeyMismatch = "primary_key_mismatch"
)

// VerifyModel compares model with the table it maps and returns every disagreement it finds,
// before any write has been sent.
//
// It is meant for a table gorgany does not own — an EF Core schema, say — whose columns a
// hand-written model has to match in ways a compiler cannot check, and where each mismatch
// otherwise surfaces as a failed or wrong write in production. Run it once per model, against
// the database the app will use, and fix the model until it returns nothing.
//
// It only reads: it generates nothing and runs no DDL, so it works on a read_only datasource
// and on one with external_schema. When the session's datasource implements
// core.TableTraitsReporter — the SQL Server one does — everything comes from that one catalog
// read: which columns exist, which are NULLable, IDENTITY, computed or defaulted, the primary
// key, and the triggers. The catalog finds the table as the server resolves the name in the
// ORM's own statements, so the table checked is the table written. gorm's migrator is not asked
// then: gorm.io/driver/sqlserver filters by schema only for a qualified name, so for an
// unqualified one it mixes in the columns of every schema's table of that name, and it cannot
// read a bracketed name at all.
//
// Otherwise the columns come from gorm's migrator (ColumnTypes) on the session's datasource,
// which must therefore be gorm-backed, as every framework engine is: whether each mapped column
// exists, whether it is NULLable, IDENTITY, has a default, and is part of the primary key. The
// checks only the catalog can answer — triggers, computed columns, and the key checks that
// need to know the IDENTITY column for certain — are skipped then.
//
// The problems come table first (triggers, then the primary key), then the key columns Create
// cannot read back, then per column in the model's column order. An error means the check itself could not run: the model does not
// parse, the datasource is not gorm-backed and reports no traits, or the table cannot be read,
// which includes a table that does not exist.
func VerifyModel(ctx context.Context, s dbCore.ISession, model any) ([]ModelProblem, error) {
	if s == nil {
		return nil, errors.New("orm: VerifyModel needs a session")
	}
	if isNilValue(model) {
		return nil, errors.New("orm: VerifyModel needs a model")
	}
	entitySchema, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		return nil, fmt.Errorf("orm: VerifyModel cannot read the schema of %T: %w", model, err)
	}
	table := entitySchema.Table

	ds := s.DataSource()
	if ds == nil {
		return nil, fmt.Errorf("orm: VerifyModel cannot check %s: the session has no datasource", table)
	}

	v := verification{table: table, schema: entitySchema}
	if reporter, ok := ds.(dbCore.TableTraitsReporter); ok {
		traits, err := reporter.TableTraits(ctx, table)
		if err != nil {
			return nil, fmt.Errorf("orm: VerifyModel cannot read the catalog of %s: %w", table, err)
		}
		v.traits = &traits
		v.columns = columnsFromTraits(traits)
		v.tableKey, v.keyKnown = traits.PrimaryKey, true
	} else {
		columns, err := migratorColumns(ctx, ds, table)
		if err != nil {
			return nil, err
		}
		v.columns = columnsFromMigrator(columns)
		v.tableKey, v.keyKnown = migratorKey(columns)
	}
	if len(v.columns.exact) == 0 {
		return nil, fmt.Errorf("orm: VerifyModel found no columns for %s: the table does not exist, "+
			"or this login cannot see it", table)
	}

	dialect := s.Query().Dialect()
	v.checkTriggers(model, dialect)
	v.checkPrimaryKey()
	v.checkKeysReadable(model, dialect)
	for _, name := range entitySchema.DBNames {
		v.checkField(entitySchema.FieldsByDBName[name])
	}
	return v.problems, nil
}

// migratorColumns reads table's columns with gorm's migrator on ds, which has to be gorm-backed.
//
// The table is passed by name, not as the model: given a model whose table is
// schema-qualified, gorm.io/driver/sqlserver splits the schema off and reads the columns of
// every schema's table of that name. The Postgres and MySQL migrators read a name as they read
// the model. It is reached only for a datasource without table traits, which the SQL Server one
// has.
func migratorColumns(ctx context.Context, ds dbCore.IDataSource, table string) ([]gorm.ColumnType, error) {
	driver, err := ds.GetDriver()
	if err != nil {
		return nil, fmt.Errorf("orm: VerifyModel cannot check %s: %w", table, err)
	}
	db, ok := driver.(*gorm.DB)
	if !ok || db == nil {
		return nil, fmt.Errorf("orm: VerifyModel reads columns through gorm's migrator, and the %T "+
			"datasource's driver is %T, not a *gorm.DB", ds, driver)
	}
	columns, err := db.WithContext(ctx).Migrator().ColumnTypes(table)
	if err != nil {
		return nil, fmt.Errorf("orm: VerifyModel cannot read the columns of %s: %w", table, err)
	}
	return columns, nil
}

// migratorKey returns the primary key columns gorm's migrator reports, and whether it says
// which columns form the key at all.
func migratorKey(columns []gorm.ColumnType) ([]string, bool) {
	var key []string
	known := false
	for _, column := range columns {
		isKey, ok := column.PrimaryKey()
		known = known || ok
		if ok && isKey {
			key = append(key, column.Name())
		}
	}
	return key, known
}

// verification collects the problems of one VerifyModel run.
type verification struct {
	table    string
	schema   *schema.Schema
	columns  columnIndex
	tableKey []string
	keyKnown bool
	traits   *dbCore.TableTraits
	problems []ModelProblem
}

func (v *verification) report(column, kind, detail string, args ...any) {
	v.problems = append(v.problems, ModelProblem{
		Table:  v.table,
		Column: column,
		Kind:   kind,
		Detail: fmt.Sprintf(detail, args...),
	})
}

// checkTriggers reports enabled triggers the model does not declare, where the dialect's
// RETURNING is refused on a table with a trigger on the statement's action. Create sends its
// INSERT with RETURNING, so a trigger on INSERT refuses every Create; Update sends its UPDATE
// with RETURNING only for a model with grgorm:"readback" fields, so a trigger on UPDATE refuses
// every Update of such a model. A trigger on anything else refuses nothing, and is no problem.
// Only the catalog knows about triggers, so nothing is reported without traits.
func (v *verification) checkTriggers(model any, dialect dbCore.SQLDialect) {
	if v.traits == nil || hasTriggers(model) || !dbCore.ReturningBlockedByTriggers(dialect) {
		return
	}
	var refused []string
	if v.traits.InsertTriggers > 0 {
		refused = append(refused, fmt.Sprintf("%d enabled trigger(s) on INSERT, so every Create fails",
			v.traits.InsertTriggers))
	}
	if v.traits.UpdateTriggers > 0 && len(readbackFields(v.schema)) > 0 {
		refused = append(refused, fmt.Sprintf("%d enabled trigger(s) on UPDATE, so every Update fails, "+
			"since it reads the model's grgorm:\"readback\" fields with RETURNING", v.traits.UpdateTriggers))
	}
	if len(refused) == 0 {
		return
	}
	v.report("", ProblemTriggersNoOptOut,
		"%s refuses RETURNING on a table with a trigger on the statement's action, and %s has %s; "+
			"implement TableHasTriggers() returning true on %s, so Create and Update read generated "+
			"columns without it", dialectName(dialect), v.table, strings.Join(refused, ", and "),
		v.schema.ModelType.Name())
}

// checkKeysReadable reports a primary key column the server generates that Create cannot read
// back, where it reads generated columns without RETURNING: on an engine without it, and where
// the model opts out of a RETURNING that triggers block. Without RETURNING the one generated
// key an INSERT reports is the IDENTITY value of its own scope, so a server-generated key
// column is readable only when it is the table's IDENTITY and no INSTEAD OF INSERT trigger
// moves the insert into another scope. Only the catalog says which column is the IDENTITY
// and what the triggers are, so nothing is reported without traits.
func (v *verification) checkKeysReadable(model any, dialect dbCore.SQLDialect) {
	if v.traits == nil {
		return
	}
	withoutReturning := !dbCore.SupportsReturning(dialect) ||
		(dbCore.ReturningBlockedByTriggers(dialect) && hasTriggers(model))
	if !withoutReturning {
		return
	}
	for _, field := range v.schema.PrimaryFields {
		if field.Creatable && !field.AutoIncrement {
			continue // the entity assigns it, and Create keys the read-back on the entity's value
		}
		c, ok := v.columns.lookup(field.DBName)
		if !ok {
			continue // checkField reports the missing column
		}
		column := c.name
		computed := containsName(v.traits.GeneratedColumns, column)
		switch {
		case v.traits.InsteadOfInsertTriggers > 0:
			v.report(column, ProblemKeyUnreadable,
				"%s has an INSTEAD OF INSERT trigger, which inserts the row in its own scope, so the "+
					"INSERT reports no generated key and Create, which reads generated columns without "+
					"RETURNING here, cannot learn the value the server gives column %s: every Create "+
					"writes its row and fails. Only a key the entity assigns can be read back on this "+
					"table: assign %s client-side, or write the table another way",
				v.table, column, field.Name)
		case !c.identity && (c.defaulted || computed || !field.Creatable):
			// A key nothing generates at all is checkField's ProblemAutoIncrementNotIdentity.
			v.report(column, ProblemKeyUnreadable,
				"column %s is generated by the server but is not the table's IDENTITY, and Create, "+
					"which reads generated columns without RETURNING here, learns only an IDENTITY "+
					"key: every Create writes its row and fails. Assign field %s client-side, such as "+
					"a GUID from uuid.New() or a sequence value read first, and tag it so Create writes "+
					"it: primaryKey;autoIncrement:false, without gorm:\"->\"", column, field.Name)
		}
	}
}

// checkPrimaryKey reports a model whose key columns are not the table's. The table's key is the
// catalog's when there are traits, and otherwise what the migrator reports; when the migrator
// does not say which columns form the key, nothing is compared.
func (v *verification) checkPrimaryKey() {
	if !v.keyKnown || sameNames(v.tableKey, v.schema.PrimaryFieldDBNames) {
		return
	}

	modelKey := "no primary key"
	if len(v.schema.PrimaryFieldDBNames) > 0 {
		modelKey = "(" + strings.Join(v.schema.PrimaryFieldDBNames, ", ") + ")"
	}
	if len(v.tableKey) == 0 {
		v.report("", ProblemPrimaryKeyMismatch,
			"%s has no primary key and the model's is %s, so nothing guarantees that Update, Delete "+
				"or Find address one row", v.table, modelKey)
		return
	}
	v.report("", ProblemPrimaryKeyMismatch,
		"the primary key of %s is (%s) and the model's is %s; Update, Delete and Find address rows "+
			"by the model's key, so tag every column of the table's key primaryKey and no other",
		v.table, strings.Join(v.tableKey, ", "), modelKey)
}

// checkField runs the column checks for one mapped field.
func (v *verification) checkField(field *schema.Field) {
	if field == nil || field.DBName == "" {
		return
	}
	column, ok := v.columns.lookup(field.DBName)
	if !ok {
		v.report(field.DBName, ProblemMissingColumn,
			"field %s maps column %s, which %s does not have; fix the column: tag, or leave the field "+
				"out of the model", field.Name, field.DBName, v.table)
		return
	}
	name := column.name
	written := field.Creatable || field.Updatable

	if column.nullable && written && !canHoldNull(field.FieldType) {
		v.report(name, ProblemNullableIntoValue,
			"column %s is NULLable and field %s is a %s, which cannot hold NULL: a NULL reads as the "+
				"zero value, and a full-row Update writes that value back over it; use a pointer, or a "+
				"sql.Null type such as sql.NullString or uuid.NullUUID", name, field.Name, field.FieldType)
	}

	generated := v.traits != nil && containsName(v.traits.GeneratedColumns, name)
	if generated && written {
		v.report(name, ProblemGeneratedWritable,
			"column %s is computed by the server (a rowversion, a computed or a GENERATED ALWAYS "+
				"column), which refuses to be written, and field %s is written by %s; tag it gorm:\"->\" "+
				"and, to have it read back after each write, grgorm:\"readback\"",
			name, field.Name, writtenBy(field))
	}

	if column.identity && field.Creatable && !(field.PrimaryKey && field.AutoIncrement) {
		if field.PrimaryKey {
			v.report(name, ProblemIdentityNotAuto,
				"column %s is an IDENTITY key and field %s is not autoIncrement, so Create sends its "+
					"value, which the server refuses, and never reads the generated key back; tag it "+
					"primaryKey;autoIncrement and leave it zero on Create", name, field.Name)
		} else {
			v.report(name, ProblemIdentityNotAuto,
				"column %s is an IDENTITY column and field %s is written by Create, which the server "+
					"refuses; tag it gorm:\"->\" and grgorm:\"readback\"", name, field.Name)
		}
	}

	// Only the catalog says for certain that a column is not the IDENTITY; the migrator's flag
	// is a hint some engines leave unset for a column the server does generate.
	if v.traits != nil && field.PrimaryKey && field.AutoIncrement && field.Creatable &&
		!column.identity && !column.defaulted && !generated {
		identityIs := "has no IDENTITY column"
		if v.traits.IdentityColumn != "" {
			identityIs = "has " + v.traits.IdentityColumn + " for its IDENTITY"
		}
		v.report(name, ProblemAutoIncrementNotIdentity,
			"field %s is an autoIncrement key — gorm makes every single integer key one unless it is "+
				"tagged autoIncrement:false — but %s %s and column %s has no default, so nothing on "+
				"the server generates it, and Create leaves a zero key out of the INSERT for the server "+
				"to fill; tag it primaryKey;autoIncrement:false and assign it on Create",
			field.Name, v.table, identityIs, name)
	}

	// A field Create does not write cannot override a default, and neither can an auto-increment
	// key, which Create leaves out whenever it is zero. A grgorm:"readback" tag is no exemption: it
	// reads the column back, and does not stop the write that overrides the default.
	if column.identity || generated || !field.Creatable || (field.PrimaryKey && field.AutoIncrement) {
		return
	}
	if column.defaulted {
		v.report(name, ProblemServerDefaultWritten,
			"column %s has a server default and Create writes field %s, so the default never applies "+
				"and an unset field stores its zero value; always set the field, or tag it gorm:\"->\" "+
				"and grgorm:\"readback\" if the server always chooses the value (grgorm:\"readback\" "+
				"alone does not stop the write)", name, field.Name)
	}
}

// tableColumn is what VerifyModel knows about one column of the table: its name as the table
// spells it, whether it allows NULL, whether it is the IDENTITY (auto-increment) column, and
// whether it has a server default.
type tableColumn struct {
	name      string
	nullable  bool
	identity  bool
	defaulted bool
}

// columnIndex finds a column by name: exactly, else ignoring case, which is how SQL Server's
// default collation and MySQL resolve a name, and how Postgres reports an unquoted one.
type columnIndex struct {
	exact  map[string]tableColumn
	folded map[string]tableColumn
}

func newColumnIndex(columns []tableColumn) columnIndex {
	index := columnIndex{exact: map[string]tableColumn{}, folded: map[string]tableColumn{}}
	for _, column := range columns {
		index.exact[column.name] = column
		if _, taken := index.folded[strings.ToLower(column.name)]; !taken {
			index.folded[strings.ToLower(column.name)] = column
		}
	}
	return index
}

// columnsFromTraits indexes the columns the catalog reported.
func columnsFromTraits(traits dbCore.TableTraits) columnIndex {
	columns := make([]tableColumn, 0, len(traits.Columns))
	for _, name := range traits.Columns {
		columns = append(columns, tableColumn{
			name:      name,
			nullable:  containsName(traits.NullableColumns, name),
			identity:  traits.IdentityColumn != "" && strings.EqualFold(traits.IdentityColumn, name),
			defaulted: containsName(traits.DefaultColumns, name),
		})
	}
	return newColumnIndex(columns)
}

// columnsFromMigrator indexes the columns gorm's migrator reported. A flag the migrator does
// not report reads as false.
func columnsFromMigrator(reported []gorm.ColumnType) columnIndex {
	columns := make([]tableColumn, 0, len(reported))
	for _, column := range reported {
		c := tableColumn{name: column.Name()}
		if nullable, ok := column.Nullable(); ok {
			c.nullable = nullable
		}
		if auto, ok := column.AutoIncrement(); ok {
			c.identity = auto
		}
		if value, ok := column.DefaultValue(); ok && strings.TrimSpace(value) != "" {
			c.defaulted = true
		}
		columns = append(columns, c)
	}
	return newColumnIndex(columns)
}

func (i columnIndex) lookup(name string) (tableColumn, bool) {
	if column, ok := i.exact[name]; ok {
		return column, true
	}
	column, ok := i.folded[strings.ToLower(name)]
	return column, ok
}

// scannerType is sql.Scanner's reflect.Type, for canHoldNull.
var scannerType = reflect.TypeFor[sql.Scanner]()

// canHoldNull reports whether a field of type t can tell NULL from a value: a pointer, a slice
// (a nil []byte is a NULL varbinary), a map or an interface, or a Scanner struct with a Valid
// field, the shape of sql.NullString, sql.Null[T] and uuid.NullUUID.
func canHoldNull(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
		return true
	case reflect.Struct:
		valid, ok := t.FieldByName("Valid")
		return ok && valid.Type.Kind() == reflect.Bool && reflect.PointerTo(t).Implements(scannerType)
	}
	return false
}

// writtenBy says which writes write a field.
func writtenBy(field *schema.Field) string {
	switch {
	case field.Creatable && field.Updatable:
		return "Create and Update"
	case field.Creatable:
		return "Create"
	default:
		return "Update"
	}
}

// sameNames reports whether a and b hold the same names, in any order, ignoring case.
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, name := range a {
		if !containsName(b, name) {
			return false
		}
	}
	return true
}

func containsName(names []string, name string) bool {
	for _, each := range names {
		if strings.EqualFold(each, name) {
			return true
		}
	}
	return false
}

func dialectName(d dbCore.SQLDialect) string {
	if d == nil {
		return "the dialect"
	}
	return d.Name()
}
