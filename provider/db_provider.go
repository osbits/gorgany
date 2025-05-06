package provider

import (
	"fmt"
	"github.com/spf13/viper"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	_postgres "git.qix.sx/gorgany/gorgany.git/db/gorm/postgres"
)

type DbProvider struct {
	connCtors []func() (string, core.GrgDBConnection)
}

func NewDbProvider() *DbProvider {
	p := &DbProvider{}

	p.connCtors = []func() (string, core.GrgDBConnection){
		func() (string, core.GrgDBConnection) {
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

func (p *DbProvider) AddConnection(name string, ctor func() core.GrgDBConnection) {
	p.connCtors = append(p.connCtors, func() (string, core.GrgDBConnection) {
		return name, ctor()
	})
}

func (p *DbProvider) Register(c core.IContainer) {
	c.SingletonLazy(func() *db.DBContext {
		return &db.DBContext{}
	})

	for _, ctor := range p.connCtors {
		c.TransientLazy(func(ctor func() (string, core.GrgDBConnection)) func() (string, core.GrgDBConnection) {
			return ctor
		}(ctor))
	}
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
				ctx.RegisterDBConnection(name, conn)
			}
		}
	})

	db.SetDBContext(ctx)
	db.SetBuilderFactory(func(name string) core.IQueryBuilder {
		conn := ctx.GetDBConnection(name)
		builder := conn.Builder()
		if err := c.Make(&builder); err != nil {
			panic(fmt.Errorf("db builder injection error: %w", err))
		}
		return builder
	})

	return nil
}
