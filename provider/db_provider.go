package provider

import (
	"fmt"
	core2 "git.qix.sx/gorgany/gorgany.git/db/gorm/postgres/v2/core"

	"github.com/spf13/viper"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	_postgres "git.qix.sx/gorgany/gorgany.git/db/gorm/postgres"
)

type DbProvider struct {
	connCtors []func() (string, core.IDataSource)
}

func NewDbProvider() *DbProvider {
	p := &DbProvider{}

	p.connCtors = []func() (string, core.IDataSource){
		func() (string, core.IDataSource) {
			databases := viper.GetStringMap("databases")
			for name, cfg := range databases {
				conf, ok := cfg.(map[string]any)
				if !ok {
					panic(fmt.Errorf("incorrect config for database '%s'", name))
				}
				driver := core.DbType(conf["driver"].(string))
				switch driver {
				case core.GormPostgreSQL:
					return name, _postgres.NewGormPostgresConnection(conf)
				case core.MongoDb:
					// TODO: implement me
				}
			}
			return "", nil
		},
	}
	return p
}

func (p *DbProvider) AddConnection(name string, ctor func() core.IDataSource) {
	p.connCtors = append(p.connCtors, func() (string, core.IDataSource) {
		return name, ctor()
	})
}

func (p *DbProvider) Register(c core.IContainer) {
	// Register DBContext as singleton
	c.SingletonLazy(func() *db.DBContext {
		return &db.DBContext{}
	})

	// Register connection constructors
	for _, ctor := range p.connCtors {
		c.SingletonLazy(func(ctor func() (string, core.IDataSource)) func() (string, core.IDataSource) {
			return ctor
		}(ctor))
	}

	// Register Session as transient
	c.TransientLazy(func(ds core2.IDataSource) (core2.ISession, error) {
		return ds.NewSession()
	})

	// Register QueryExecutor as transient
	c.TransientLazy(func(session core2.ISession) core2.IQueryExecutor {
		return session.Executor()
	})

	// Register QueryBuilder as transient
	c.TransientLazy(func(session core2.ISession) core2.IQueryBuilder {
		return session.Query()
	})
}

func (p *DbProvider) Boot(c core.IContainer) error {
	var ctx *db.DBContext
	if err := c.Make(&ctx); err != nil {
		return fmt.Errorf("db Boot: cannot Make DBContext: %w", err)
	}

	c.Invoke(func(db *db.DBContext) {
		for _, ctor := range p.connCtors {
			name, conn := ctor()
			if conn != nil {
				ctx.RegisterDataSource(name, conn)
			}
		}
	})

	db.SetDBContext(ctx)
	db.SetBuilderFactory(func(name string) core.IQueryBuilder {
		conn := ctx.GetDataSource(name)
		return conn.Builder()
	})

	return nil
}
