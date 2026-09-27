package v2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/gorm/guard"
	"github.com/osbits/gorgany/v2/log"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// gormSQLServerDataSource implements dbCore.IDataSource over gorm.io/driver/sqlserver and
// go-mssqldb.
type gormSQLServerDataSource struct {
	db    *gorm.DB
	sqlDB *sql.DB

	// readOnly and externalSchema carry databases.<name>.read_only and external_schema, which
	// Policy reports and Dialect threads into every builder. The connection's guards are
	// installed from the same flags, so the layers cannot disagree.
	readOnly       bool
	externalSchema bool

	// tokens is the token source of an Entra ID sign-in, nil for a SQL login.
	tokens *datasourceTokens

	closeOnce sync.Once
	closeErr  error
}

var _ dbCore.PolicyReporter = (*gormSQLServerDataSource)(nil)

// warn reports what a datasource's config asks for and does not get, or should know. It is a
// variable so tests can collect what it says.
var warn = func(msg string) { log.Log().Warn(msg) }

// NewDataSource creates a SQL Server datasource from a raw `databases.<name>` map.
func NewDataSource(config map[string]any) (dbCore.IDataSource, error) {
	cfg, err := dsconfig.Parse(config)
	if err != nil {
		return nil, err
	}
	return NewDataSourceWithConfig(cfg)
}

// NewDataSourceWithConfig creates a SQL Server datasource from a typed config.
//
// Everything that can be decided without the network is decided first, and a config that
// cannot work is refused with an error rather than a panic: the DSN and its options, the
// named-instance and Azure rules, and how the connection signs in (see planConnection). Then
// go-mssqldb's connector and a *sql.DB over it are made, with the pool defaults of
// effectivePool, and gorm is opened on that without its ping. Before anything can send a
// statement, the connection gets the bind-parameter cap and, as the flags ask, the read-only
// guard and the external-schema guard, installed on the handle gorm.Open returned so that
// every session, transaction and the handle GetDriver returns share them.
//
// Without lazy_connect it then signs in and connects, retrying the errors Azure SQL returns
// while it resumes or fails over, and an unreachable server or a failed login fails the
// constructor. With lazy_connect it opens nothing, and the first query signs in and then makes
// the first connection. Every connection takes its token before it dials (see
// tokenFirstConnector).
//
// No error contains the DSN, a password, a secret or a token: they name the server as
// host:port/db (see describe) and the sign-in method.
func NewDataSourceWithConfig(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	plan, err := planConnection(cfg)
	if err != nil {
		return nil, err
	}

	ds := &gormSQLServerDataSource{readOnly: cfg.ReadOnly, externalSchema: cfg.ExternalSchema}
	if plan.auth.authenticator != nil {
		if ds.tokens, err = newTokens(plan); err != nil {
			return nil, err
		}
	}

	connector, err := newConnector(plan.dsn, ds.tokens)
	if err != nil {
		_ = ds.Close()
		return nil, fmt.Errorf("sqlserver: invalid connection settings for %s: %w", plan.target, err)
	}
	ds.sqlDB = sql.OpenDB(connector)
	applyPool(ds.sqlDB, effectivePool(cfg.Pool))

	if ds.db, err = openGorm(ds.sqlDB, cfg.ReadOnly, cfg.ExternalSchema); err != nil {
		_ = ds.Close()
		return nil, err
	}

	for _, msg := range plan.warnings {
		warn(msg)
	}

	if !cfg.LazyConnect {
		if err := warmUp(ds.tokens, plan.request.LoginTimeout); err != nil {
			_ = ds.Close()
			return nil, fmt.Errorf("sqlserver: cannot sign in to %s (auth: %s): %w", plan.target, plan.auth.method, err)
		}
		if err := pingWithRetry(context.Background(), ds.sqlDB, defaultRetry); err != nil {
			_ = ds.Close()
			return nil, connectError(plan.target, plan.auth.method, wrapServerError(err))
		}
	}

	if cfg.Log {
		warn(fmt.Sprintf("sqlserver: log is on for %s: every statement is logged with its bound values, "+
			"which may be personal data; turn it off outside development", plan.target))
		ds.db = ds.db.Debug()
	}
	return ds, nil
}

// connectionPlan is everything a datasource needs to connect that its config decides.
type connectionPlan struct {
	dsn      string
	target   string
	auth     resolvedAuth
	request  AuthRequest
	warnings []string
}

// planConnection decides from cfg alone, with no I/O, how the datasource connects: the DSN,
// the server as errors name it, the sign-in method and, for a token method, what its
// Authenticator is asked for. A config that cannot work is refused here, before anything is
// opened.
func planConnection(cfg dsconfig.DataSource) (connectionPlan, error) {
	if err := refuseSearchPath(cfg); err != nil {
		return connectionPlan{}, err
	}
	auth, err := resolveAuth(cfg)
	if err != nil {
		return connectionPlan{}, err
	}
	dsn, _, warnings, err := buildDSN(cfg, auth.sqlLogin())
	if err != nil {
		return connectionPlan{}, err
	}

	plan := connectionPlan{
		dsn:      dsn,
		target:   describe(cfg),
		auth:     auth,
		warnings: warnings,
	}
	if auth.sqlLogin() {
		return plan, nil
	}

	scope, err := ResolveScope(cfg.Host, cfg.Auth.Scope)
	if err != nil {
		return connectionPlan{}, err
	}
	cloud, err := ResolveCloud(cfg.Host, scope)
	if err != nil {
		return connectionPlan{}, err
	}
	plan.request = AuthRequest{
		Method:       auth.method,
		Auth:         cfg.Auth,
		Username:     cfg.Username,
		Host:         cfg.Host,
		Database:     cfg.Database,
		Target:       plan.target,
		Scope:        scope,
		Cloud:        cloud,
		LoginTimeout: loginTimeoutFor(auth.method, cfg.Auth.LoginTimeout),
	}
	return plan, nil
}

// newTokens asks plan's Authenticator for the datasource's token source, giving it a lifetime
// Close ends.
func newTokens(plan connectionPlan) (*datasourceTokens, error) {
	root, cancel := context.WithCancel(context.Background())
	request := plan.request
	request.Context = root

	source, err := plan.auth.authenticator(request)
	if err == nil && source == nil {
		err = errors.New("the authenticator returned no token source")
	}
	if err != nil {
		cancel()
		return nil, fmt.Errorf("sqlserver: auth.method %s for %s: %w", plan.auth.method, plan.target, err)
	}
	return &datasourceTokens{source: source, root: root, cancel: cancel}, nil
}

// openGorm opens gorm on conn and installs the callbacks every SQL Server connection has
// before anything can send a statement on it: the bind-parameter cap, then the guards the
// flags ask for.
//
// gorm's automatic ping is off, since connecting is the constructor's decision (see
// NewDataSourceWithConfig), and its logger is silent, as on the other engines, until the log
// key turns on Debug.
func openGorm(conn gorm.ConnPool, readOnly, externalSchema bool) (*gorm.DB, error) {
	db, err := gorm.Open(sqlserver.New(sqlserver.Config{Conn: conn}), &gorm.Config{
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("sqlserver: %w", err)
	}
	if err := installParamLimit(db); err != nil {
		return nil, err
	}
	if readOnly {
		if err := guard.InstallReadOnly(db, dbCore.LexiconTSQL); err != nil {
			return nil, fmt.Errorf("sqlserver: %w", err)
		}
	}
	if externalSchema {
		if err := guard.InstallExternalSchema(db, dbCore.LexiconTSQL); err != nil {
			return nil, fmt.Errorf("sqlserver: %w", err)
		}
	}
	return db, nil
}

// Dialect returns the SQL dialect this connection speaks, carrying read_only, so every
// builder a session hands out refuses a write where it is built.
func (ds *gormSQLServerDataSource) Dialect() dbCore.SQLDialect {
	return &SQLServerDialect{ReadOnly: ds.readOnly}
}

// Policy reports what the datasource's configuration allows (see dbCore.DataSourcePolicy).
func (ds *gormSQLServerDataSource) Policy() dbCore.DataSourcePolicy {
	return dbCore.DataSourcePolicy{ExternalSchema: ds.externalSchema, ReadOnly: ds.readOnly}
}

// GetDriver returns the underlying *gorm.DB, guarded as the datasource's flags ask.
func (ds *gormSQLServerDataSource) GetDriver() (any, error) {
	return ds.db, nil
}

// Close closes the datasource's connections.
//
// It returns without waiting on anything the network does: it never pings and never asks for
// a token. It first ends the token source's lifetime, so a sign-in in flight — a lazy
// interactive one still waiting on the browser, say — is abandoned rather than awaited, and
// the query that started it fails; then it closes the *sql.DB, which closes the idle
// connections and each busy one as it is returned. That matters at shutdown, where
// DBContext.Close runs after the drains and nothing bounds how long it takes. Calling it again
// does nothing and returns what the first call did.
func (ds *gormSQLServerDataSource) Close() error {
	ds.closeOnce.Do(func() {
		if ds.tokens != nil {
			ds.tokens.cancel()
		}
		if ds.sqlDB != nil {
			ds.closeErr = ds.sqlDB.Close()
		}
	})
	return ds.closeErr
}
