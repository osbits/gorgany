package db

import "git.qix.sx/gorgany/gorgany.git/app/core"

type DBContext struct {
	dbConnections map[string]core.GrgDBConnection
}

func (thiz *DBContext) Init() {
	thiz.dbConnections = make(map[string]core.GrgDBConnection)
}

func (thiz *DBContext) RegisterDBConnection(name string, dbConnection core.GrgDBConnection) {
	if thiz.dbConnections == nil {
		thiz.dbConnections = make(map[string]core.GrgDBConnection)
	}
	thiz.dbConnections[name] = dbConnection
}

func (thiz *DBContext) GetDBConnection(name string) core.GrgDBConnection {
	if thiz.dbConnections == nil {
		return nil
	}
	if dbConnection, ok := thiz.dbConnections[name]; ok {
		return dbConnection
	}
	return nil
}
