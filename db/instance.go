package db

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
)

var (
	dbCtx          core.IDBContext
	builderFactory func(name string) core.IQueryBuilder
)

func SetDBContext(ctx core.IDBContext) {
	dbCtx = ctx
}

func SetBuilderFactory(f func(name string) core.IQueryBuilder) {
	builderFactory = f
}

func GetDBContext() core.IDBContext {
	return dbCtx
}

func Connection(name ...string) core.IDataSource {
	key := core.DefaultKeyInRegistrar
	if len(name) > 0 && name[0] != "" {
		key = name[0]
	}
	return GetDBContext().GetDataSource(key)
}

func Builder(name ...string) core.IQueryBuilder {
	if builderFactory == nil {
		return nil
	}
	key := core.DefaultKeyInRegistrar
	if len(name) > 0 && name[0] != "" {
		key = name[0]
	}
	return builderFactory(key)
}
