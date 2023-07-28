package db

import (
	"bytes"
	"errors"
	"fmt"
	"gorgany/db"
	"gorgany/provider"
	"gorgany/util"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"text/template"
	"time"
)

const MigrationDir = "db/migration"

type DiffCommand struct {
}

func (thiz DiffCommand) GetName() string {
	return "db:diff"
}

func (thiz DiffCommand) Execute() {
	gormInstance := db.GetWrapper("gorm").GetInstance().(*gorm.DB)

	tx := gormInstance.Begin()
	defer tx.Rollback()

	var statements []string
	tx.Callback().Raw().Register("record_migration", func(tx *gorm.DB) {
		statements = append(statements, tx.Statement.SQL.String())
	})

	moduleName := util.ModuleName()
	modelsMap := provider.FrameworkRegistrar.GetDomains()

	pkgInfos, err := util.ScanDir("./pkg/domain")
	if err != nil {
		panic(err)
	}

	for pkgPath, info := range pkgInfos {
		for _, st := range info.Structs {
			key := moduleName + "/" + pkgPath + "." + st.Name
			_, ok := modelsMap[key]
			if !ok {
				fmt.Println("New domain detected, please register it.")
				fmt.Println("Please complete one of the following steps:")
				fmt.Println("- Register it manually, just add it to models registrar(registrar/models.go)")
				fmt.Println("- Run `go run cmd/cli.go domains:register`")
				return
			}
		}
	}

	models := make([]interface{}, 0)

	for _, model := range modelsMap {
		models = append(models, model)
	}

	if err := tx.Migrator().AutoMigrate(models...); err != nil {
		fmt.Println(err)
		return
	}

	namingStrategyService := schema.NamingStrategy{}
	for _, model := range models {
		rType := reflect.TypeOf(model)
		for i := 0; i < rType.NumField(); i++ {
			rField := rType.Field(i)

			if rField.Anonymous && rField.Type.Kind() == reflect.Struct {
				tableName := namingStrategyService.TableName(rField.Type.Name())
				if !isColumnExists(tx, tableName, db.StructModelColumn) {
					statements = append(statements, fmt.Sprintf("ALTER TABLE %s ADD COLUMN IF NOT EXISTS model_struct varchar(255)", tableName))
				}
			}

			if tx.Migrator().HasConstraint(model, rField.Name) {
				continue
			}

			if err := tx.Migrator().CreateConstraint(model, rField.Name); err != nil {
				fmt.Println(err)
				return
			}
		}
	}

	thiz.generateMigration(statements)
}

func isColumnExists(db *gorm.DB, tableName string, columnName string) bool {
	var count int64
	result := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ?", tableName, columnName).Scan(&count)
	if result.Error != nil {
		return false
	}
	return count > 0
}

func (thiz DiffCommand) generateMigration(statements []string) {
	if len(statements) == 0 {
		fmt.Println("DB has actual state")
		return
	}

	ddls := make([]string, 0)
	for _, statement := range statements {
		ddls = append(ddls, strings.ReplaceAll(statement, "\"", ""))
	}

	_, callerFilename, _, _ := runtime.Caller(0)
	dir := filepath.Dir(callerFilename)

	content, err := os.ReadFile(filepath.Join(dir, "../../resource/template/command/db_diff.html"))
	if err != nil {
		panic(err)
	}

	tpl, err := template.New("db_diff").Parse(string(content))
	if err != nil {
		panic(err)
	}

	writer := new(bytes.Buffer)

	now := time.Now()
	name := now.Format("20060102_150405.000")
	structName := "Migration" + now.Format("20060102150405")
	fileName := now.Format("20060102150405") + "_migration.go"

	err = tpl.Execute(writer, map[string]any{"Name": name, "StructName": structName, "Statements": ddls})
	if err != nil {
		panic(err)
	}

	if _, err := os.Stat(MigrationDir); errors.Is(err, os.ErrNotExist) {
		err := os.Mkdir(MigrationDir, os.ModePerm)
		if err != nil {
			panic(err)
		}
	}

	err = os.WriteFile(path.Join(MigrationDir, fileName), writer.Bytes(), os.ModePerm)
	if err != nil {
		panic(err)
	}

	fmt.Printf("File %s/%s successfully generated\n", MigrationDir, fileName)
}
