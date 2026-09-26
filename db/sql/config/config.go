// Package config holds the typed datasource configuration shared by every SQL
// driver, plus the strict map->struct decoding that turns a `databases` entry in
// the app config into it.
//
// Before v2 each datasource parsed the raw map with unchecked type assertions
// (config["log"].(bool), props["maxOpenConnections"].(int), ...), so a missing or
// mistyped key panicked rather than returning an error. A hand-built config map —
// which is exactly what a test helper writes — took the process down.
package config

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Pool holds connection-pool settings. Zero values mean "leave the driver
// default alone".
type Pool struct {
	// MaxOpenConnections caps total open connections.
	MaxOpenConnections int
	// MaxIdleConnections caps idle connections retained in the pool.
	MaxIdleConnections int
	// ConnectionMaxLifetime caps how long a connection may be reused.
	ConnectionMaxLifetime time.Duration
	// ConnectionMaxIdleLifetime caps how long a connection may sit idle.
	ConnectionMaxIdleLifetime time.Duration
}

// DataSource is the driver-independent part of a datasource configuration.
type DataSource struct {
	// Driver names the registered driver, e.g. "postgres_gorm" or "mysql_gorm".
	Driver string
	// Host is the server hostname.
	Host string
	// Port is the server port.
	Port int
	// Username and Password authenticate the connection.
	Username string
	Password string
	// Database is the database (schema, on MySQL) name.
	Database string

	// SSL selects the transport security mode. On Postgres it is sslmode
	// (disable/require/verify-ca/verify-full); on MySQL it is tls
	// (false/true/skip-verify/preferred).
	SSL string

	// Instance names a SQL Server named instance, the part after the backslash in
	// host\instance. It is for the SQL Server engine; no engine in this build reads it.
	// Postgres and MySQL have no such concept and refuse the key rather than ignore it: a
	// config that names an instance was written for another engine, and connecting to
	// whatever answers on the default port would hide that.
	Instance string

	// SearchPath sets the schema search path the connection starts in. It is what
	// makes schema-per-test isolation possible: without it a consumer is forced
	// into CREATE DATABASE per test run plus truncate-between-tests.
	//
	// Postgres honours it directly. MySQL has no equivalent — a schema *is* a
	// database there — so the MySQL driver rejects it rather than ignoring it.
	SearchPath string

	// Options carries additional driver-specific DSN parameters verbatim, for
	// anything this struct does not model (Postgres: connect_timeout,
	// application_name; MySQL: charset, collation, loc). Keys are emitted in
	// sorted order so the DSN is reproducible.
	Options map[string]string

	// PreferSimpleProtocol disables implicit prepared statements. Postgres only;
	// required when connecting through a transaction-pooling proxy such as
	// PgBouncer.
	PreferSimpleProtocol bool

	// AllowUnfaithfulUpsert permits the MySQL dialect to translate
	// ON CONFLICT (cols) DO UPDATE into ON DUPLICATE KEY UPDATE, which fires on any
	// unique index rather than the conflict target the caller named. MySQL only;
	// Postgres expresses the construct exactly and ignores the key.
	//
	// It lives in the config because it is the only way an app using the ORM can reach
	// MySQLDialect.AllowUnfaithfulUpsert. Before H3, gormMySQLDataSource.Dialect()
	// returned a hard-coded &MySQLDialect{}, and session.Query() builds every builder
	// from that — so the documented opt-in could only be taken by constructing a builder
	// by hand with NewBuilderWithDialect and bypassing the ORM entirely. The flag was
	// unsettable through the path every app actually uses.
	//
	// Read the caveat in docs/DIALECTS.md before setting it: on a table with more than
	// one unique index MySQL's own manual advises against the clause, because which row
	// is updated is not the caller's to control.
	AllowUnfaithfulUpsert bool

	// ExternalSchema declares that this database's schema is owned outside gorgany — by
	// another application, its migrations tool, or a DBA. Every engine reads it.
	//
	// It exists because the framework's defaults assume ownership: db:migrate creates a
	// migrations table and runs DDL, db:seed records what it seeded, db:diff proposes
	// ALTERs, and the session store creates its own table. Against a schema another system
	// migrates, each of those is a write nobody reviewed.
	//
	// In this build the flag does two things. The datasource reports it through
	// core.PolicyOf, and its connection refuses DDL: Postgres and MySQL install the guard in
	// db/sql/gorm/guard, which refuses a schema change that reaches gorm. Nothing yet refuses
	// the rest up front. db:migrate, db:seed and db:diff still run, and fail at the first DDL
	// they send; a migration or seeder that only writes rows, into migrations and seeders
	// tables that already exist, still runs and is recorded; and the provider still attaches
	// the session store's migrations to such a default. Refusing those is the commands' and
	// the provider's job, which a later change adds. DDL the guard cannot see, such as a Postgres DO block or a procedure that
	// runs DDL, is not refused either, so a principal without DDL rights is the guarantee.
	ExternalSchema bool

	// ReadOnly declares that nothing may be written through this datasource. No engine in
	// this build enforces it, so Postgres and MySQL refuse the key, and driver.New refuses a
	// datasource from any other driver that does not report it (core.PolicyReporter): a flag
	// that is accepted but not enforced reads as a guarantee it is not. The SQL Server engine
	// that follows is to enforce it with a dialect that refuses writes, a guard on the
	// connection (guard.InstallReadOnly) and ApplicationIntent=ReadOnly.
	//
	// It is a safety net, not a permission: the guards recognise write statements, they do
	// not prove their absence. When a guarantee is what you need, connect as a principal
	// that can only read.
	ReadOnly bool

	// LazyConnect skips connecting at boot, so the first query opens the first connection
	// instead. Postgres and MySQL skip gorm's initial ping, and MySQL its version query. The
	// cost is that an unreachable server, or a credential that does not work, surfaces on the
	// first request instead of failing the deploy. On MySQL there is a second cost: without
	// the version query gorm's driver writes the SQL MySQL 8 takes, some of which MariaDB and
	// MySQL 5.x reject, so leave it off against those.
	LazyConnect bool

	// Auth selects how the connection signs in when a username and password are not the
	// whole story. The zero value, and Method "sql", mean exactly that: Username and
	// Password at the top level. Postgres and MySQL speak nothing else and refuse any other
	// method; the other methods are for the SQL Server engine that follows, and no engine in
	// this build reads them.
	Auth Auth

	// Log turns on statement logging for this connection.
	Log bool

	// Pool holds connection-pool settings.
	Pool Pool
}

// Auth is the `auth` block of a datasource: which sign-in method to use and the settings
// that method takes.
//
// Every field is a plain value, parsed strictly like the rest of DataSource, and none of
// them is interpreted here. Which fields a method requires, allows or refuses is the
// engine's rule to apply — a service principal needs a tenant and a client, an interactive
// sign-in refuses a client secret — because only the engine knows the methods it offers.
// Parse only guarantees that every key is one of ours and every value has the right type.
type Auth struct {
	// Method names the sign-in method, lowercased and trimmed when parsed so that a value
	// written as "Service_Principal " still selects one. Empty means "sql".
	Method string

	// TenantID is the Microsoft Entra tenant (directory) the principal belongs to.
	TenantID string

	// ClientID is the application (client) ID of the app registration or the user-assigned
	// managed identity that signs in.
	ClientID string

	// ClientSecret is a service principal's client secret. An unresolved ${VAR} placeholder
	// here stops the boot rather than being blanked like other keys; see
	// ResolveEnvPlaceholders in the framework's top-level config package.
	ClientSecret string

	// CertificatePath is the file holding a service principal's certificate and private
	// key, the alternative to ClientSecret.
	CertificatePath string

	// CertificatePassword decrypts the file at CertificatePath. Like ClientSecret, an
	// unresolved placeholder here stops the boot.
	CertificatePassword string

	// SendCertificateChain sends the certificate's whole chain rather than the leaf alone,
	// which subject-name/issuer authentication requires.
	SendCertificateChain bool

	// ResourceID and ObjectID select a user-assigned managed identity by its Azure
	// resource ID or its principal's object ID, as alternatives to ClientID.
	ResourceID string
	ObjectID   string

	// TokenFilePath is the file holding the projected service-account token a workload
	// identity signs in with.
	TokenFilePath string

	// RedirectURL is where an interactive sign-in returns to; it must match the app
	// registration's redirect URI.
	RedirectURL string

	// Scope overrides the token scope, which is otherwise derived from the host.
	Scope string

	// LoginTimeout bounds one sign-in, configured in seconds. Zero leaves the engine's
	// default in place; a negative value is refused.
	LoginTimeout time.Duration
}

// IsZero reports whether a is the zero value, which is what an absent `auth` block parses
// to.
func (a Auth) IsZero() bool { return a == Auth{} }

// Validate reports the first structural problem with c.
func (c *DataSource) Validate() error {
	if c.Host == "" {
		return fmt.Errorf("datasource config: 'host' is required")
	}
	if c.Database == "" {
		return fmt.Errorf("datasource config: 'db' is required")
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("datasource config: 'port' must be between 0 and 65535, got %d", c.Port)
	}
	if c.Pool.MaxOpenConnections < 0 {
		return fmt.Errorf("datasource config: 'properties.maxOpenConnections' must not be negative, got %d", c.Pool.MaxOpenConnections)
	}
	if c.Pool.MaxIdleConnections < 0 {
		return fmt.Errorf("datasource config: 'properties.maxIdleConnections' must not be negative, got %d", c.Pool.MaxIdleConnections)
	}
	// Parse refuses a negative login_timeout already; this catches a DataSource built by
	// hand, which never went through Parse.
	if c.Auth.LoginTimeout < 0 {
		return fmt.Errorf("datasource config: 'auth.login_timeout' must not be negative, got %s", c.Auth.LoginTimeout)
	}
	return nil
}

// knownKeys are the top-level keys Parse understands, lowercased. Anything else is
// reported rather than silently ignored, so a typo like "databse" surfaces at boot.
//
// Everything in this package matches keys case-insensitively, because Viper
// lowercases every key it reads: a YAML `maxOpenConnections` arrives as
// `maxopenconnections`. Comparing against a camelCase literal therefore never
// matches anything that came from a config file — which is how all four pool
// settings came to be silently ignored on every version before this fix, and how
// the unknown-key check below briefly turned that silence into a boot failure.
var knownKeys = map[string]bool{
	"driver": true, "host": true, "port": true, "username": true,
	"password": true, "db": true, "ssl": true, "search_path": true,
	"options": true, "prefer_simple_protocol": true, "log": true,
	"properties": true, "allow_unfaithful_upsert": true,
	"instance": true, "external_schema": true, "read_only": true,
	"lazy_connect": true, "auth": true,
}

// Parse decodes a raw `databases.<name>` map into a DataSource.
//
// Every failure names the offending key and the type that was expected. Viper
// hands numbers back as int, int64, float64 or string depending on whether the
// value came from YAML, an environment variable or a literal map, so numeric and
// boolean keys accept all of those rather than assuming one.
func Parse(rawInput map[string]any) (DataSource, error) {
	var cfg DataSource

	if rawInput == nil {
		return cfg, fmt.Errorf("datasource config: configuration is empty")
	}

	// Fold keys to lowercase so a hand-built camelCase map and a Viper-supplied
	// lowercased one behave identically.
	raw := foldKeys(rawInput)

	if unknown := unknownKeys(rawInput, knownKeys); len(unknown) > 0 {
		return cfg, unknownKeyError(unknown)
	}

	var err error
	if cfg.Driver, err = optString(raw, "driver"); err != nil {
		return cfg, err
	}
	if cfg.Host, err = optString(raw, "host"); err != nil {
		return cfg, err
	}
	if cfg.Port, err = optInt(raw, "port"); err != nil {
		return cfg, err
	}
	if cfg.Username, err = optString(raw, "username"); err != nil {
		return cfg, err
	}
	if cfg.Password, err = optPassword(raw); err != nil {
		return cfg, err
	}
	if cfg.Database, err = optString(raw, "db"); err != nil {
		return cfg, err
	}
	if cfg.SSL, err = optString(raw, "ssl"); err != nil {
		return cfg, err
	}
	if cfg.SearchPath, err = optString(raw, "search_path"); err != nil {
		return cfg, err
	}
	if cfg.Instance, err = optString(raw, "instance"); err != nil {
		return cfg, err
	}
	if cfg.PreferSimpleProtocol, err = optBool(raw, "prefer_simple_protocol"); err != nil {
		return cfg, err
	}
	if cfg.AllowUnfaithfulUpsert, err = optBool(raw, "allow_unfaithful_upsert"); err != nil {
		return cfg, err
	}
	if cfg.ExternalSchema, err = optBool(raw, "external_schema"); err != nil {
		return cfg, err
	}
	if cfg.ReadOnly, err = optBool(raw, "read_only"); err != nil {
		return cfg, err
	}
	if cfg.LazyConnect, err = optBool(raw, "lazy_connect"); err != nil {
		return cfg, err
	}
	if cfg.Log, err = optBool(raw, "log"); err != nil {
		return cfg, err
	}
	if cfg.Options, err = optStringMap(raw, "options"); err != nil {
		return cfg, err
	}
	if cfg.Pool, err = parsePool(raw); err != nil {
		return cfg, err
	}
	if cfg.Auth, err = parseAuth(raw); err != nil {
		return cfg, err
	}

	return cfg, nil
}

// knownPoolKeys are the `properties` keys, lowercased — see knownKeys for why.
var knownPoolKeys = map[string]bool{
	"maxopenconnections": true, "maxidleconnections": true,
	"connectionmaxlifetime": true, "connectionmaxidlelifetime": true,
}

func parsePool(raw map[string]any) (Pool, error) {
	var pool Pool

	propsRaw, ok := raw["properties"]
	if !ok || propsRaw == nil {
		return pool, nil
	}

	propsInput, ok := toStringMap(propsRaw)
	if !ok {
		return pool, fmt.Errorf("datasource config: key 'properties' must be a map, got %T", propsRaw)
	}
	props := foldKeys(propsInput)

	if unknown := unknownKeys(propsInput, knownPoolKeys); len(unknown) > 0 {
		// Quoted here rather than by unknownKeys, which returns bare names so
		// unknownKeyError can also feed them to the suggester.
		return pool, fmt.Errorf("datasource config: unknown key(s) %s under 'properties' — "+
			"recognised keys are %s",
			strings.Join(quoteAll(unknown), ", "), strings.Join(sortedKeysOf(knownPoolKeys), ", "))
	}

	var err error
	if pool.MaxOpenConnections, err = poolInt(props, "maxOpenConnections"); err != nil {
		return pool, err
	}
	if pool.MaxIdleConnections, err = poolInt(props, "maxIdleConnections"); err != nil {
		return pool, err
	}

	// Durations are configured in seconds, matching the pre-v2 behaviour.
	lifetime, err := poolInt(props, "connectionMaxLifetime")
	if err != nil {
		return pool, err
	}
	pool.ConnectionMaxLifetime = time.Duration(lifetime) * time.Second

	idle, err := poolInt(props, "connectionMaxIdleLifetime")
	if err != nil {
		return pool, err
	}
	pool.ConnectionMaxIdleLifetime = time.Duration(idle) * time.Second

	return pool, nil
}

// poolInt reads a `properties` entry by its lowercased key while reporting errors
// under the camelCase spelling the docs and config files use.
func poolInt(props map[string]any, camelKey string) (int, error) {
	return optIntUnder(props, "properties", strings.ToLower(camelKey), camelKey)
}

// knownAuthKeys are the `auth` keys, lowercased — see knownKeys for why. They are
// snake_case, like every other key the framework owns, even where the driver's own
// connection-string vocabulary spells the same setting differently.
var knownAuthKeys = map[string]bool{
	"method": true, "tenant_id": true, "client_id": true, "client_secret": true,
	"certificate_path": true, "certificate_password": true, "send_certificate_chain": true,
	"resource_id": true, "object_id": true, "token_file_path": true,
	"redirect_url": true, "scope": true, "login_timeout": true,
}

// topLevelUsername and topLevelPassword are the suggestions for a login name or password
// written inside `auth`. They are phrases rather than keys, because the keys they point to are
// not under `auth` at all.
const (
	topLevelUsername = "the top-level username"
	topLevelPassword = "the top-level password"
)

// authKeyAliases maps what someone arriving from an Azure portal snippet, a go-mssqldb
// connection string or an identity SDK writes to our key — see keyAliases for why edit
// distance cannot catch these.
//
// The login is the odd one out. Its name stays the datasource's own `username`, where an
// interactive sign-in reads it as the account hint, and its password the datasource's own
// `password`, so `user` or `password` inside `auth` is pointed back out of the block rather
// than given a second home in it — which is also what `method: sql` reads.
//
// The short forms of certificate_password are listed because edit distance gets them wrong,
// not merely misses them: certificate_pass is two edits from certificate_path and four from
// certificate_password, so on distance alone the reader would be told to put the password
// where the path goes.
var authKeyAliases = map[string]string{
	"tenant":              "tenant_id",
	"tenantid":            "tenant_id",
	"client":              "client_id",
	"clientid":            "client_id",
	"app_id":              "client_id",
	"application_id":      "client_id",
	"applicationclientid": "client_id",
	"secret":              "client_secret",
	"clientsecret":        "client_secret",
	"cert":                "certificate_path",
	"certificate":         "certificate_path",
	"cert_path":           "certificate_path",
	"clientcertpath":      "certificate_path",
	"cert_password":       "certificate_password",
	"cert_pass":           "certificate_password",
	"cert_pwd":            "certificate_password",
	"certificate_pass":    "certificate_password",
	"certificate_pwd":     "certificate_password",
	"type":                "method",
	"mode":                "method",
	"fedauth":             "method",
	"authentication":      "method",
	"login_hint":          topLevelUsername,
	"user":                topLevelUsername,
	"user_id":             topLevelUsername,
	"username":            topLevelUsername,
	"password":            topLevelPassword,
}

// parseAuth decodes the optional `auth` block, as strictly as parsePool decodes
// `properties`: an absent or null block is the zero Auth, anything but a map is refused, and
// an unrecognised key is refused with a suggestion.
//
// Strictness matters more here than anywhere else in the config. An ignored, misspelt pool
// key costs performance; an ignored, misspelt `tenant_id` signs in against the identity
// SDK's default tenant instead, and the failure that follows names neither the key nor the
// typo.
func parseAuth(raw map[string]any) (Auth, error) {
	var auth Auth

	authRaw, ok := raw["auth"]
	if !ok || authRaw == nil {
		return auth, nil
	}

	authInput, ok := toStringMap(authRaw)
	if !ok {
		return auth, fmt.Errorf("datasource config: key 'auth' must be a map, got %T", authRaw)
	}
	fields := foldKeys(authInput)

	if unknown := unknownKeys(authInput, knownAuthKeys); len(unknown) > 0 {
		parts := []string{fmt.Sprintf("datasource config: unknown key(s) %s under 'auth'",
			strings.Join(quoteAll(unknown), ", "))}
		for _, key := range unknown {
			nearest, found := nearestKey(key, knownAuthKeys, authKeyAliases)
			if !found {
				continue
			}
			if knownAuthKeys[nearest] {
				nearest = "'" + nearest + "'"
			}
			parts = append(parts, fmt.Sprintf("did you mean %s instead of '%s'?", nearest, key))
		}
		parts = append(parts, "recognised keys are "+strings.Join(sortedKeysOf(knownAuthKeys), ", "))
		return auth, errors.New(strings.Join(parts, " — "))
	}

	var err error
	if auth.Method, err = optStringUnder(fields, "auth", "method"); err != nil {
		return auth, err
	}
	// Folded like a key, because it is matched like one: `Service_Principal` and a value
	// with a trailing space from a hand-edited env file should select the method they name
	// rather than fail as an unknown one.
	auth.Method = strings.ToLower(strings.TrimSpace(auth.Method))

	for _, field := range []struct {
		key    string
		target *string
	}{
		{"tenant_id", &auth.TenantID},
		{"client_id", &auth.ClientID},
		{"certificate_path", &auth.CertificatePath},
		{"resource_id", &auth.ResourceID},
		{"object_id", &auth.ObjectID},
		{"token_file_path", &auth.TokenFilePath},
		{"redirect_url", &auth.RedirectURL},
		{"scope", &auth.Scope},
	} {
		if *field.target, err = optStringUnder(fields, "auth", field.key); err != nil {
			return auth, err
		}
	}

	if auth.ClientSecret, err = optSecretString(fields, "auth", "client_secret"); err != nil {
		return auth, err
	}
	if auth.CertificatePassword, err = optSecretString(fields, "auth", "certificate_password"); err != nil {
		return auth, err
	}

	if auth.SendCertificateChain, err = optBoolUnder(fields, "auth", "send_certificate_chain"); err != nil {
		return auth, err
	}

	// Seconds, like the pool's lifetimes.
	timeout, err := optIntUnder(fields, "auth", "login_timeout", "login_timeout")
	if err != nil {
		return auth, err
	}
	if timeout < 0 {
		return auth, fmt.Errorf("datasource config: key 'auth.login_timeout' must not be negative, got %d", timeout)
	}
	auth.LoginTimeout = time.Duration(timeout) * time.Second

	return auth, nil
}

// foldKeys returns m with every key lowercased. A collision (the same key in two
// cases) keeps the first in sorted order, so the result is deterministic.
func foldKeys(m map[string]any) map[string]any {
	folded := make(map[string]any, len(m))
	for _, k := range sortedMapKeys(m) {
		lower := strings.ToLower(k)
		if _, taken := folded[lower]; taken {
			continue
		}
		folded[lower] = m[k]
	}
	return folded
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// unknownKeys reports keys of raw that are not in known.
//
// Matching is done on the lowercased key, because `known` is lowercased and Viper
// lowercases what it reads, but the *reported* key is the caller's original
// spelling — being told your typo was 'maxopenconnection' when you wrote
// 'maxOpenConnection' is needlessly confusing.
func unknownKeys(raw map[string]any, known map[string]bool) []string {
	var unknown []string
	for k := range raw {
		if !known[strings.ToLower(k)] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// unknownKeyError explains an unrecognised key under `databases.<name>` well enough to act
// on, because rejecting it is a boot panic that a `go build`/`go vet` migration check does
// not catch.
//
// The rejection itself stays strict: it is what turns a typo like `databse` into a boot
// failure instead of a setting silently ignored, which is the defect it was added for. But
// a deliberate app-owned key lands here too, and the bare "unknown key(s) 'pool'" gave no
// hint that the framework now owns this namespace. A real app carried an app-owned `pool:`
// block here *because* the framework's own `properties` path was dead before v2 — so v2
// fixed `properties` and made the workaround fatal in the same release.
//
// A near-miss gets a suggestion; anything else gets the two ways out.
func unknownKeyError(unknown []string) error {
	var parts []string
	parts = append(parts, fmt.Sprintf("datasource config: unknown key(s) %s under this database",
		strings.Join(quoteAll(unknown), ", ")))

	for _, key := range unknown {
		if nearest, ok := nearestKnownKey(key); ok {
			parts = append(parts, fmt.Sprintf("did you mean '%s' instead of '%s'?", nearest, key))
		}
	}

	// The remedy is appended even when a suggestion was found, because the two answer
	// different questions and a boot-failure message has to be self-sufficient. 'pool' gets
	// "did you mean 'properties'?" — useful — but an app whose 'pool' block holds its own
	// settings needs to be told it can move them, not to rename them.
	parts = append(parts, fmt.Sprintf(
		"recognised keys are %s; if the key is your app's own, move it under 'properties' "+
			"(passed through untouched) or out from under 'databases.<name>' entirely",
		strings.Join(sortedKnownKeys(), ", ")))

	return errors.New(strings.Join(parts, " — "))
}

// keyAliases maps names a reasonable person writes instead of ours to the real key.
//
// Edit distance cannot catch these: they are vocabulary confusions, not typos. Someone
// arriving from a libpq connection string writes `sslmode` and `dbname`; someone from a
// MySQL DSN writes `user` and `pass`. Each is far enough from our spelling that no
// threshold would match, and close enough in intent that the reader is certain they got it
// right.
//
// The same holds for someone arriving from SQL Server: a go-mssqldb or ADO.NET connection
// string says `encrypt`, `server`, `fedauth` and `ApplicationIntent`, and someone used to an
// identity SDK writes `tenant_id` and `client_id` beside the host. The entries pointing into
// `auth.` name the nested key, since that is where the setting lives here.
//
// Some names are deliberately absent. `tenant` and `cache` say too little at the top level to
// point anywhere, and TestAnUnrelatedKeyGetsNoSuggestion pins that. `managed` reads the other
// way round from external_schema: `managed: false` renamed as suggested would become
// `external_schema: false`, which is the owned schema the reader meant to rule out. And
// `TrustServerCertificate` has no key here until an engine defines one: pointing it at
// `options` would hand Postgres and MySQL a parameter their servers refuse at connect time,
// which is a worse failure than this one at boot.
var keyAliases = map[string]string{
	"sslmode":     "ssl",
	"ssl_mode":    "ssl",
	"encrypt":     "ssl",
	"tls":         "ssl",
	"dbname":      "db",
	"db_name":     "db",
	"database":    "db",
	"user":        "username",
	"pass":        "password",
	"passwd":      "password",
	"searchpath":  "search_path",
	"schema":      "search_path",
	"hostname":    "host",
	"addr":        "host",
	"address":     "host",
	"server":      "host",
	"params":      "options",
	"parameters":  "options",
	"props":       "properties",
	"pool":        "properties",
	"connections": "properties",

	"instance_name": "instance",

	"fedauth":               "auth",
	"authentication":        "auth",
	"auth_method":           "auth",
	"authentication_method": "auth",
	"tenant_id":             "auth.tenant_id",
	"client_id":             "auth.client_id",
	"client_secret":         "auth.client_secret",

	"applicationintent":  "read_only",
	"application_intent": "read_only",
	"readonly":           "read_only",

	"unmanaged":    "external_schema",
	"schema_owner": "external_schema",

	"lazy": "lazy_connect",
}

// nearestKnownKey returns the top-level key closest to input; see nearestKey.
func nearestKnownKey(input string) (string, bool) {
	return nearestKey(input, knownKeys, keyAliases)
}

// nearestKey returns the key in known closest to input, when one is close enough to be a
// likely typo, or the alias for input when it is a known confusion. An alias may name
// something other than a key in known — a nested key such as auth.tenant_id, or a phrase —
// so the caller decides how to quote it.
//
// The edit-distance threshold is deliberately tight. Suggesting 'db' for 'pool' on distance
// alone would be worse than saying nothing: it reads as authoritative and sends the reader
// to rename a key that was never meant to be one of ours. (`pool` does get a suggestion, but
// from keyAliases, where it is a deliberate entry rather than a coincidence of spelling.)
func nearestKey(input string, known map[string]bool, aliases map[string]string) (string, bool) {
	lowered := strings.ToLower(input)

	if alias, ok := aliases[lowered]; ok {
		return alias, true
	}

	return Suggest(lowered, sortedKeysOf(known))
}

// editDistance is Damerau-Levenshtein, counting a transposition as one edit rather than
// two.
//
// Plain Levenshtein scores `prot` against `port` as 2, which a one-edit threshold rejects —
// and a transposed pair of adjacent letters is one of the most common typos there is. The
// full matrix is kept rather than two rows, because the transposition case needs the row
// before last.
func editDistance(a, b string) int {
	if a == b {
		return 0
	}

	rows := make([][]int, len(a)+1)
	for i := range rows {
		rows[i] = make([]int, len(b)+1)
		rows[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		rows[0][j] = j
	}

	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}

			rows[i][j] = min(rows[i-1][j]+1, min(rows[i][j-1]+1, rows[i-1][j-1]+cost))

			// Adjacent transposition.
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				rows[i][j] = min(rows[i][j], rows[i-2][j-2]+1)
			}
		}
	}

	return rows[len(a)][len(b)]
}

func sortedKnownKeys() []string { return sortedKeysOf(knownKeys) }

func sortedKeysOf(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func quoteAll(names []string) []string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, "'"+name+"'")
	}
	return quoted
}

func toStringMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[any]any:
		out := make(map[string]any, len(m))
		for k, val := range m {
			ks, ok := k.(string)
			if !ok {
				return nil, false
			}
			out[ks] = val
		}
		return out, true
	default:
		return nil, false
	}
}

func optString(raw map[string]any, key string) (string, error) {
	return optStringUnder(raw, "", key)
}

// optStringUnder reads raw[key], naming it parent.key in an error.
func optStringUnder(raw map[string]any, parent, key string) (string, error) {
	v, ok := raw[key]
	if !ok || v == nil {
		return "", nil
	}
	if s, ok := scalarString(v); ok {
		return s, nil
	}
	return "", fmt.Errorf("datasource config: key '%s' must be a string, got %T (%v)", qualify(parent, key), v, v)
}

// scalarString is v as the string optString accepts it as: a string itself, or an int or a
// bool YAML read from an unquoted literal.
func scalarString(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case int:
		return strconv.Itoa(s), true
	case int64:
		return strconv.FormatInt(s, 10), true
	case bool:
		return strconv.FormatBool(s), true
	default:
		return "", false
	}
}

// optPassword reads the top-level password as optString does, except that an error never
// shows the value, for optSecretString's reason.
//
// It still converts an unquoted int or bool, as optString does, rather than refusing it the
// way optSecretString does: `password: 123456` boots today, and refusing it would stop a
// working deploy over a value that reads back as written. An unquoted literal that does not
// read back — 0x1F, which YAML hands over as 31 — is a password that fails to sign in with
// or without this, and the sign-in error names the login.
func optPassword(raw map[string]any) (string, error) {
	v, ok := raw["password"]
	if !ok || v == nil {
		return "", nil
	}
	if s, ok := scalarString(v); ok {
		return s, nil
	}
	return "", fmt.Errorf("datasource config: key 'password' must be a string — quote it if it "+
		"is written inline — got %T (the value is not shown, because it is a secret)", v)
}

// optSecretString reads a secret, which must be a string, and never puts the value it
// found into an error.
//
// optString echoes the offending value, which is the right call for a host or a port and
// the wrong one here: a boot error lands in the deploy log, the terminal scrollback and
// whatever collects both, so a secret mistyped as a map or a number would be published by
// the very message that reports it.
//
// It also refuses the numbers and booleans optString converts. A YAML scalar that parses
// as one is no longer the text the operator wrote — an unquoted 0x1F becomes 31, and 1e3
// becomes 1000 — so converting it back would hand the identity provider a secret that
// looks right in the file and fails at sign-in. A value from a ${VAR} placeholder is
// always a string, so this only ever stops a literal that needed quotes.
func optSecretString(raw map[string]any, parent, key string) (string, error) {
	v, ok := raw[key]
	if !ok || v == nil {
		return "", nil
	}
	if s, isString := v.(string); isString {
		return s, nil
	}
	return "", fmt.Errorf("datasource config: key '%s' must be a string — quote it if it is "+
		"written inline — got %T (the value is not shown, because it is a secret)",
		qualify(parent, key), v)
}

func optInt(raw map[string]any, key string) (int, error) {
	return optIntUnder(raw, "", key, key)
}

// optIntUnder reads raw[key], reporting problems under displayKey so an error names
// the spelling the user actually wrote in their config.
func optIntUnder(raw map[string]any, parent, key, displayKey string) (int, error) {
	v, ok := raw[key]
	if !ok || v == nil {
		return 0, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case uint:
		return int(n), nil
	case uint64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("datasource config: key '%s' must be a whole number, got %v", qualify(parent, displayKey), v)
		}
		return int(n), nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, fmt.Errorf("datasource config: key '%s' must be an integer, got %q", qualify(parent, displayKey), n)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("datasource config: key '%s' must be an integer, got %T (%v)", qualify(parent, displayKey), v, v)
	}
}

func optBool(raw map[string]any, key string) (bool, error) {
	return optBoolUnder(raw, "", key)
}

// optBoolUnder reads raw[key], naming it parent.key in an error.
func optBoolUnder(raw map[string]any, parent, key string) (bool, error) {
	v, ok := raw[key]
	if !ok || v == nil {
		return false, nil
	}
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(b))
		if err != nil {
			return false, fmt.Errorf("datasource config: key '%s' must be a boolean, got %q", qualify(parent, key), b)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("datasource config: key '%s' must be a boolean, got %T (%v)", qualify(parent, key), v, v)
	}
}

func optStringMap(raw map[string]any, key string) (map[string]string, error) {
	v, ok := raw[key]
	if !ok || v == nil {
		return nil, nil
	}

	m, ok := toStringMap(v)
	if !ok {
		return nil, fmt.Errorf("datasource config: key '%s' must be a map of strings, got %T", key, v)
	}
	// Option keys are NOT folded: they are passed to the driver verbatim, and some
	// drivers are case-sensitive about them. Viper will already have lowercased
	// anything read from a config file — see the note on DataSource.Options.

	// An error names the option and never its value: options are where a driver's own
	// credentials go when the typed config has no key for them, so a value may be a secret.
	out := make(map[string]string, len(m))
	for k, val := range m {
		if val == nil {
			out[k] = ""
			continue
		}
		s, ok := scalarString(val)
		if !ok {
			return nil, fmt.Errorf("datasource config: key '%s.%s' must be a scalar, got %T (the value is not shown, since an option may carry a credential)", key, k, val)
		}
		out[k] = s
	}
	return out, nil
}

func qualify(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// SortedOptionKeys returns the Options keys in lexical order, so a DSN built from
// them is byte-for-byte reproducible.
func (c *DataSource) SortedOptionKeys() []string {
	keys := make([]string, 0, len(c.Options))
	for k := range c.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
