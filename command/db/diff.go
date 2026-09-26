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
// Add a dialect here only if its DDL rolls back.
var TransactionalDDLDialects = []string{"postgres"}

type DiffCommand struct {
	// Datasource declares --datasource to command.Resolver, whose flag parser rejects
	// every flag a command does not declare: without it `cli db:diff --datasource=x`
	// exited 2 before Execute ran. Execute reads the value through SelectedDatasource,
	// as db:migrate does.
	Datasource string `command:"flag,name=datasource,default=default,description=datasource to diff against (a key under databases)"`

	modelStructAlreadyAdded map[string]bool
	pivotTables             map[string]bool
	domainContext           core.IDomainContext `container:"inject"`
	dbContext               core.IDBContext     `container:"inject"`
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
// A datasource whose dialect is not in TransactionalDDLDialects is refused before
// anything runs against it.
func (thiz DiffCommand) Execute(ctx context.Context) {
	thiz.modelStructAlreadyAdded = make(map[string]bool)
	thiz.pivotTables = make(map[string]bool)

	datasource := SelectedDatasource()

	gormDb, err := ResolveGorm(thiz.dbContext, datasource)
	if err != nil {
		panic(err)
	}

	if err := requireTransactionalDDL(gormDb, datasource); err != nil {
		panic(err)
	}

	fmt.Printf("Diffing against datasource %q\n", datasource)

	tx := gormDb.Begin()
	defer tx.Rollback()

	var statements []string
	tx.Callback().Raw().Register("record_migration", func(tx *gorm.DB) {
		statements = append(statements, tx.Statement.SQL.String())
	})

	moduleName := util.ModuleName()
	modelsMap := thiz.domainContext.GetDomains()

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

	for _, model := range modelsMap {
		err := thiz.migrateModel(model, tx)
		if err != nil {
			rType := reflect.TypeOf(model)
			fmt.Printf("Domain: %s, error: %v", rType.Name(), err)
			return
		}
	}

	migrator := tx.Migrator()
	for _, model := range modelsMap {
		rType := reflect.TypeOf(model)
		err := thiz.migrateModelConstraints(rType, &statements, migrator)
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

func (thiz DiffCommand) migrateModelConstraints(rModel reflect.Type, statements *[]string, migrator gorm.Migrator) error {
	namingStrategyService := schema.NamingStrategy{}
	alreadyExtends := false
	for i := 0; i < rModel.NumField(); i++ {
		rField := rModel.Field(i)

		if rField.Anonymous && rField.Type.Kind() == reflect.Struct && orm.IsParamInTagExists(rField.Tag, core.GeneratedDomainTagValue) {
			err := thiz.migrateModelConstraints(rField.Type, statements, migrator)
			if err != nil {
				return err
			}
			continue
		}

		if rField.Anonymous && rField.Type.Kind() == reflect.Struct && orm.IsParamInTagExists(rField.Tag, core.GorganyORMExtends) {
			if alreadyExtends {
				return fmt.Errorf("Gorgany ORM only supports one struct extension!")
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
				*statements = append(*statements, fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS %s varchar(255)", tableName, structColumn))
			}

			alreadyExtends = true

			thiz.modelStructAlreadyAdded[tableName] = true

			err := thiz.migrateModelConstraints(rField.Type, statements, migrator)
			if err != nil {
				return err
			}

			continue
		}

		indirectRModel := util.IndirectType(rModel)
		rvModel := reflect.New(indirectRModel)
		model := rvModel.Interface()

		if migrator.HasConstraint(model, rField.Name) {
			continue
		}

		if err := migrator.CreateConstraint(model, rField.Name); err != nil {
			panic(err)
		}

	}
	return nil
}

// requireTransactionalDDL refuses a datasource whose dialect would commit the DDL that
// db:diff runs to find differences. See TransactionalDDLDialects.
func requireTransactionalDDL(gormDb *gorm.DB, datasource string) error {
	dialect := "unknown"
	if gormDb != nil && gormDb.Config != nil && gormDb.Dialector != nil {
		dialect = gormDb.Dialector.Name()
	}

	if util.InArray(dialect, TransactionalDDLDialects) {
		return nil
	}

	return fmt.Errorf(
		"db:diff cannot run against datasource %q: its dialect (%s) commits DDL immediately, "+
			"so the CREATE TABLE and ALTER TABLE statements db:diff runs to find differences "+
			"would change that database instead of being rolled back. db:diff supports %s; "+
			"write migrations for this datasource by hand",
		datasource, dialect, strings.Join(TransactionalDDLDialects, ", "))
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
