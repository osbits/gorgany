package provider

import (
	"fmt"

	dbCmd "github.com/osbits/gorgany/command/db"
	"github.com/osbits/gorgany/db/migration"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/osbits/gorgany/db/sql/gorm/postgres/v2"

	"github.com/spf13/viper"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/db"
)

type DbProvider struct {
	connCtors []func() (string, dbCore.IDataSource)
}

func NewDbProvider() *DbProvider {
	p := &DbProvider{}

	p.connCtors = []func() (string, dbCore.IDataSource){
		func() (string, dbCore.IDataSource) {
			databases := viper.GetStringMap("databases")
			for name, cfg := range databases {
				conf, ok := cfg.(map[string]any)
				if !ok {
					panic(fmt.Errorf("incorrect config for database '%s'", name))
				}
				driver := core.DbType(conf["driver"].(string))
				switch driver {
				case core.GormPostgreSQL:
					return name, v2.NewDataSource(conf)
				case core.MongoDb:
					// TODO: implement me
				}
			}
			return "", nil
		},
	}
	return p
}

func (p *DbProvider) AddConnection(name string, ctor func() dbCore.IDataSource) {
	p.connCtors = append(p.connCtors, func() (string, dbCore.IDataSource) {
		return name, ctor()
	})
}

func (p *DbProvider) Register(c core.IContainer) {
	// Register DBContext as singleton
	c.SingletonLazy(func() core.IDBContext {
		return &db.DBContext{}
	})

	// Register DataContext for migrations
	c.SingletonLazy(func() core.IDataContext {
		return &dbCmd.DataContext{}
	})

	for _, ctor := range p.connCtors {
		name, conn := ctor()

		if conn != nil {
			c.Invoke(func(db core.IDBContext) {
				db.RegisterDataSource(name, conn)
			})
		}

		// Register Session as transient
		c.TransientLazy(func() (dbCore.ISession, error) {
			return conn.NewSession()
		})

		// Register QueryExecutor as transient
		c.TransientLazy(func(session dbCore.ISession) dbCore.IQueryExecutor {
			return session.Executor()
		})

		// Register QueryBuilder as transient
		c.TransientLazy(func(session dbCore.ISession) dbCore.IQueryBuilder {
			return session.Query()
		})
	}
}

func (p *DbProvider) Boot(c core.IContainer) {
	// Register sessions migration
	c.Invoke(func(dataContext core.IDataContext) {
		dataContext.AddMigration(migration.NewSessionsMigration())
	})
}
