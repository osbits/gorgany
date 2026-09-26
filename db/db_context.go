package db

import (
	"errors"
	"fmt"
	"sort"

	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
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

// Close closes every registered datasource, and reports each failure. ServerApp calls it
// last on a graceful shutdown, once no request, job or async subscriber is left to use one.
func (thiz *DBContext) Close() error {
	names := make([]string, 0, len(thiz.dbConnections))
	for name := range thiz.dbConnections {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		if err := thiz.dbConnections[name].Close(); err != nil {
			errs = append(errs, fmt.Errorf("datasource '%s': %w", name, err))
		}
	}
	return errors.Join(errs...)
}
