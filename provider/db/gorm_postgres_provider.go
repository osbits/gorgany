package db

import (
	"fmt"
	db2 "gorgany/db"
	"gorgany/db/gorm/plugins"
	"gorgany/provider"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type GormPostgresProvider struct {
}

func NewGormPostgresProvider() *GormPostgresProvider {
	return &GormPostgresProvider{}
}

func (thiz GormPostgresProvider) InitProvider() {
	dsn := thiz.GetDataSource()
	config := provider.FrameworkRegistrar.GetDbConfig(db2.PostgreSQL)
	gormConfig := postgres.Config{DSN: dsn, PreferSimpleProtocol: config["PreferSimpleProtocol"].(bool)}
	db, err := gorm.Open(postgres.New(gormConfig), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		panic(err)
	}

	db.Callback().Query().Before("gorm:query").Register("extended_model_processor_add_type_to_where", plugins.ExtendedModelProcessor{}.AddModelTypeToWhere)
	db.Callback().Create().After("gorm:after_create").Register("after_create", plugins.ExtendedModelProcessor{}.AddModelTypeAfterInsert)

	db2.SetDbInstance("gorm", db2.GormWrapper{Gorm: db})
}

func (thiz GormPostgresProvider) GetDataSource() string {
	config := provider.FrameworkRegistrar.GetDbConfig(db2.PostgreSQL)
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config["Host"].(string), config["Port"].(int), config["Username"].(string), config["Password"].(string), config["DBName"].(string), config["SSLMode"].(string))
}
