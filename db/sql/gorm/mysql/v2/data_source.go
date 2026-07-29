package v2

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	dsconfig "github.com/osbits/gorgany/db/sql/config"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Defaults applied when the config leaves them unset. utf8mb4 is the only charset
// that covers the whole BMP plus emoji; utf8mb4_unicode_ci gives the
// case-insensitive comparison the dialect's ILIKE -> LIKE rewrite relies on.
const (
	DefaultCharset   = "utf8mb4"
	DefaultCollation = "utf8mb4_unicode_ci"
	DefaultPort      = 3306
)

// gormMySQLDataSource implements dbCore.IDataSource over gorm.io/driver/mysql.
type gormMySQLDataSource struct {
	db *gorm.DB
}

// NewDataSource creates a MySQL datasource from a raw `databases.<name>` map.
// The map is validated and every failure names the offending key and the type
// that was expected.
func NewDataSource(config map[string]any) (dbCore.IDataSource, error) {
	cfg, err := dsconfig.Parse(config)
	if err != nil {
		return nil, err
	}
	return NewDataSourceWithConfig(cfg)
}

// NewDataSourceWithConfig creates a MySQL datasource from a typed config.
func NewDataSourceWithConfig(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	dsn, err := BuildDSN(cfg)
	if err != nil {
		return nil, err
	}

	db, err := gorm.Open(mysql.New(mysql.Config{DSN: dsn}), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("mysql: cannot open connection to %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, err)
	}

	rawDb, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("mysql: cannot reach underlying *sql.DB: %w", err)
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

	return &gormMySQLDataSource{db: db}, nil
}

// BuildDSN renders cfg as a go-sql-driver/mysql DSN:
//
//	user:password@tcp(host:port)/dbname?param=value&...
//
// Credentials, host and database name are escaped for their DSN positions, and
// query parameters go through url.Values so a value containing '&' or '=' cannot
// smuggle in a second parameter.
//
// charset and collation default to utf8mb4 / utf8mb4_unicode_ci, and parseTime is
// forced on: without it the driver hands DATETIME columns back as []byte and
// every time.Time field in every model fails to scan.
//
// SearchPath is rejected. MySQL has no schema search path — a schema *is* a
// database — so honouring it is impossible and ignoring it would silently connect
// to the wrong place.
func BuildDSN(cfg dsconfig.DataSource) (string, error) {
	if cfg.SearchPath != "" {
		return "", dbCore.Unsupported(DialectName, "search_path",
			"MySQL has no schema search path; point 'db' at the schema you want")
	}

	port := cfg.Port
	if port == 0 {
		port = DefaultPort
	}

	params := url.Values{}
	// parseTime must be on for time.Time scanning to work at all.
	params.Set("parseTime", "true")
	params.Set("charset", DefaultCharset)
	params.Set("collation", DefaultCollation)

	// tls is the MySQL analogue of Postgres' sslmode.
	if cfg.SSL != "" {
		params.Set("tls", cfg.SSL)
	}

	// Explicit options win over the defaults above.
	for _, key := range sortedKeys(cfg.Options) {
		if err := validateDSNParam(key); err != nil {
			return "", err
		}
		params.Set(canonicalDSNParam(key), cfg.Options[key])
	}

	var auth strings.Builder
	if cfg.Username != "" {
		auth.WriteString(cfg.Username)
		if cfg.Password != "" {
			auth.WriteString(":")
			auth.WriteString(cfg.Password)
		}
		auth.WriteString("@")
	}

	return fmt.Sprintf("%stcp(%s:%d)/%s?%s",
		auth.String(),
		cfg.Host,
		port,
		cfg.Database,
		encodeSorted(params),
	), nil
}

// encodeSorted encodes params with keys in lexical order, so the DSN is
// byte-for-byte reproducible across runs.
func encodeSorted(params url.Values) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(params.Get(k)))
	}
	return strings.Join(pairs, "&")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// dsnParamCanonicalNames maps the lowercased form of every go-sql-driver/mysql DSN
// parameter to the spelling the driver actually expects.
//
// This exists because Viper lowercases every key it reads, so an `options` entry
// written as `readTimeout: 10s` in YAML arrives here as `readtimeout` — and
// go-sql-driver's parameter names are case-sensitive, so it would reject the DSN
// outright. Restoring the canonical spelling makes camelCase options expressible
// from a config file at all.
//
// A parameter not in this table is passed through unchanged: it may be a MySQL
// server system variable, which the driver forwards verbatim and which is
// case-insensitive on the server side.
var dsnParamCanonicalNames = map[string]string{
	"allowallfiles":            "allowAllFiles",
	"allowcleartextpasswords":  "allowCleartextPasswords",
	"allowfallbacktoplaintext": "allowFallbackToPlaintext",
	"allownativepasswords":     "allowNativePasswords",
	"allowoldpasswords":        "allowOldPasswords",
	"charset":                  "charset",
	"checkconnliveness":        "checkConnLiveness",
	"clientfoundrows":          "clientFoundRows",
	"collation":                "collation",
	"columnswithalias":         "columnsWithAlias",
	"connectionattributes":     "connectionAttributes",
	"interpolateparams":        "interpolateParams",
	"loc":                      "loc",
	"maxallowedpacket":         "maxAllowedPacket",
	"multistatements":          "multiStatements",
	"parsetime":                "parseTime",
	"readtimeout":              "readTimeout",
	"rejectreadonly":           "rejectReadOnly",
	"serverpubkey":             "serverPubKey",
	"timetruncate":             "timeTruncate",
	"timeout":                  "timeout",
	"tls":                      "tls",
	"writetimeout":             "writeTimeout",
}

// canonicalDSNParam restores the driver's expected spelling for a known parameter.
func canonicalDSNParam(key string) string {
	if canonical, ok := dsnParamCanonicalNames[strings.ToLower(key)]; ok {
		return canonical
	}
	return key
}

// validateDSNParam rejects parameter names that are not plain identifiers.
func validateDSNParam(key string) error {
	if key == "" {
		return fmt.Errorf("mysql: DSN option key must not be empty")
	}
	for _, r := range key {
		isAllowed := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_'
		if !isAllowed {
			return fmt.Errorf("mysql: invalid DSN option key %q: only letters, digits and '_' are allowed", key)
		}
	}
	return nil
}

// Dialect returns the SQL dialect this connection speaks.
func (ds *gormMySQLDataSource) Dialect() dbCore.SQLDialect {
	return &MySQLDialect{}
}

// GetDriver returns the underlying *gorm.DB.
func (ds *gormMySQLDataSource) GetDriver() (any, error) {
	return ds.db, nil
}

// Close closes the database connection.
func (ds *gormMySQLDataSource) Close() error {
	sqlDB, err := ds.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
