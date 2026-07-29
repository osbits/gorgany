package v2

import (
	"fmt"
	"strconv"
	"strings"

	dsconfig "github.com/osbits/gorgany/db/sql/config"
	"github.com/osbits/gorgany/db/sql/core"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// gormPostgresDataSource implements the IDataSource interface
type gormPostgresDataSource struct {
	db *gorm.DB
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
func NewDataSourceWithConfig(cfg dsconfig.DataSource) (core.IDataSource, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	dsn, err := BuildDSN(cfg)
	if err != nil {
		return nil, err
	}

	gormConfig := postgres.Config{DSN: dsn, PreferSimpleProtocol: cfg.PreferSimpleProtocol}
	db, err := gorm.Open(postgres.New(gormConfig), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: cannot open connection to %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, err)
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

	return &gormPostgresDataSource{db: db}, nil
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
func BuildDSN(cfg dsconfig.DataSource) (string, error) {
	pairs := make([]string, 0, 8+len(cfg.Options))

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

	for _, key := range cfg.SortedOptionKeys() {
		if err := validateDSNKey(key); err != nil {
			return "", err
		}
		add(key, cfg.Options[key])
	}

	return strings.Join(pairs, " "), nil
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
// this datasource hand it to every builder they produce.
func (ds *gormPostgresDataSource) Dialect() core.SQLDialect {
	return &PostgresDialect{}
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
