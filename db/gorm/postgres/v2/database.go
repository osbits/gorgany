package v2

import (
	"git.qix.sx/gorgany/gorgany.git/db/gorm/postgres/v2/core"
	"gorm.io/gorm"
)

// Database is the main facade for database operations
type Database struct {
	dataSource core.IDataSource
	session    core.ISession
}

// NewDatabase creates a new database instance
func NewDatabase(db *gorm.DB) (*Database, error) {
	dataSource := NewDataSource(db)
	session, err := dataSource.NewSession()
	if err != nil {
		return nil, err
	}

	return &Database{
		dataSource: dataSource,
		session:    session,
	}, nil
}

// NewSession creates a new database session
func (d *Database) NewSession() (core.ISession, error) {
	return d.dataSource.NewSession()
}

// Close closes the database connection
func (d *Database) Close() error {
	if d.session != nil {
		if err := d.session.Close(); err != nil {
			return err
		}
	}
	return d.dataSource.Close()
}
