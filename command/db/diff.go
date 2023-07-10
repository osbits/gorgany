package db

import (
	"bytes"
	"fmt"
	"gorgany/db"
	"gorgany/provider"
	"gorgany/util"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"
)

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
				fmt.Println("- Run `go run cmd/cli.go models:register`")
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

	thiz.generateMigration(statements)
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

	err = os.WriteFile("db/migration/"+fileName, writer.Bytes(), os.ModePerm)
	if err != nil {
		panic(err)
	}

	fmt.Printf("File db/migration/%s successfully generated\n", fileName)
}
