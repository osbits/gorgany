package db

import (
	"gorgany/app/core"
	"gorgany/internal"
)

func Builder(kind core.DbType) core.IQueryBuilder {
	connection := internal.GetFrameworkRegistrar().GetDbConnection(kind)
	return connection.Builder()
}
