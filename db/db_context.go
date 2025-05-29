package db

import "git.qix.sx/gorgany/gorgany.git/app/core"

type DBContext struct {
	dbConnections map[string]core.IDataSource
}

func (thiz *DBContext) Init() {
	thiz.dbConnections = make(map[string]core.IDataSource)
}

func (thiz *DBContext) RegisterDataSource(name string, dbConnection core.IDataSource) {
	if thiz.dbConnections == nil {
		thiz.dbConnections = make(map[string]core.IDataSource)
	}
	thiz.dbConnections[name] = dbConnection
}

func (thiz *DBContext) GetDataSource(name string) core.IDataSource {
	if thiz.dbConnections == nil {
		return nil
	}
	if dbConnection, ok := thiz.dbConnections[name]; ok {
		return dbConnection
	}
	return nil
}
