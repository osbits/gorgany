package v2

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/db/sql/core"
	"git.qix.sx/gorgany/gorgany.git/db/sql/gorm/plugin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"time"
)

// dataSourceImpl implements the IDataSource interface
type dataSourceImpl struct {
	db *gorm.DB
}

// NewDataSource creates a new data source instance
func NewDataSource(config map[string]any) core.IDataSource {
	dsn := getDsn(config)

	gormConfig := postgres.Config{DSN: dsn, PreferSimpleProtocol: config["prefer_simple_protocol"].(bool)}
	db, err := gorm.Open(postgres.New(gormConfig), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		panic(err)
	}

	db.Logger.LogMode(logger.Info)

	db.Callback().Query().Before("gorm:query").Register("extended_model_processor_add_type_to_where", plugin.ExtendedModelProcessor{}.AddModelTypeToWhere)
	db.Callback().Create().After("gorm:after_create").Register("after_create", plugin.ExtendedModelProcessor{}.AddModelTypeAfterInsert)

	if propsRaw, ok := config["properties"]; ok {
		rawDb, err := db.DB()
		if err != nil {
			panic(err)
		}

		props := propsRaw.(map[string]any)

		if maxOpenConnections, ok := props["maxOpenConnections"]; ok {
			rawDb.SetMaxOpenConns(maxOpenConnections.(int))
		}

		if maxIdleConnections, ok := props["maxIdleConnections"]; ok {
			rawDb.SetMaxIdleConns(maxIdleConnections.(int))
		}

		if connectionMaxLifetime, ok := props["connectionMaxLifetime"]; ok {
			rawDb.SetConnMaxLifetime(time.Duration(connectionMaxLifetime.(int)) * time.Second)
		}

		if connectionMaxIdleLifetime, ok := props["connectionMaxIdleLifetime"]; ok {
			rawDb.SetConnMaxIdleTime(time.Duration(connectionMaxIdleLifetime.(int)) * time.Second)
		}
	}

	if config["log"].(bool) {
		db = db.Debug()
	}

	return &dataSourceImpl{
		db: db,
	}
}

func getDsn(config map[string]any) string {
	return fmt.Sprintf("host=%s port=%v user=%s password=%s dbname=%s sslmode=%s",
		config["host"], config["port"], config["username"], config["password"], config["db"], config["ssl"])
}

// Close closes the database connection
func (ds *dataSourceImpl) Close() error {
	sqlDB, err := ds.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
