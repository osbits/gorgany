package db

import (
	"bytes"
	"context"
	"fmt"
	"github.com/osbits/gorgany/v2"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/orm"
	"github.com/osbits/gorgany/v2/db/sql/gorm/plugin"
	model2 "github.com/osbits/gorgany/v2/service/cache"
	"github.com/osbits/gorgany/v2/util"
	"go/format"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"
)

const MigrationDir = "db/migration"

var AllowedTypesToMigrate = []string{"gorm.io/gorm.DeletedAt", gorgany.FrameworkGit + "/model.File", "time.Time"}

// TransactionalDDLDialects lists the GORM dialects (gorm.Dialector.Name()) whose DDL a
// transaction rolls back. db:diff runs on no others.
//
// db:diff finds differences by running the migrator's CREATE TABLE and ALTER TABLE
// statements inside a transaction, recording them, and rolling back. MySQL commits each
// DDL statement implicitly, so there the rollback discarded nothing: the diff applied the
// schema changes to the database it was only meant to compare, and the migration it wrote
// then failed on that same database with "Table ... already exists".
//
// Computing the statements without executing them (a gorm DryRun session) is not a
// substitute. The diff's later steps read back what its earlier ones created, so a dry run
// emits, for example, a UNIQUE or CHECK constraint that the CREATE TABLE above it already
// declares, and that migration fails too.
//
// SQL Server is on the list because its DDL is transactional as Postgres's is: CREATE TABLE,
// ALTER TABLE and CREATE INDEX inside a transaction are undone by its rollback, which the live
// suite shows on the statements (TestSQLServerDDLRollsBackInATransaction) and through db:diff
// itself (TestDiffRollsBackAndItsDraftRunsOnSQLServer). The statements SQL Server cannot run
// in a transaction at all, such as CREATE DATABASE, are ones the diff never sends. It is
// matched by name, so this package does not link the SQL Server driver.
//
// Add a dialect here only if its DDL rolls back.
var TransactionalDDLDialects = []string{"postgres", sqlServerDialect}

// sqlServerDialect is the name gorm's SQL Server dialector reports (gorm.Dialector.Name()).
//
// db:diff compares it as a string rather than import gorm.io/driver/sqlserver: the provider
// links this package, and an app that never speaks SQL Server must not link go-mssqldb.
const sqlServerDialect = "sqlserver"

type DiffCommand struct {
	// Datasource declares --datasource to command.Resolver, whose flag parser rejects
	// every flag a command does not declare: without it `cli db:diff --datasource=x`
	// exited 2 before Execute ran. Execute reads the value through SelectedDatasource,
	// as db:migrate does.
	Datasource string `command:"flag,name=datasource,default=default,description=datasource to diff against (a key under databases)"`

	modelStructAlreadyAdded map[string]bool
	pivotTables             map[string]bool
	// otherDomains are the registered domains of the other datasources, by struct type. The
	// domains being diffed may relate to them, and the DDL gorm derives from such a relation
	// lands on, or points at, a table of theirs. See migrateModelConstraints.
	otherDomains  map[reflect.Type]otherDomain
	domainContext core.IDomainContext `container:"inject"`
	dbContext     core.IDBContext     `container:"inject"`
}

func (thiz DiffCommand) GetName() string {
	return "db:diff"
}

// Execute diffs the registered domains against the selected datasource.
//
// It used to hard-code core.DefaultKeyInRegistrar, so in a two-datasource app it
// always diffed against the first database no matter which one the models belonged
// to. The datasource now comes from --datasource, defaulting to `default`.
//
// An external_schema or read_only datasource is refused first, on its configuration
// (see RequireOwnedDatasource): the diff runs real CREATE TABLE and ALTER TABLE in a
// transaction, and the migration it drafts is one gorgany would then be asked to run
// there. That check comes before the dialect's, so such a datasource is told why it is
// refused rather than that its dialect commits DDL. A datasource whose dialect is not in
// TransactionalDDLDialects is refused next, before anything runs against it.
//
// Only the domains that belong to the datasource (see domainsFor) are diffed. Every
// registered domain used to be, so a second datasource's tables were drafted as new
// tables for the first. Every domain in ./pkg/domain must still be registered, whichever
// datasource it belongs to. Nor is a constraint drafted that involves another datasource's
// domain, whether it would sit on that domain's table or reference it (see
// migrateModelConstraints).
func (thiz DiffCommand) Execute(ctx context.Context) {
	thiz.modelStructAlreadyAdded = make(map[string]bool)
	thiz.pivotTables = make(map[string]bool)

	datasource := SelectedDatasource()

	gormDb, err := ResolveOwnedGorm(thiz.dbContext, datasource, thiz.GetName())
	if err != nil {
		panic(err)
	}

	if err := requireTransactionalDDL(gormDb, datasource); err != nil {
		panic(err)
	}

	fmt.Printf("Diffing against datasource %q\n", datasource)

	modelsMap := thiz.domainContext.GetDomains()
	domains, otherDomains, err := domainsFor(modelsMap, datasource, thiz.dbContext)
	if err != nil {
		panic(err)
	}
	thiz.otherDomains = otherDomains

	tx := gormDb.Begin()
	defer tx.Rollback()

	var statements []string
	stopRecording := recordStatements(tx, &statements)
	defer stopRecording()

	moduleName := util.ModuleName()

	pkgInfos, err := util.ScanDir("./pkg/domain")
	if err != nil {
		panic(err)
	}

	for pkgPath, info := range pkgInfos {
		for _, st := range info.Structs {
			if st.FindAnnotationByName("@Embedded") != nil || st.FindAnnotationByName("@Abstract") != nil {
				continue
			}

			key := moduleName + "/" + pkgPath + "." + st.Name
			_, ok := modelsMap[key]
			if !ok {
				fmt.Println("New domain detected, please register it.")
				fmt.Println("Please complete one of the following steps:")
				fmt.Println("- Run `go run ./cmd/cli domains:register`, which rewrites pkg/provider/domains.go")
				fmt.Println("- Or register it yourself with IDomainContext.RegisterDomain in a provider's Boot")
				return
			}
		}
	}

	for _, model := range domains {
		err := thiz.migrateModel(model, tx)
		if err != nil {
			rType := reflect.TypeOf(model)
			fmt.Printf("Domain: %s, error: %v", rType.Name(), err)
			return
		}
	}

	migrator := tx.Migrator()
	dialect := tx.Dialector.Name()
	for _, model := range domains {
		rType := reflect.TypeOf(model)
		err := thiz.migrateModelConstraints(rType, &statements, migrator, dialect)
		if err != nil {
			fmt.Printf("Domain: %s, error: %v", rType.Name(), err)
			return
		}
	}

	thiz.generateMigration(statements, datasource)
}

func (thiz DiffCommand) migrateModel(model any, tx *gorm.DB) error {
	migrator := tx.Migrator()

	rModel := util.IndirectType(reflect.TypeOf(model))

	if migrator.HasTable(model) {
		for i := 0; i < rModel.NumField(); i++ {
			field := rModel.Field(i)

			if field.Anonymous && field.Type.Kind() == reflect.Struct && orm.IsParamInTagExists(field.Tag, core.GeneratedDomainTagValue) {
				indirectRModel := util.IndirectType(field.Type)
				rvModel := reflect.New(indirectRModel)
				generatedModel := rvModel.Interface()

				err := thiz.migrateModel(generatedModel, tx)
				if err != nil {
					return err
				}

				continue
			}

			if (field.Anonymous || util.IndirectType(field.Type).Kind() == reflect.Struct ||
				util.IndirectType(field.Type).Kind() == reflect.Slice) && !util.InArray(field.Type.PkgPath()+"."+field.Type.Name(), AllowedTypesToMigrate) {
				continue
			}

			if migrator.HasColumn(model, field.Name) {
				continue
			}

			err := migrator.AddColumn(model, field.Name)
			if err != nil {
				return err
			}
		}
	} else {
		err := migrator.CreateTable(model)
		if err != nil {
			return err
		}
	}

	return thiz.migratePivatTable(model, tx)
}

func (thiz DiffCommand) migratePivatTable(model any, tx *gorm.DB) error {
	parseScheme := model2.GetDomainSchemeCache().ParseDomain(model)
	many2manies := parseScheme.Relationships.Many2Many

	for _, relation := range many2manies {
		if _, ok := thiz.pivotTables[relation.JoinTable.Table]; ok {
			continue
		}

		indirectRModel := util.IndirectType(relation.Field.FieldType)
		rvModel := reflect.New(indirectRModel)
		relationModel := rvModel.Interface()

		err := tx.Table(relation.JoinTable.Table).AutoMigrate(model, relationModel)
		if err != nil {
			return err
		}
		thiz.pivotTables[relation.JoinTable.Table] = true
	}
	return nil
}

// migrateModelConstraints drafts the constraints of rModel's relations, and the struct column
// of the table it extends.
//
// A relation to a domain of another datasource drafts neither. gorm puts a has-one or has-many
// foreign key on the related table and a belongs-to one on this table, referencing the
// related one, so either way the statement names the other datasource's table. The draft runs
// on the datasource being diffed. Where the two are separate databases, that table is not
// there, and the ALTER TABLE failed the diff with the driver's error; where they share one,
// it was DDL on, or a new dependency of, a table whose datasource may be external_schema.
// Nor is a table extended that belongs to another datasource. Each one skipped is printed,
// and a constraint between datasources that share a database is written by hand.
//
// dialect is the diffed datasource's gorm dialect name, which decides how the struct column
// is added (see structColumnDDL).
func (thiz DiffCommand) migrateModelConstraints(rModel reflect.Type, statements *[]string, migrator gorm.Migrator, dialect string) error {
	namingStrategyService := schema.NamingStrategy{}
	alreadyExtends := false
	for i := 0; i < rModel.NumField(); i++ {
		rField := rModel.Field(i)

		if rField.Anonymous && rField.Type.Kind() == reflect.Struct && orm.IsParamInTagExists(rField.Tag, core.GeneratedDomainTagValue) {
			err := thiz.migrateModelConstraints(rField.Type, statements, migrator, dialect)
			if err != nil {
				return err
			}
			continue
		}

		if rField.Anonymous && rField.Type.Kind() == reflect.Struct && orm.IsParamInTagExists(rField.Tag, core.GorganyORMExtends) {
			if alreadyExtends {
				return fmt.Errorf("Gorgany ORM only supports one struct extension!")
			}

			// The parent's table is another datasource's, so neither its struct column nor
			// its constraints are this diff's to draft: the draft runs on the datasource
			// being diffed, and the parent's schema may not be gorgany's at all.
			if other, ok := thiz.otherDomains[rField.Type]; ok {
				alreadyExtends = true
				fmt.Printf("Skipping the %s column for %s: the domain it extends, %s, belongs to datasource %q\n",
					plugin.StructModelColumn(), rModel.Name(), other.key, other.datasource)
				continue
			}

			tableName := namingStrategyService.TableName(rField.Type.Name())

			if _, ok := thiz.modelStructAlreadyAdded[tableName]; ok {
				continue
			}

			// Through the migrator, so the check runs on the selected datasource and in
			// its current schema. It used to query information_schema through
			// db.Builder(), which is always `default`, with no schema filter. It also
			// checked for StructModelColumn() and then added StructDefaultColumn, so a
			// configured column name was never the one added.
			structColumn := plugin.StructModelColumn()
			if !migrator.HasColumn(tableName, structColumn) {
				*statements = append(*statements, structColumnDDL(dialect, tableName, structColumn))
			}

			alreadyExtends = true

			thiz.modelStructAlreadyAdded[tableName] = true

			err := thiz.migrateModelConstraints(rField.Type, statements, migrator, dialect)
			if err != nil {
				return err
			}

			continue
		}

		indirectRModel := util.IndirectType(rModel)
		rvModel := reflect.New(indirectRModel)
		model := rvModel.Interface()

		if other, ok := thiz.otherDatasourceConstraint(model, rField.Name); ok {
			fmt.Printf("Skipping the constraint of %s.%s: it involves domain %s, which belongs to datasource %q\n",
				rModel.Name(), rField.Name, other.key, other.datasource)
			continue
		}

		if migrator.HasConstraint(model, rField.Name) {
			continue
		}

		if err := migrator.CreateConstraint(model, rField.Name); err != nil {
			panic(err)
		}

	}
	return nil
}

// structColumnDDL renders the statement that adds column, the struct column, to table, the
// table of a domain another one extends.
//
// On Postgres, and on any dialect it does not know, it is the statement db:diff has always
// drafted, unchanged. SQL Server has neither ADD COLUMN nor IF NOT EXISTS in ALTER TABLE, and
// refuses that statement as a syntax error, so there it is ADD with the column bracketed and
// declared nullable. nvarchar rather than varchar, because SQL Server's varchar stores the code
// page of the column's collation instead of Unicode. NULL is written out because a column
// added without it takes its nullability from session settings. The check that the column is
// missing ran when the diff did, so the draft needs no IF of its own on either engine.
func structColumnDDL(dialect, table, column string) string {
	if dialect == sqlServerDialect {
		return fmt.Sprintf("ALTER TABLE %s ADD %s nvarchar(255) NULL", bracketIdentifier(table), bracketIdentifier(column))
	}
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s varchar(255)", table, column)
}

// bracketIdentifier quotes name as one SQL Server identifier, doubling any closing bracket in
// it. Brackets quote whatever QUOTED_IDENTIFIER is set to, where double quotes depend on it.
func bracketIdentifier(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

// otherDomain is a registered domain of a datasource other than the one being diffed: its
// registration key, and that datasource.
type otherDomain struct {
	key        string
	datasource string
}

// domainsFor returns the registered domains that belong to datasource, in the order of
// their registration keys, and prints a line for each one it leaves out. It also returns
// the ones it leaves out, by struct type, for migrateModelConstraints.
//
// A domain belongs to the datasource DomainDatasourceOf names, with one exception. A domain
// that declares none belongs to `default`, but in an app with no `default` it is diffed
// against the selected datasource, as every domain was before db:diff looked at datasources,
// and a line says so: such an app kept its domains on named datasources and diffed them with
// --datasource, and refusing them now would fail every diff until each domain declared one.
//
// The order is fixed so that two diffs of the same schema draft the same migration; it
// used to follow map iteration, which Go randomises. A domain naming a datasource that is
// not configured is an error rather than a skip, as a migration naming one is (see
// PartitionByDatasource): it would never be diffed anywhere, and never say so. So is a
// domain whose naming method panics when asked. A domain of an external_schema or
// read_only datasource is skipped like any other datasource's: its rows may be read (and,
// unless read_only, written) through the ORM, and only its schema is off limits.
func domainsFor(domains map[string]any, datasource string, dbContext core.IDBContext) ([]any, map[reflect.Type]otherDomain, error) {
	keys := make([]string, 0, len(domains))
	for key := range domains {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	isConfigured := IsConfigured(dbContext)
	selected := make([]any, 0, len(keys))
	others := make(map[reflect.Type]otherDomain)
	undeclared := 0
	for _, key := range keys {
		model := domains[key]
		target, declared, err := declaredDomainDatasource(model)
		if err != nil {
			return nil, nil, fmt.Errorf("domain %s: %w", key, err)
		}
		if !declared {
			target = core.DefaultKeyInRegistrar
			if !isConfigured(target) {
				target = datasource
				undeclared++
			}
		}

		if target == datasource {
			selected = append(selected, model)
			continue
		}
		if !isConfigured(target) {
			return nil, nil, fmt.Errorf(
				"domain %s belongs to datasource %q, which is not configured under the `databases` key",
				key, target)
		}
		fmt.Printf("Skipping domain %s: it belongs to datasource %q, not %q\n", key, target, datasource)
		if model == nil {
			continue
		}
		// A type registered under two keys is named by the first.
		if rType := util.IndirectType(reflect.TypeOf(model)); others[rType] == (otherDomain{}) {
			others[rType] = otherDomain{key: key, datasource: target}
		}
	}

	if undeclared > 0 {
		fmt.Printf("No %q datasource is configured, so the %d domain(s) that declare no datasource are "+
			"diffed against %q; give each a DbConnectionName() to diff it against its own datasource\n",
			core.DefaultKeyInRegistrar, undeclared, datasource)
	}
	return selected, others, nil
}

// otherDatasourceConstraint reports the domain of another datasource that the constraint of
// model's field involves, as the table it would sit on or the table it would reference.
//
// It finds the constraint as gorm's migrator does for a field's name: the one the
// relationship declared by that field defines.
func (thiz DiffCommand) otherDatasourceConstraint(model any, field string) (otherDomain, bool) {
	if len(thiz.otherDomains) == 0 {
		return otherDomain{}, false
	}

	parsed := model2.GetDomainSchemeCache().ParseDomain(model)
	if parsed == nil {
		return otherDomain{}, false
	}
	relationField := parsed.LookUpField(field)
	if relationField == nil {
		return otherDomain{}, false
	}

	for _, relation := range parsed.Relationships.Relations {
		if relation.Field != relationField {
			continue
		}
		constraint := relation.ParseConstraint()
		if constraint == nil {
			continue
		}
		for _, side := range []*schema.Schema{constraint.Schema, constraint.ReferenceSchema} {
			if side == nil {
				continue
			}
			if other, ok := thiz.otherDomains[side.ModelType]; ok {
				return other, true
			}
		}
		break
	}
	return otherDomain{}, false
}

// requireTransactionalDDL refuses a datasource whose dialect would commit the DDL that
// db:diff runs to find differences. See TransactionalDDLDialects.
//
// The message says why for the dialects known to commit DDL, and says only that the dialect
// is not supported for one nobody has checked, rather than claim something about an engine
// that may not be true of it.
func requireTransactionalDDL(gormDb *gorm.DB, datasource string) error {
	dialect := "unknown"
	if gormDb != nil && gormDb.Config != nil && gormDb.Dialector != nil {
		dialect = gormDb.Dialector.Name()
	}

	if util.InArray(dialect, TransactionalDDLDialects) {
		return nil
	}

	supported := strings.Join(TransactionalDDLDialects, ", ")
	if dialect == "mysql" {
		return fmt.Errorf(
			"db:diff cannot run against datasource %q: its dialect (%s) commits DDL immediately, "+
				"so the CREATE TABLE and ALTER TABLE statements db:diff runs to find differences "+
				"would change that database instead of being rolled back. db:diff supports %s; "+
				"write migrations for this datasource by hand",
			datasource, dialect, supported)
	}
	return fmt.Errorf(
		"db:diff cannot run against datasource %q: its dialect (%s) is not one whose DDL is known "+
			"to roll back, and db:diff finds differences by running CREATE TABLE and ALTER TABLE "+
			"in a transaction and rolling it back. db:diff supports %s; "+
			"write migrations for this datasource by hand",
		datasource, dialect, supported)
}

func (thiz DiffCommand) generateMigration(statements []string, datasource string) {
	if len(statements) == 0 {
		fmt.Println("DB has actual state")
		return
	}

	fileName, source, err := renderMigration(statements, datasource, time.Now())
	if err != nil {
		panic(err)
	}

	// 0755 and 0644, not os.ModePerm: a source file has no business being executable or
	// world-writable.
	if err := os.MkdirAll(MigrationDir, 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path.Join(MigrationDir, fileName), source, 0o644); err != nil {
		panic(err)
	}

	fmt.Printf("File %s/%s successfully generated\n", MigrationDir, fileName)
}

// recordMigrationCallback is the name the recorder is registered under; see recordStatements.
const recordMigrationCallback = "record_migration"

// recordStatements makes tx append every statement it executes to statements, as the draft
// will run it (see draftStatement). A statement that cannot be drafted fails where it ran,
// so the diff stops instead of writing a migration that could not work. It returns the
// function that stops the recording, which the diff defers.
//
// gorm keeps callbacks on the *gorm.DB the transaction was begun from, which every session of
// the datasource shares, not on the transaction. So the recorder passes over any statement
// that did not run on tx's own connection: another session's traffic on the pool is not the
// diff's, and is neither drafted nor refused. It used to be drafted. And the recorder is
// removed when the diff returns. It used to stay registered, and since it refuses a statement
// that binds anything but a string, every later Exec on that datasource in the same process
// that bound a number, a time or a bool would fail with db:diff's error, on Postgres as on SQL
// Server. The CLI exits after the command, so a test, an embedding tool or anything else
// sharing the pool is what would meet it.
func recordStatements(tx *gorm.DB, statements *[]string) (stop func()) {
	diffConn := tx.Statement.ConnPool
	_ = tx.Callback().Raw().Register(recordMigrationCallback, func(db *gorm.DB) {
		if db.Statement.ConnPool != diffConn {
			return
		}
		statement, err := draftStatement(db)
		if err != nil {
			_ = db.AddError(err)
			return
		}
		*statements = append(*statements, statement)
	})
	// A fresh Raw(): Register and Remove each record themselves on the value they are called on.
	return func() { _ = tx.Callback().Raw().Remove(recordMigrationCallback) }
}

// draftStatement returns the statement tx executed in the form the draft runs it in, which is
// dbGorm.Exec with no arguments.
//
// A statement executed without bound arguments is returned as it is; on Postgres that is every
// statement the diff runs, so its drafts are what they always were. The SQL Server migrator
// binds three in one place: a column comment is stored by sp_addextendedproperty with the
// schema, table and column passed as @p1, @p2 and @p3. Recorded as it was, that statement
// reached the draft with the placeholders and nothing to fill them, and failed when db:migrate
// ran it. Those arguments are names, so they are written into the statement as string
// literals, with the dialect's own quoting. A statement that binds anything other than a
// string is refused, since no literal is known to stand in for it faithfully.
func draftStatement(tx *gorm.DB) (string, error) {
	sql := tx.Statement.SQL.String()
	if len(tx.Statement.Vars) == 0 {
		return sql, nil
	}
	for _, value := range tx.Statement.Vars {
		if _, ok := value.(string); !ok {
			return "", fmt.Errorf(
				"db:diff cannot draft %q: it binds a %T, which a migration that runs statements "+
					"without arguments cannot pass; write this change by hand", sql, value)
		}
	}
	return tx.Dialector.Explain(sql, tx.Statement.Vars...), nil
}

// renderMigration returns the file name and the gofmt'd source of a migration that runs
// statements, in order, on datasource.
//
// The template used to run the statements through dbGorm.DB(), which on the transaction
// db:migrate passes in returns the underlying pool, so they ran outside the transaction:
// a failure left the earlier statements applied and the migration unrecorded. Its Down()
// returned nil, so `db:migrate down` recorded a rollback that changed nothing. And every
// `"` was stripped from the statements so they could be pasted between quotes, which
// broke identifiers that need quoting, such as reserved words and mixed-case names.
func renderMigration(statements []string, datasource string, now time.Time) (string, []byte, error) {
	_, callerFilename, _, _ := runtime.Caller(0)
	dir := filepath.Dir(callerFilename)

	content, err := os.ReadFile(filepath.Join(dir, "../../resource/template/command/db_diff.html"))
	if err != nil {
		return "", nil, err
	}

	tpl, err := template.New("db_diff").
		Funcs(template.FuncMap{"goString": goStringLiteral}).
		Parse(string(content))
	if err != nil {
		return "", nil, err
	}

	name := now.Format("20060102_150405.000")
	writer := new(bytes.Buffer)
	err = tpl.Execute(writer, map[string]any{
		"Name":                name,
		"StructName":          "Migration" + now.Format("20060102150405"),
		"Datasource":          datasource,
		"Statements":          statements,
		"NotReversible":       fmt.Sprintf("migration %s is not reversible: db:diff does not generate Down()", name),
		"FrameworkModuleName": gorgany.FrameworkGit,
	})
	if err != nil {
		return "", nil, err
	}

	// gofmt the output. The template's indentation is not gofmt's, and a generated file
	// that fails `gofmt -l` fails every CI format gate the moment it is committed.
	source, err := format.Source(writer.Bytes())
	if err != nil {
		return "", nil, fmt.Errorf("db:diff: the generated migration does not parse: %w", err)
	}

	return now.Format("20060102150405") + "_migration.go", source, nil
}

// goStringLiteral renders s as a Go string literal: plainly quoted when nothing in it
// needs escaping, backquoted when it can be, so DDL with quoted identifiers stays
// readable, and quoted with escapes otherwise.
func goStringLiteral(s string) string {
	quoted := strconv.Quote(s)
	if quoted == `"`+s+`"` || !strconv.CanBackquote(s) {
		return quoted
	}
	return "`" + s + "`"
}
