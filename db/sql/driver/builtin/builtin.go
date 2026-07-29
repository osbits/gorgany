// Package builtin registers the datasource drivers that ship with the framework.
//
// It exists as its own package so the registry (db/sql/driver) stays free of any
// dependency on the concrete drivers, and the drivers stay free of any dependency
// on the registry. Importing it for side effects registers everything:
//
//	import _ "github.com/osbits/gorgany/db/sql/driver/builtin"
//
// provider.DbProvider does that already, so an app using the standard bootstrap
// needs no import of its own. An app adding its own engine calls
// driver.Register("<name>", ctor) from its provider's Register phase.
package builtin

import (
	dsconfig "github.com/osbits/gorgany/db/sql/config"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/osbits/gorgany/db/sql/driver"
	mysql "github.com/osbits/gorgany/db/sql/gorm/mysql/v2"
	postgres "github.com/osbits/gorgany/db/sql/gorm/postgres/v2"
)

// Driver names as written under `databases.<name>.driver` in the app config.
const (
	PostgresGorm = "postgres_gorm"
	MySQLGorm    = "mysql_gorm"
)

func init() {
	driver.Register(PostgresGorm, func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return postgres.NewDataSourceWithConfig(cfg)
	})
	driver.Register(MySQLGorm, func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return mysql.NewDataSourceWithConfig(cfg)
	})
}
