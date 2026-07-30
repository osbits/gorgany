// Package mysql registers the framework's MySQL datasource driver, and nothing else.
//
// Import it for its side effects:
//
//	import _ "github.com/osbits/gorgany/db/sql/driver/mysql"
//
// See the sibling postgres package for why the engines are registered separately.
//
// Use driver/builtin instead if you want both engines.
package mysql

import (
	dsconfig "github.com/osbits/gorgany/db/sql/config"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/osbits/gorgany/db/sql/driver"
	mysql "github.com/osbits/gorgany/db/sql/gorm/mysql/v2"
)

// Name is the driver name as written under `databases.<name>.driver`.
const Name = "mysql_gorm"

func init() {
	driver.Register(Name, func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return mysql.NewDataSourceWithConfig(cfg)
	})
}
