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

	// Log turns on statement logging for this connection.
	Log bool

	// Pool holds connection-pool settings.
	Pool Pool
}

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
	"properties": true,
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
		return cfg, fmt.Errorf("datasource config: unknown key(s) %s", strings.Join(unknown, ", "))
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
	if cfg.Password, err = optString(raw, "password"); err != nil {
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
	if cfg.PreferSimpleProtocol, err = optBool(raw, "prefer_simple_protocol"); err != nil {
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
		return pool, fmt.Errorf("datasource config: unknown key(s) %s under 'properties'", strings.Join(unknown, ", "))
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
			unknown = append(unknown, "'"+k+"'")
		}
	}
	sort.Strings(unknown)
	return unknown
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
	v, ok := raw[key]
	if !ok || v == nil {
		return "", nil
	}
	switch s := v.(type) {
	case string:
		return s, nil
	case int:
		return strconv.Itoa(s), nil
	case int64:
		return strconv.FormatInt(s, 10), nil
	case bool:
		return strconv.FormatBool(s), nil
	default:
		return "", fmt.Errorf("datasource config: key '%s' must be a string, got %T (%v)", key, v, v)
	}
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
			return false, fmt.Errorf("datasource config: key '%s' must be a boolean, got %q", key, b)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("datasource config: key '%s' must be a boolean, got %T (%v)", key, v, v)
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

	out := make(map[string]string, len(m))
	for k, val := range m {
		s, err := optString(map[string]any{k: val}, k)
		if err != nil {
			return nil, fmt.Errorf("datasource config: key '%s.%s' must be a scalar, got %T (%v)", key, k, val, val)
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
