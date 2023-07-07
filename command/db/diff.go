package db

import (
	"fmt"
	"gorgany/db"
	"gorgany/provider"
	"gorgany/util"
	"gorm.io/gorm"
	"strings"
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

	ddls := make([]string, 0)
	for _, statement := range statements {
		ddl := fmt.Sprintf("\t_, err = sql.Exec(\"%s\")\n\t"+
			"if err != nil {\n\t\t"+
			"return err\n\t"+
			"}\n\t", strings.ReplaceAll(statement, "\"", ""))
		ddls = append(ddls, ddl)
	}

	fmt.Printf("return func(dbGorm *gorm.DB) error {\n\t"+
		"sql, err := dbGorm.DB()\n\t"+
		"if err != nil {\n\t\t"+
		"return err\n\t"+
		"}\n\n"+
		"%s"+
		"return nil\n"+
		"}", strings.Join(ddls, "\n\n"))
}
