package db

import (
	dbCore "github.com/osbits/gorgany/db/sql/core"
)

type DBContext struct {
	dbConnections map[string]dbCore.IDataSource
}

func (thiz *DBContext) Init() {
	thiz.dbConnections = make(map[string]dbCore.IDataSource)
}

func (thiz *DBContext) RegisterDataSource(name string, dbConnection dbCore.IDataSource) {
	if thiz.dbConnections == nil {
		thiz.dbConnections = make(map[string]dbCore.IDataSource)
	}
	thiz.dbConnections[name] = dbConnection
}

func (thiz *DBContext) GetDataSource(name string) dbCore.IDataSource {
	if thiz.dbConnections == nil {
		return nil
	}
	if dbConnection, ok := thiz.dbConnections[name]; ok {
		return dbConnection
	}
	return nil
}
