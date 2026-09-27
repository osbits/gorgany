package v2

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/db/sql/gorm/guard"
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

	// allowUnfaithfulUpsert carries databases.<name>.allow_unfaithful_upsert through to
	// the dialect. See dsconfig.DataSource.AllowUnfaithfulUpsert for why the config is
	// the only route: everything downstream builds its builder from Dialect().
	allowUnfaithfulUpsert bool

	// externalSchema carries databases.<name>.external_schema, which Policy reports. The
	// connection's DDL guard is installed from the same flag, so the two cannot disagree.
	externalSchema bool

	// readOnly carries databases.<name>.read_only, which Policy reports and Dialect hands
	// every builder. The connection's write guard is installed from the same flag, so none of
	// them can disagree.
	readOnly bool
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
//
// Settings MySQL cannot honour are refused before anything is opened; see refuseUnsupported.
// With external_schema, the connection refuses DDL (see guard.InstallExternalSchema) from
// before the first statement anyone can send on it.
//
// With read_only, writes are refused in two places, the second of which sees what the first
// cannot. The dialect refuses to render a write, so a builder from this datasource's sessions
// and transactions fails where the write is built (MySQLDialect.ReadOnly). The guard on the
// connection refuses one that reaches gorm any other way — an app's Exec or Raw, gorm's
// Create, Update and Delete, the Migrator — from before the first statement anyone can send on
// it (guard.InstallReadOnly). Unlike Postgres, the server is not asked to make the session
// read-only; BuildDSN says why. So a write the guard never sees, such as one sent on the
// *sql.DB that DB() returns, or one a stored routine the statement calls runs, reaches the
// server as any other would. Both are a safety net; a user that can only read is the
// guarantee.
//
// Without lazy_connect the constructor talks to the server twice, and an unreachable one
// fails it, as it always has: gorm.io/driver/mysql asks for SELECT VERSION() while it
// initialises, and gorm pings. With lazy_connect it does neither, so the first query makes the
// first connection. Skipping the version query has a cost, because the driver uses the answer
// to choose some of the SQL it writes. Without it the driver assumes a current MySQL (8.0 or
// later): its Migrator writes RENAME COLUMN, RENAME INDEX, ALTER TABLE … DROP CONSTRAINT and
// DATETIME(3) columns, a shared lock is FOR SHARE, and nothing uses RETURNING. MariaDB and
// MySQL 5.x reject some of those, so against them leave lazy_connect off. That is
// gorm.io/driver/mysql v1.6.0's behaviour; a later version may choose differently.
func NewDataSourceWithConfig(cfg dsconfig.DataSource) (dbCore.IDataSource, error) {
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

	dialector := mysql.New(mysql.Config{DSN: dsn, SkipInitializeWithVersion: cfg.LazyConnect})
	db, err := gorm.Open(dialector, &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
		DisableAutomaticPing:                     cfg.LazyConnect,
		Logger:                                   logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("mysql: cannot open connection to %s:%d/%s: %w", cfg.Host, cfg.Port, cfg.Database, err)
	}

	ds, err := fromOpened(db, cfg)
	if err != nil {
		return nil, closeAfter(db, err)
	}
	return ds, nil
}

// fromOpened finishes a datasource on db, the handle gorm.Open returned for cfg: it installs
// the guards cfg's policy asks for, applies the pool settings and statement logging, and
// carries the flags the datasource reports and the dialect it speaks.
//
// It is split from NewDataSourceWithConfig, which opens db from a DSN, so that a test can
// finish a datasource on a handle over a driver of its own and see exactly what reaches it.
// The caller closes db when it fails.
func fromOpened(db *gorm.DB, cfg dsconfig.DataSource) (*gormMySQLDataSource, error) {
	// Installed on the handle gorm.Open returned, before Debug() below derives one from it:
	// every handle derived from it shares its callbacks and its guarded pool, so the guards
	// cover the logging handle, every session and every transaction.
	if cfg.ExternalSchema {
		if err := guard.InstallExternalSchema(db, dbCore.LexiconMySQL); err != nil {
			return nil, fmt.Errorf("mysql: %w", err)
		}
	}
	if cfg.ReadOnly {
		if err := guard.InstallReadOnly(db, dbCore.LexiconMySQL); err != nil {
			return nil, fmt.Errorf("mysql: %w", err)
		}
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

	return &gormMySQLDataSource{
		db:                    db,
		allowUnfaithfulUpsert: cfg.AllowUnfaithfulUpsert,
		externalSchema:        cfg.ExternalSchema,
		readOnly:              cfg.ReadOnly,
	}, nil
}

// refuseUnsupported refuses the datasource settings MySQL does not honour, each with an
// UnsupportedError that names the key.
//
// Every one of them is refused rather than ignored, because ignoring it would change what the
// config means without saying so:
//
//   - instance names a SQL Server named instance. A config that sets it was written for
//     another engine, and connecting to whatever answers on the host's default port would
//     hide that.
//   - auth selects a sign-in method, and MySQL here signs in one way only: the top-level
//     username and password, which is what method "sql" and an absent block mean. Any other
//     method, or any other setting under auth, would be dropped on the floor, and the
//     sign-in that followed would not be the one configured.
//
// external_schema, read_only and lazy_connect are honoured, so they are not here. search_path
// is refused by BuildDSN, which is where it would otherwise be dropped.
func refuseUnsupported(cfg dsconfig.DataSource) error {
	if cfg.Instance != "" {
		return dbCore.Unsupported(DialectName, "instance", "named instances are a SQL Server concept")
	}
	return refuseNonSQLAuth(cfg.Auth)
}

// refuseNonSQLAuth refuses an auth block that asks for anything but the top-level username
// and password. The error names the method, which is a word from the config's vocabulary, and
// never a value from any other auth key, which may be a secret.
func refuseNonSQLAuth(auth dsconfig.Auth) error {
	method := strings.ToLower(strings.TrimSpace(auth.Method))
	if method != "" && method != "sql" {
		return dbCore.Unsupported(DialectName, fmt.Sprintf("auth method %q", auth.Method),
			"MySQL signs in with the top-level username and password only; remove the auth block or set method: sql")
	}
	auth.Method = ""
	if !auth.IsZero() {
		return dbCore.Unsupported(DialectName, "auth settings other than method",
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
//
// ReadOnly adds nothing here, though Postgres's BuildDSN asks its server for read-only
// transactions. MySQL's equivalent is a session variable, and go-sql-driver/mysql sends every
// parameter it does not know as `SET <name> = <value>` right after it connects, so an unknown
// variable fails that SET and with it every connection. No one name is known to all the
// servers this engine meets: transaction_read_only exists from MySQL 5.7.20 and in MariaDB
// from 11.1, and tx_read_only in MySQL before 8.0.3 and in every MariaDB, so MySQL 8 refuses
// tx_read_only, and MySQL 5.7 before 5.7.20 and MariaDB before 11.1 refuse
// transaction_read_only. The DSN is built before the server has said what it is, and with
// lazy_connect it is never asked. So a read_only MySQL datasource relies on the dialect, the
// guard and, as the guarantee, a user that can only read. An operator who knows the server
// can still set the variable its server has under options, which this passes through as it
// would any other.
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
//
// The upsert flag is threaded from the config here rather than left at its zero value,
// which is what made the opt-in unreachable: NewSession and Transaction both build their
// builders from this method, so a hard-coded &MySQLDialect{} meant no ORM caller could ever
// set it.
func (ds *gormMySQLDataSource) Dialect() dbCore.SQLDialect {
	return &MySQLDialect{AllowUnfaithfulUpsert: ds.allowUnfaithfulUpsert, ReadOnly: ds.readOnly}
}

// Policy reports what the datasource's configuration allows (see dbCore.DataSourcePolicy).
func (ds *gormMySQLDataSource) Policy() dbCore.DataSourcePolicy {
	return dbCore.DataSourcePolicy{ExternalSchema: ds.externalSchema, ReadOnly: ds.readOnly}
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
