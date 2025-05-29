package v2

import (
	"git.qix.sx/gorgany/gorgany.git/db/gorm/postgres/v2/core"
	"gorm.io/gorm"
)

// dataSourceImpl implements the IDataSource interface
type dataSourceImpl struct {
	db *gorm.DB
}

// NewDataSource creates a new data source instance
func NewDataSource(db *gorm.DB) core.IDataSource {
	return &dataSourceImpl{
		db: db,
	}
}

// Close closes the database connection
func (ds *dataSourceImpl) Close() error {
	sqlDB, err := ds.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
