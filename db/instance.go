package db

import (
	"gorgany/app/core"
	"gorgany/internal"
)

func Connection(name ...string) core.IConnection {
	if len(name) == 0 {
		return internal.GetFrameworkRegistrar().GetDbConnection("default")
	}
	return internal.GetFrameworkRegistrar().GetDbConnection(name[0])
}

func Builder(name ...string) core.IQueryBuilder {
	return Connection(name...).Builder()
}
