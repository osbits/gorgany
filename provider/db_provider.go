package provider

import (
	"fmt"
	"sort"

	dbCmd "github.com/osbits/gorgany/command/db"
	"github.com/osbits/gorgany/db/migration"
	dsconfig "github.com/osbits/gorgany/db/sql/config"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/osbits/gorgany/db/sql/driver"
	"github.com/osbits/gorgany/log"

	// Registers the drivers that ship with the framework (postgres_gorm,
	// mysql_gorm). An app adding its own engine calls driver.Register from its
	// own provider.
	_ "github.com/osbits/gorgany/db/sql/driver/builtin"

	"github.com/spf13/viper"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/db"
)

type DbProvider struct {
	// connCtors holds only the connections added programmatically through
	// AddConnection/AddConnectionE. The connections described by config are built
	// during Register — see configuredConnections for why that timing matters.
	connCtors []func() (string, dbCore.IDataSource, error)
}

func NewDbProvider() *DbProvider {
	return &DbProvider{}
}

// configuredConnections turns every entry under the `databases` config key into a
// constructor, in sorted-name order.
//
// Before v2 this was a single closure that looped over the map and *returned from
// inside the loop*, so configuring two databases registered exactly one of them —
// and because Go randomises map iteration order, which one changed on every boot.
// The loop also understood only `postgres_gorm`; any other driver fell through to
// a silent nil.
//
// Sorting the names makes registration order deterministic too, which matters
// because it decides warning order and, before the `default`-only rule below, used
// to decide which connection won the unnamed transient bindings.
//
// This must be called from Register, never from NewDbProvider. An app builds its
// bootstrapper — and so this provider — as the argument to app.NewServerApp(...),
// which is evaluated before ServerApp.Run() parses config/config. Reading viper in
// the constructor would see an empty config and silently register no connections
// at all.
func configuredConnections() []func() (string, dbCore.IDataSource, error) {
	databases := viper.GetStringMap("databases")

	names := make([]string, 0, len(databases))
	for name := range databases {
		names = append(names, name)
	}
	sort.Strings(names)

	ctors := make([]func() (string, dbCore.IDataSource, error), 0, len(names))
	for _, name := range names {
		name, raw := name, databases[name]
		ctors = append(ctors, func() (string, dbCore.IDataSource, error) {
			conf, ok := raw.(map[string]any)
			if !ok {
				return name, nil, fmt.Errorf("incorrect config for database '%s': expected a map, got %T", name, raw)
			}

			cfg, err := dsconfig.Parse(conf)
			if err != nil {
				return name, nil, fmt.Errorf("database '%s': %w", name, err)
			}

			ds, err := driver.New(cfg)
			if err != nil {
				return name, nil, fmt.Errorf("database '%s': %w", name, err)
			}
			return name, ds, nil
		})
	}
	return ctors
}

// AddConnection registers a connection the config loop cannot express.
func (p *DbProvider) AddConnection(name string, ctor func() dbCore.IDataSource) {
	p.connCtors = append(p.connCtors, func() (string, dbCore.IDataSource, error) {
		ds := ctor()
		if ds == nil {
			return name, nil, fmt.Errorf("connection '%s': constructor returned a nil datasource", name)
		}
		return name, ds, nil
	})
}

// AddConnectionE is AddConnection for a constructor that can fail. Prefer it: a
// datasource constructor that cannot report an error has to panic instead.
func (p *DbProvider) AddConnectionE(name string, ctor func() (dbCore.IDataSource, error)) {
	p.connCtors = append(p.connCtors, func() (string, dbCore.IDataSource, error) {
		ds, err := ctor()
		if err != nil {
			return name, nil, fmt.Errorf("connection '%s': %w", name, err)
		}
		if ds == nil {
			return name, nil, fmt.Errorf("connection '%s': constructor returned a nil datasource", name)
		}
		return name, ds, nil
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

	// Config-described connections are resolved here, not in the constructor,
	// because the config file is parsed after the provider is built. They come
	// first so a programmatic AddConnection can be reasoned about as an addition to
	// what config declares.
	ctors := append(configuredConnections(), p.connCtors...)

	// Build and register every connection. One that fails to build is fatal:
	// booting with a database silently missing turns every query against it into a
	// nil dereference much later.
	connections := make(map[string]dbCore.IDataSource, len(ctors))
	for _, ctor := range ctors {
		name, conn, err := ctor()
		if err != nil {
			panic(err)
		}
		if conn == nil {
			continue
		}
		if _, duplicate := connections[name]; duplicate {
			panic(fmt.Errorf("database '%s' is configured more than once", name))
		}
		connections[name] = conn

		c.Invoke(func(dbContext core.IDBContext) {
			dbContext.RegisterDataSource(name, conn)
		})
	}

	// The unnamed transient bindings resolve against the `default` connection and
	// nothing else.
	//
	// Before v2 these three were re-bound once per connection inside the
	// registration loop, and Container.bind overwrites, so a bare injected
	// dbCore.ISession resolved to whichever connection happened to register last.
	// With no `databases` key at all the built-in constructor returned ("", nil)
	// and the closures were registered anyway, capturing a nil connection and
	// panicking if ever resolved. Both are gone: registration is skipped entirely
	// when there is no default connection, and named resolution via
	// GetDataSource(name).NewSession() is the only way to reach a non-default one.
	defaultConn, hasDefault := connections[core.DefaultKeyInRegistrar]
	if !hasDefault {
		if len(connections) > 0 {
			names := sortedNames(connections)
			log.Log().Warnf(
				"db: no '%s' connection is configured, so a bare injected dbCore.ISession / "+
					"IQueryExecutor / IQueryBuilder cannot be resolved. Reach these connections "+
					"by name instead, e.g. GetDataSource(%q).NewSession(). Configured: %v",
				core.DefaultKeyInRegistrar, names[0], names)
		}
		return
	}

	// Register Session as transient
	c.TransientLazy(func() (dbCore.ISession, error) {
		return defaultConn.NewSession()
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

func sortedNames(connections map[string]dbCore.IDataSource) []string {
	names := make([]string, 0, len(connections))
	for name := range connections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (p *DbProvider) Boot(c core.IContainer) {
	// Register sessions migration
	c.Invoke(func(dataContext core.IDataContext) {
		dataContext.AddMigration(migration.NewSessionsMigration())
	})
}
