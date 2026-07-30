// Package postgres registers the framework's PostgreSQL datasource driver, and nothing
// else.
//
// Import it for its side effects:
//
//	import _ "github.com/osbits/gorgany/v2/db/sql/driver/postgres"
//
// It exists so a Postgres-only app does not link the MySQL driver. driver/builtin
// registers both, and provider.DbProvider used to import builtin unconditionally — so
// every app that used the standard bootstrap pulled in gorm.io/driver/mysql,
// go-sql-driver/mysql and filippo.io/edwards25519 whether or not it would ever speak
// MySQL. Not a defect, but it is dependency surface and attack surface an app did not
// choose.
//
// Use driver/builtin instead if you want both engines, or want the pre-F7 behaviour.
package postgres

import (
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	postgres "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
)

// Name is the driver name as written under `databases.<name>.driver`.
const Name = "postgres_gorm"

func init() {
	driver.Register(Name, func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return postgres.NewDataSourceWithConfig(cfg)
	})
}
