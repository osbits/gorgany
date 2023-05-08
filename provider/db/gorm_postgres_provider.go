package db

import (
	"fmt"
	db2 "gorgany/db"
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
	dataSource := thiz.GetDataSource()
	db, err := gorm.Open(postgres.Open(dataSource), &gorm.Config{})
	if err != nil {
		panic(err)
	}

	db2.SetDbInstance("gorm", db2.GormWrapper{Gorm: db})
}

func (thiz GormPostgresProvider) GetDataSource() string {
	config := provider.FrameworkRegistrar.GetDbConfig(db2.PostgreSQL)
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config["Host"].(string), config["Port"].(int), config["Username"].(string), config["Password"].(string), config["DBName"].(string), config["SSLMode"].(string))
}
