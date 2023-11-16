package provider

import (
	"fmt"
	"github.com/spf13/viper"
	"gorgany/app/core"
	postgres2 "gorgany/db/gorm/postgres"
	"gorgany/internal"
	"gorgany/log"
)

type DbProvider struct {
}

func NewDbProvider() *DbProvider {
	return &DbProvider{}
}

func (thiz *DbProvider) InitProvider() {

	databases := viper.GetStringMap("databases")
	for name, config := range databases {
		configMap, ok := config.(map[string]any)
		if !ok {
			panic(fmt.Errorf("Incorrect config for '%s' database", name))
		}
		dbTypeRaw := configMap["driver"].(string)
		if dbTypeRaw == "" {
			panic(fmt.Errorf("Incorrect driver for '%s'", name))
		}
		dbType := core.DbType(dbTypeRaw)

		conn := thiz.resolveDb(dbType, configMap)
		if conn != nil {
			thiz.RegisterDbConnection(name, conn)
		} else {
			log.Log("").Infof("Connection for %s did not initialize\n", dbType)
		}
	}
}

func (thiz *DbProvider) RegisterDbConnection(name string, connection core.IConnection) {
	internal.GetFrameworkRegistrar().RegisterDbConnection(name, connection)
	log.Log("").Infof("Connection for %s initialized", name)
}

func (thiz *DbProvider) resolveDb(kind core.DbType, config map[string]any) core.IConnection {
	switch kind {
	case core.GormPostgreSQL:
		return postgres2.NewGormPostgresConnection(config)
	case core.MongoDb:
		//todo implement me
	}
	return nil
}
