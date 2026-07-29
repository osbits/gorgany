package testsupport

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Isolation is how the harness keeps one test's rows out of the next test's way.
type Isolation string

const (
	// IsolateByTruncation empties every migrated table after each test.
	//
	// The default, because it is the strategy that cannot silently mislead: it works
	// with code that commits, spawns goroutines, or opens its own sessions.
	IsolateByTruncation Isolation = "truncate"

	// IsolateByRollback runs each test in a transaction and rolls it back.
	//
	// Faster, and leaves nothing behind even when a test panics — but the code under
	// test must use the session the harness provides. Anything that opens its own
	// connection will not see the uncommitted rows, which looks like a bug in the code
	// rather than in the harness.
	IsolateByRollback Isolation = "rollback"
)

// Environment variables the harness reads. A suite configures the engine without a code
// change, which is what lets one test file run against both Postgres and MySQL in CI.
const (
	EnvDriver      = "GORGANY_TEST_DRIVER"
	EnvHost        = "GORGANY_TEST_HOST"
	EnvPort        = "GORGANY_TEST_PORT"
	EnvUser        = "GORGANY_TEST_USER"
	EnvPassword    = "GORGANY_TEST_PASSWORD"
	EnvDatabase    = "GORGANY_TEST_DB"
	EnvSSL         = "GORGANY_TEST_SSL"
	EnvIsolation   = "GORGANY_TEST_ISOLATION"
	EnvEngineWait  = "GORGANY_TEST_ENGINE_WAIT"
	EnvKeepData    = "GORGANY_TEST_KEEP_DATA"
	EnvMigrateDown = "GORGANY_TEST_MIGRATE_DOWN"
)

// Driver names, matching the keys of the framework's driver registry.
const (
	DriverPostgres = "postgres_gorm"
	DriverMySQL    = "mysql_gorm"
)

// DatabaseConfig describes one engine to test against.
type DatabaseConfig struct {
	// Name labels this engine in test output and subtest names. Empty means Driver.
	Name string

	// Driver is a key of the framework's driver registry: DriverPostgres or DriverMySQL.
	Driver string

	Host     string
	Port     int
	User     string
	Password string
	Database string

	// SSL is the Postgres sslmode. Ignored by MySQL.
	SSL string
}

// Label is how this database appears in test output.
func (c DatabaseConfig) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Driver
}

// Validate reports a config that cannot produce a connection.
func (c DatabaseConfig) Validate() error {
	var missing []string
	if c.Driver == "" {
		missing = append(missing, "Driver")
	}
	if c.Host == "" {
		missing = append(missing, "Host")
	}
	if c.Port == 0 {
		missing = append(missing, "Port")
	}
	if c.Database == "" {
		missing = append(missing, "Database")
	}

	if len(missing) > 0 {
		return fmt.Errorf("testsupport: database config %q is missing %s",
			c.Label(), strings.Join(missing, ", "))
	}
	return nil
}

// datasourceConfig renders the map the framework's driver registry expects.
func (c DatabaseConfig) datasourceConfig() map[string]any {
	cfg := map[string]any{
		"driver":   c.Driver,
		"host":     c.Host,
		"port":     c.Port,
		"username": c.User,
		"password": c.Password,
		"db":       c.Database,
	}

	if c.Driver == DriverPostgres {
		ssl := c.SSL
		if ssl == "" {
			ssl = "disable"
		}
		cfg["ssl"] = ssl
	}

	return cfg
}

// Config is the harness's settings.
type Config struct {
	// Databases are the engines to test against. Empty means one entry built from the
	// environment (see FromEnv).
	Databases []DatabaseConfig

	// Isolation is how rows are kept out of the next test's way. Empty means
	// IsolateByTruncation.
	Isolation Isolation

	// EngineWait is how long to keep retrying the connection before giving up. Zero
	// means DefaultEngineWait.
	//
	// It exists because a container started in the same CI step is usually not accepting
	// connections yet, and a suite that fails on the first refused dial is a suite that
	// fails intermittently.
	EngineWait time.Duration

	// KeepData leaves rows in place after each test. For debugging a failure by hand;
	// tests will interfere with each other.
	KeepData bool

	// MigrateDown runs every migration's Down before Up, so a schema left behind by an
	// interrupted run does not poison the next one.
	MigrateDown bool
}

// DefaultEngineWait is how long the harness waits for an engine to accept connections.
const DefaultEngineWait = 30 * time.Second

// FromEnv builds a Config from the environment, with sensible defaults.
//
// Defaults are the Postgres container from docs/TESTING.md — 127.0.0.1:5433,
// postgres/test, gorgany_test — so a developer who starts that container needs no
// environment at all.
func FromEnv() Config {
	driver := envOr(EnvDriver, DriverPostgres)

	cfg := Config{
		Databases: []DatabaseConfig{{
			Driver:   driver,
			Host:     envOr(EnvHost, "127.0.0.1"),
			Port:     envIntOr(EnvPort, defaultPortFor(driver)),
			User:     envOr(EnvUser, defaultUserFor(driver)),
			Password: envOr(EnvPassword, "test"),
			Database: envOr(EnvDatabase, "gorgany_test"),
			SSL:      envOr(EnvSSL, "disable"),
		}},
		Isolation:   Isolation(envOr(EnvIsolation, string(IsolateByTruncation))),
		KeepData:    envBool(EnvKeepData),
		MigrateDown: envBool(EnvMigrateDown),
	}

	if raw := os.Getenv(EnvEngineWait); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			cfg.EngineWait = parsed
		}
	}

	return cfg
}

// defaultPortFor is the port the engine's own container image publishes in
// docs/TESTING.md, not the engine's default port — a developer running this against a
// container mapped to the standard port has a real database on it.
func defaultPortFor(driver string) int {
	if driver == DriverMySQL {
		return 3307
	}
	return 5433
}

func defaultUserFor(driver string) string {
	if driver == DriverMySQL {
		return "root"
	}
	return "postgres"
}

// resolved fills in the defaults and validates.
func (c Config) resolved() (Config, error) {
	out := c

	if len(out.Databases) == 0 {
		out.Databases = FromEnv().Databases
	}
	if out.Isolation == "" {
		out.Isolation = IsolateByTruncation
	}
	if out.EngineWait == 0 {
		out.EngineWait = DefaultEngineWait
	}

	switch out.Isolation {
	case IsolateByTruncation, IsolateByRollback:
	default:
		return out, fmt.Errorf(
			"testsupport: unknown isolation %q; use %q or %q",
			out.Isolation, IsolateByTruncation, IsolateByRollback)
	}

	for _, database := range out.Databases {
		if err := database.Validate(); err != nil {
			return out, err
		}
	}

	return out, nil
}

func envOr(name, fallback string) string {
	// LookupEnv, not Getenv: an explicitly empty password is a real value, and
	// substituting the default for it would silently connect as something else.
	if value, present := os.LookupEnv(name); present {
		return value
	}
	return fallback
}

func envIntOr(name string, fallback int) int {
	raw, present := os.LookupEnv(name)
	if !present || raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func envBool(name string) bool {
	value, err := strconv.ParseBool(os.Getenv(name))
	return err == nil && value
}
