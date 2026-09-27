// Package sqlserver registers the framework's SQL Server and Azure SQL datasource driver, and
// nothing else.
//
// Import it for its side effects, in pkg/provider/bootstrap.go next to the app's other driver
// import:
//
//	import _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"
//
// It is not part of driver/builtin, and will not be. builtin is the import most Postgres and
// MySQL apps, and testsupport, already have, and bringing SQL Server into it would link
// go-mssqldb and gorm.io/driver/sqlserver into every one of them: dependency and attack
// surface they did not choose, which is the cost the per-engine split removed for MySQL.
//
// This package signs in with a SQL login only. The Microsoft Entra ID methods (interactive,
// device_code, azure_cli, azure_default, service_principal) link the Azure identity SDK, MSAL
// and a browser opener, so they register from a package of their own, which registers this
// driver as well. An app that signs in with one imports that instead:
//
//	import _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread"
package sqlserver

import (
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
)

// Name is the driver name as written under `databases.<name>.driver`.
const Name = "sqlserver_gorm"

func init() {
	driver.Register(Name, func(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
		return sqlserver.NewDataSourceWithConfig(cfg)
	})
}
