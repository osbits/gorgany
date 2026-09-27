package v2

import (
	"fmt"
	"strconv"
	"strings"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/gorm/guard"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// gormPostgresDataSource implements the IDataSource interface
type gormPostgresDataSource struct {
	db *gorm.DB

	// externalSchema carries databases.<name>.external_schema, which Policy reports. The
	// connection's DDL guard is installed from the same flag, so the two cannot disagree.
	externalSchema bool

	// readOnly carries databases.<name>.read_only, which Policy reports and Dialect hands
	// every builder. The connection's write guard is installed from the same flag, and BuildDSN
	// asks the server for read-only transactions from it, so none of them can disagree.
	readOnly bool
}

// NewDataSource creates a Postgres datasource from a raw `databases.<name>` map.
//
// It validates the map and returns a descriptive error naming the offending key
// and expected type. Before v2 it did unchecked type assertions —
// config["prefer_simple_protocol"].(bool), props["maxOpenConnections"].(int),
// config["log"].(bool) — so a missing or mistyped key panicked. A hand-built
// config map, which is exactly what a test helper writes, took the process down.
func NewDataSource(config map[string]any) (core.IDataSource, error) {
	cfg, err := dsconfig.Parse(config)
	if err != nil {
		return nil, err
	}
	return NewDataSourceWithConfig(cfg)
}

// NewDataSourceWithConfig creates a Postgres datasource from a typed config.
//
// Settings Postgres cannot honour are refused before anything is opened; see
// refuseUnsupported. With lazy_connect the constructor opens nothing at all: gorm's initial
// ping is skipped, so the first query makes the first connection. Without it, an unreachable
// server fails the constructor, as it always has. With external_schema, the connection
// refuses DDL (see guard.InstallExternalSchema) from before the first statement anyone can
// send on it.
//
// With read_only, writes are refused in three places, each of which sees what the one before
// it cannot. The dialect refuses to render a write, so a builder from this datasource's
// sessions and transactions fails where the write is built (PostgresDialect.ReadOnly). The
// guard on the connection refuses one that reaches gorm any other way — an app's Exec or Raw,
// gorm's Create, Update and Delete, the Migrator — from before the first statement anyone can
// send on it (guard.InstallReadOnly). And the server is asked to make every transaction
// read-only, which also covers what the guard never sees, such as a statement sent on the
// *sql.DB that DB() returns — unless the config leaves the request out, or something the guard
// does not see switches it off for a connection, as asksForReadOnlyTransactions explains. The
// dialect's and the guard's refusals wrap core.ErrReadOnly; the server's is the driver's
// error, SQLSTATE 25006. Each is a safety net; a role that can only read is the guarantee.
func NewDataSourceWithConfig(cfg dsconfig.DataSource) (core.IDataSource, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := refuseUnsupported(cfg); err != nil {
		return nil, err
	}

	dsn, err := BuildDSN(cfg)
	if err != nil {
		return nil, err
	}

	gormConfig := postgres.Config{DSN: dsn, PreferSimpleProtocol: cfg.PreferSimpleProtocol}
	db, err := gorm.Open(postgres.New(gormConfig), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		DisableAutomaticPing:                     cfg.LazyConnect,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: cannot open connection to %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, err)
	}

	ds, err := fromOpened(db, cfg)
	if err != nil {
		return nil, closeAfter(db, err)
	}
	return ds, nil
}

// fromOpened finishes a datasource on db, the handle gorm.Open returned for cfg: it installs
// the guards cfg's policy asks for, applies the pool settings and statement logging, and
// carries the flags the datasource reports.
//
// It is split from NewDataSourceWithConfig, which opens db from a DSN, so that a test can
// finish a datasource on a handle over a driver of its own and see exactly what reaches it.
// The caller closes db when it fails.
func fromOpened(db *gorm.DB, cfg dsconfig.DataSource) (*gormPostgresDataSource, error) {
	// Installed on the handle gorm.Open returned, before Debug() below derives one from it:
	// every handle derived from it shares its callbacks and its guarded pool, so the guards
	// cover the logging handle, every session and every transaction.
	if cfg.ExternalSchema {
		if err := guard.InstallExternalSchema(db, core.LexiconPostgres); err != nil {
			return nil, fmt.Errorf("postgres: %w", err)
		}
	}
	if cfg.ReadOnly {
		if err := guard.InstallReadOnly(db, core.LexiconPostgres); err != nil {
			return nil, fmt.Errorf("postgres: %w", err)
		}
	}

	rawDb, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("postgres: cannot reach underlying *sql.DB: %w", err)
	}

	if cfg.Pool.MaxOpenConnections > 0 {
		rawDb.SetMaxOpenConns(cfg.Pool.MaxOpenConnections)
	}
	if cfg.Pool.MaxIdleConnections > 0 {
		rawDb.SetMaxIdleConns(cfg.Pool.MaxIdleConnections)
	}
	if cfg.Pool.ConnectionMaxLifetime > 0 {
		rawDb.SetConnMaxLifetime(cfg.Pool.ConnectionMaxLifetime)
	}
	if cfg.Pool.ConnectionMaxIdleLifetime > 0 {
		rawDb.SetConnMaxIdleTime(cfg.Pool.ConnectionMaxIdleLifetime)
	}

	if cfg.Log {
		db = db.Debug()
	}

	return &gormPostgresDataSource{db: db, externalSchema: cfg.ExternalSchema, readOnly: cfg.ReadOnly}, nil
}

// refuseUnsupported refuses the datasource settings Postgres does not honour, each with an
// UnsupportedError that names the key.
//
// Every one of them is refused rather than ignored, because ignoring it would change what the
// config means without saying so:
//
//   - instance names a SQL Server named instance. A config that sets it was written for
//     another engine, and connecting to whatever answers on the host's default port would
//     hide that.
//   - auth selects a sign-in method, and Postgres here signs in one way only: the top-level
//     username and password, which is what method "sql" and an absent block mean. Any other
//     method, or any other setting under auth, would be dropped on the floor, and the
//     sign-in that followed would not be the one configured.
//
// external_schema, read_only and lazy_connect are honoured, so they are not here.
func refuseUnsupported(cfg dsconfig.DataSource) error {
	if cfg.Instance != "" {
		return core.Unsupported(DialectName, "instance", "named instances are a SQL Server concept")
	}
	return refuseNonSQLAuth(cfg.Auth)
}

// refuseNonSQLAuth refuses an auth block that asks for anything but the top-level username
// and password. The error names the method, which is a word from the config's vocabulary, and
// never a value from any other auth key, which may be a secret.
func refuseNonSQLAuth(auth dsconfig.Auth) error {
	method := strings.ToLower(strings.TrimSpace(auth.Method))
	if method != "" && method != "sql" {
		return core.Unsupported(DialectName, fmt.Sprintf("auth method %q", auth.Method),
			"Postgres signs in with the top-level username and password only; remove the auth block or set method: sql")
	}
	auth.Method = ""
	if !auth.IsZero() {
		return core.Unsupported(DialectName, "auth settings other than method",
			"method sql signs in with the top-level username and password and takes no other auth key; remove them")
	}
	return nil
}

// closeAfter closes the pool gorm.Open opened for db, since the caller is about to fail
// without handing db to anyone who could, and returns err.
func closeAfter(db *gorm.DB, err error) error {
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		_ = sqlDB.Close()
	}
	return err
}

// BuildDSN renders cfg as a libpq keyword/value connection string.
//
// It gained two things in v2:
//
//   - search_path, so a connection can start inside a named schema. That is what
//     makes schema-per-test isolation possible; without it consumers are forced
//     into CREATE DATABASE per test run plus truncate-between-tests. The pgx
//     driver underneath gorm.io/driver/postgres forwards any key it does not
//     recognise as a server runtime parameter, which is how search_path arrives.
//   - Options, for arbitrary driver parameters (connect_timeout,
//     application_name, ...). Keys are emitted sorted so the DSN is reproducible.
//
// Every value is escaped per libpq rules: a value containing whitespace, a quote
// or a backslash is single-quoted with those characters backslash-escaped. The
// old fmt.Sprintf built the DSN by raw interpolation, so a password containing a
// space silently truncated the DSN and a password containing a quote corrupted it.
//
// Keys with empty values are omitted rather than emitted as `key=`, so the driver
// applies its own default instead of parsing a malformed pair. For any config
// that connected before, the output is unchanged.
//
// A read_only config also gets default_transaction_read_only=on, after search_path and
// before the options, except where asksForReadOnlyTransactions explains it must not be sent.
func BuildDSN(cfg dsconfig.DataSource) (string, error) {
	pairs := make([]string, 0, 9+len(cfg.Options))

	add := func(key, value string) {
		if value == "" {
			return
		}
		pairs = append(pairs, key+"="+escapeDSNValue(value))
	}

	add("host", cfg.Host)
	if cfg.Port > 0 {
		add("port", strconv.Itoa(cfg.Port))
	}
	add("user", cfg.Username)
	add("password", cfg.Password)
	add("dbname", cfg.Database)
	add("sslmode", cfg.SSL)
	add("search_path", cfg.SearchPath)
	if asksForReadOnlyTransactions(cfg) {
		add(defaultTransactionReadOnly, "on")
	}

	for _, key := range cfg.SortedOptionKeys() {
		if err := validateDSNKey(key); err != nil {
			return "", err
		}
		add(key, cfg.Options[key])
	}

	return strings.Join(pairs, " "), nil
}

// defaultTransactionReadOnly is the Postgres setting that makes every transaction a session
// begins read-only unless it asks otherwise, the implicit one around a lone statement
// included.
const defaultTransactionReadOnly = "default_transaction_read_only"

// asksForReadOnlyTransactions reports whether BuildDSN asks the server to make every
// transaction read-only, as a runtime parameter pgx sends when it connects.
//
// It does for a read_only datasource, so that the server itself refuses a write that got past
// the dialect and the guard: one sent on the *sql.DB that DB() returns, which the guard never
// sees, or one a function the statement calls runs. The write then fails with SQLSTATE 25006,
// "cannot execute INSERT in a read-only transaction", which arrives as the driver's error and
// does not wrap core.ErrReadOnly. It is still a safety net. A session can switch the setting
// off again, with SET default_transaction_read_only = off, SET TRANSACTION READ WRITE or
// set_config, the function form of the first. The guard refuses all three, but not what it
// does not see: a set_config sent on DB(), or run by a function a SELECT calls, switches the
// setting off on that pooled connection for as long as the connection lives, for every
// statement anyone sends on it afterwards, DB()'s included. Only a role that can only read is
// a guarantee.
//
// It is left out where it must not be sent:
//
//   - options name it already: as a key of their own, in any case, or with -c or -- in
//     libpq's options key, however Postgres would spell it there (see
//     optionsSetDefaultTransactionReadOnly). The explicit setting wins, off included: the
//     operator has said what the server should do. A key of its own that is empty sends
//     nothing at all, which is how a datasource leaves the parameter out, as it must behind a
//     proxy that refuses it (see below).
//   - options set target_session_attrs to read-write or read-only. pgx decides whether a host
//     qualifies for those by asking the new session's transaction_read_only, which this
//     setting turns on, so read-write would refuse every host and read-only would accept
//     every host, the primary included, instead of looking for a standby. standby and
//     prefer-standby ask pg_is_in_recovery() instead, and are what finds a replica.
//
// What BuildDSN is not given, it cannot defer to. pgx also reads options from PGOPTIONS,
// target_session_attrs from PGTARGETSESSIONATTRS, and any setting from a service file (service,
// or PGSERVICE), none of which is seen here. Postgres applies a runtime parameter after the
// switches in options, and pgx takes the DSN's settings over a service file's, so the
// default_transaction_read_only=on added here overrides an off given in PGOPTIONS or a service
// file, and a target_session_attrs given in PGTARGETSESSIONATTRS or a service file meets it as
// one under options would, above. A read_only datasource that means to set either sets it
// under options.
//
// PgBouncer before 1.26 refuses every startup parameter it does not track, whatever its pool
// mode, so a read_only datasource behind one fails to connect, with "unsupported startup
// parameter: default_transaction_read_only", until one of three things changes. PgBouncer can
// track it, with track_extra_parameters from 1.20 on and Postgres 14 or later, and then
// restores it on whichever server connection a client is given; 1.26 tracks it by default. It
// can ignore it, with ignore_startup_parameters, and then the server never sees it. Or the
// datasource sets the option empty and sends nothing. The dialect and the guard apply either
// way. prefer_simple_protocol changes nothing here: it chooses the protocol, not the proxy,
// and says nothing about what a proxy tracks.
func asksForReadOnlyTransactions(cfg dsconfig.DataSource) bool {
	if !cfg.ReadOnly {
		return false
	}
	for key, value := range cfg.Options {
		switch {
		case strings.EqualFold(key, defaultTransactionReadOnly):
			// Set, or set empty so that nothing is sent.
			return false
		case value == "":
			// BuildDSN leaves an empty option out, so it sets nothing.
		case strings.EqualFold(key, "options") && optionsSetDefaultTransactionReadOnly(value):
			return false
		case strings.EqualFold(key, "target_session_attrs"):
			if attrs := strings.ToLower(strings.TrimSpace(value)); attrs == "read-write" || attrs == "read-only" {
				return false
			}
		}
	}
	return true
}

// optionsSetDefaultTransactionReadOnly reports whether options, the value of libpq's options
// key, names default_transaction_read_only, as a -c or -- switch would set it.
//
// It matches the name as Postgres reads the switches, not as they are written. Postgres takes
// a backslash in options to escape the character after it, and a dash in a switch's setting
// name for an underscore, so -c default-transaction-read-only=off and
// --default-transaction-read-only=off set it as -c default_transaction_read_only=off does; and
// a setting's name is not case-sensitive. A spelling that went unseen here would have the
// parameter BuildDSN adds override the operator's own. Matching the name anywhere in the value,
// rather than parsing each switch, errs the other way: a value that merely mentions it, in
// another setting's value, leaves the parameter out.
func optionsSetDefaultTransactionReadOnly(options string) bool {
	normalised := strings.NewReplacer(`\`, "", "-", "_").Replace(strings.ToLower(options))
	return strings.Contains(normalised, defaultTransactionReadOnly)
}

// validateDSNKey rejects option keys that cannot appear in a keyword/value DSN.
// An unchecked key could otherwise smuggle a second parameter into the string.
func validateDSNKey(key string) error {
	if key == "" {
		return fmt.Errorf("postgres: DSN option key must not be empty")
	}
	for _, r := range key {
		isAllowed := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_'
		if !isAllowed {
			return fmt.Errorf("postgres: invalid DSN option key %q: only letters, digits and '_' are allowed", key)
		}
	}
	return nil
}

// escapeDSNValue quotes a libpq keyword/value connection-string value.
func escapeDSNValue(value string) string {
	if !strings.ContainsAny(value, " \t\r\n'\\") {
		return value
	}

	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('\'')
	for _, r := range value {
		if r == '\'' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('\'')
	return b.String()
}

// Dialect returns the SQL dialect this connection speaks. Sessions created from
// this datasource hand it to every builder they produce, their transactions' included, so
// read_only reaches every builder an app gets from here.
func (ds *gormPostgresDataSource) Dialect() core.SQLDialect {
	return &PostgresDialect{ReadOnly: ds.readOnly}
}

// Policy reports what the datasource's configuration allows (see core.DataSourcePolicy).
func (ds *gormPostgresDataSource) Policy() core.DataSourcePolicy {
	return core.DataSourcePolicy{ExternalSchema: ds.externalSchema, ReadOnly: ds.readOnly}
}

func (ds *gormPostgresDataSource) GetDriver() (any, error) {
	return ds.db, nil
}

// Close closes the database connection
func (ds *gormPostgresDataSource) Close() error {
	sqlDB, err := ds.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
