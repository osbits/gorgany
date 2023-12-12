package db

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/internal"
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
