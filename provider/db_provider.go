package provider

import (
	"fmt"
	"sort"

	dbCmd "github.com/osbits/gorgany/v2/command/db"
	"github.com/osbits/gorgany/v2/db/migration"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	"github.com/osbits/gorgany/v2/log"

	// No driver package is imported here on purpose (F7). This provider used to
	// blank-import db/sql/driver/builtin, which registers both engines — so every app
	// using the standard bootstrap linked gorm.io/driver/mysql, go-sql-driver/mysql and
	// filippo.io/edwards25519 whether or not it would ever speak MySQL, with no way to
	// opt out.
	//
	// The app now chooses, with one blank import next to its own provider:
	//
	//	_ "github.com/osbits/gorgany/v2/db/sql/driver/postgres"
	//	_ "github.com/osbits/gorgany/v2/db/sql/driver/mysql"
	//	_ "github.com/osbits/gorgany/v2/db/sql/driver/builtin"  // both
	//
	// This is compile-clean and shows up only at boot, so driver.New has a dedicated
	// message for an empty registry that names those import lines.

	"github.com/spf13/viper"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db"
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

	// Refused here rather than on the first request, and as a panic like every other
	// configuration error in this method, because nothing later can repair it: the sessions
	// table is never created on such a default (db:migrate leaves its migrations out there),
	// and the session repository refuses to write there, so the app would boot and then fail
	// every login.
	if refusal := databaseSessionStorageRefusal(defaultConn); refusal != nil {
		panic(refusal)
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

// databaseSessionStorageRefusal reports why auth.session.storage: database cannot keep its
// sessions on defaultConn, or nil when it can or the sessions are kept elsewhere.
//
// Database session storage writes a row on every login, touch and logout, into a table the
// sessions migrations create — so it needs a default whose schema gorgany owns and which takes
// writes. The key is read here as well as in AppProvider, which picks the storage, because this
// is where the default connection has been built and its policy can be asked; AppProvider runs
// before any connection exists.
//
// external_schema is reported ahead of read_only when both are set, as every policy refusal
// reports it (dbCore.DataSourcePolicy.Refusal): it is the one that also rules out creating
// the table, and the advice is the same either way.
func databaseSessionStorageRefusal(defaultConn dbCore.IDataSource) error {
	if viper.GetString("auth.session.storage") != "database" {
		return nil
	}

	reason := dbCore.PolicyOf(defaultConn).Refusal()
	if reason == nil {
		return nil
	}

	return fmt.Errorf(
		"auth.session.storage: database keeps sessions in datasource %q, which refuses them: %w. "+
			"Use auth.session.storage: memory, which is only correct for a single instance, or point %q "+
			"at a database gorgany owns and configure this one under another name; see gorgany's "+
			"docs/DEPLOYMENT.md, \"More than one instance\"",
		core.DefaultKeyInRegistrar, reason, core.DefaultKeyInRegistrar)
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
	// Publish the resolved context to the db package's process-global. That global is
	// how code holding no container reaches a datasource — db.GetDBContext() and
	// db.Connection(name) — which is the situation reflective helpers and validators
	// are in.
	//
	// Nothing called this before, so the global stayed nil in every application built
	// on the standard bootstrap and db.Connection() dereferenced a nil interface.
	c.Invoke(func(dbContext core.IDBContext) {
		db.SetDBContext(dbContext)
	})

	// Register sessions migrations. They run only on a `default` configured without
	// external_schema or read_only, and db:migrate decides that when it runs, with a line saying
	// why it left them out (see dbCmd.OnOwnedDefault), rather than here: an app may register its
	// `default` on the DBContext from a provider whose Boot runs after this one. Order matters
	// on a fresh install: the version column is added to a table create_sessions_table has to
	// have made first.
	c.Invoke(func(dataContext core.IDataContext) {
		dataContext.AddMigration(dbCmd.OnOwnedDefault(migration.NewSessionsMigration()))
		dataContext.AddMigration(dbCmd.OnOwnedDefault(migration.NewSessionsVersionMigration()))
	})
}
