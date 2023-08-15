package db

import (
	"gorgany/internal"
	"gorgany/proxy"
)

func Builder(kind proxy.DbType) proxy.IQueryBuilder {
	connection := internal.GetFrameworkRegistrar().GetDbConnection(kind)
	return connection.Builder()
}
