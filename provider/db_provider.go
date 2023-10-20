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
	for key, config := range databases {
		dbType := core.DbType(key)
		configMap, ok := config.(map[string]any)
		if !ok {
			panic(fmt.Errorf("Incorrect config for '%s' database", key))
		}
		conn := thiz.resolveDb(dbType, configMap)
		if conn != nil {
			thiz.RegisterDbConnection(dbType, conn)
		} else {
			log.Log("").Infof("Connection for %s did not initialize\n", dbType)
		}
	}
}

func (thiz *DbProvider) RegisterDbConnection(dbType core.DbType, connection core.IConnection) {
	internal.GetFrameworkRegistrar().RegisterDbConnection(dbType, connection)
	log.Log("").Infof("Connection for %s initialized", dbType)
}

func (thiz *DbProvider) resolveDb(kind core.DbType, config map[string]any) core.IConnection {
	switch kind {
	case core.GormPostgresQL:
		return postgres2.NewGormPostgresConnection(config)
	case core.MongoDb:
		//todo implement me
	}
	return nil
}
