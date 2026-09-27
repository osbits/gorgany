package testsupport

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/osbits/gorgany/v2/db/sql/driver"
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
// change, which is what lets one test file run against Postgres, MySQL and SQL Server in CI.
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

	// EnvAllowAnyTarget switches off the target guard; see Config.AllowAnyTarget.
	EnvAllowAnyTarget = "GORGANY_TEST_ALLOW_ANY_TARGET"

	// EnvTrustServerCert sets DatabaseConfig.TrustServerCertificate; false makes a SQL Server
	// connection verify the server's certificate. Postgres and MySQL ignore it.
	EnvTrustServerCert = "GORGANY_TEST_TRUST_SERVER_CERT"
)

// Driver names, matching the keys of the framework's driver registry.
const (
	DriverPostgres = "postgres_gorm"
	DriverMySQL    = "mysql_gorm"

	// DriverSQLServer serves SQL Server and Azure SQL, though the target guard refuses an Azure
	// SQL host unless AllowAnyTarget is set.
	//
	// It is a string, not a reference to the engine's package, because testsupport must not
	// link SQL Server's driver into every app's test binary. testsupport registers only
	// Postgres and MySQL, so a suite that tests against SQL Server imports its driver itself,
	// _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver", and the harness says so when
	// the import is missing.
	DriverSQLServer = "sqlserver_gorm"
)

// DatabaseConfig describes one engine to test against.
type DatabaseConfig struct {
	// Name labels this engine in test output and subtest names. Empty means Driver.
	Name string

	// Driver is a key of the framework's driver registry: DriverPostgres, DriverMySQL or
	// DriverSQLServer.
	Driver string

	Host     string
	Port     int
	User     string
	Password string
	Database string

	// SSL is the Postgres sslmode, empty meaning disable, and SQL Server's encryption mode
	// (see dsconfig.DataSource.SSL), empty meaning encrypted. Ignored by MySQL.
	SSL string

	// TrustServerCertificate is SQL Server's options.trust_server_certificate, "true" or
	// "false". Ignored by Postgres and MySQL.
	//
	// Empty means true, so that the connection accepts the self-signed certificate a SQL Server
	// container generates when it starts, and is still encrypted; the harness is for such a
	// container. It means false on an Azure SQL host, whose certificate always verifies and
	// which the engine refuses to trust blindly, and with SSL strict, which verifies the
	// certificate by definition. Set it to false to verify a server whose certificate a trusted
	// authority signed.
	//
	// It is a string rather than a bool because the zero value has to mean "the default",
	// which depends on the host, as an empty SSL does.
	TrustServerCertificate string
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

	// Checked here rather than left to the driver, which refuses a value that is not a boolean
	// with an error it reports on every attempt to connect: the harness would retry it for the
	// whole engine wait and then skip, as though the engine were not running.
	if c.Driver == DriverSQLServer && c.TrustServerCertificate != "" {
		if _, err := strconv.ParseBool(c.TrustServerCertificate); err != nil {
			return fmt.Errorf("testsupport: database config %q: TrustServerCertificate %q is not "+
				"true or false (%s sets it)", c.Label(), c.TrustServerCertificate, EnvTrustServerCert)
		}
	}

	// The same holds for an SSL value SQL Server's engine does not know, which the harness
	// forwards to it. The likeliest is a Postgres sslmode such as require, still exported in
	// GORGANY_TEST_SSL after GORGANY_TEST_DRIVER was switched to SQL Server.
	if c.Driver == DriverSQLServer && !sqlServerSSLModes[strings.ToLower(strings.TrimSpace(c.SSL))] {
		return fmt.Errorf("testsupport: database config %q: SSL %q is not a SQL Server encryption "+
			"mode; use true (the default), strict, false or disable (%s sets it)", c.Label(), c.SSL, EnvSSL)
	}
	return nil
}

// sqlServerSSLModes are the SSL values SQL Server's engine accepts, compared in lower case with
// surrounding space trimmed, as it compares them: "" and its synonyms for encrypted, strict, the
// synonyms for false, and disable. testsupport cannot ask the engine, which it does not link, so
// TestTheHarnessAcceptsTheSSLModesTheEngineDoes keeps this list and the engine's together.
var sqlServerSSLModes = map[string]bool{
	"": true, "true": true, "mandatory": true, "yes": true, "1": true,
	"strict": true,
	"false":  true, "optional": true, "no": true, "0": true,
	"disable": true,
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

	if c.Driver == DriverSQLServer {
		// An empty SSL is left out rather than defaulted to Postgres's disable: on SQL Server
		// that turns encryption off, and the engine's own default, encrypted, is what the
		// container supports.
		if c.SSL != "" {
			cfg["ssl"] = c.SSL
		}
		if trust := c.trustServerCertificate(); trust != "" {
			cfg["options"] = map[string]any{"trust_server_certificate": trust}
		}
	}

	return cfg
}

// trustServerCertificate is the options.trust_server_certificate a SQL Server datasource gets,
// or "" for none; see DatabaseConfig.TrustServerCertificate.
//
// The default is left out, rather than rendered as false, where the engine refuses a trusted
// certificate, so that a config the engine would refuse is never one the harness wrote. A value
// the caller set is rendered whatever the host, and the engine refuses it where it must.
func (c DatabaseConfig) trustServerCertificate() string {
	if c.TrustServerCertificate != "" {
		if trust, err := strconv.ParseBool(c.TrustServerCertificate); err == nil {
			return strconv.FormatBool(trust)
		}
		return c.TrustServerCertificate
	}
	if dsconfig.IsAzureSQLHost(c.Host) || strings.EqualFold(strings.TrimSpace(c.SSL), "strict") {
		return ""
	}
	return "true"
}

// Config is the harness's settings.
//
// A field left zero is taken from the environment (see FromEnv), and from the default when
// its variable is unset too. A field set here keeps its value whatever the environment
// says. So a zero Config is exactly FromEnv, and a suite that sets only Isolation still
// honours GORGANY_TEST_ENGINE_WAIT.
type Config struct {
	// Databases are the engines to test against. Empty means one entry built from the
	// environment.
	Databases []DatabaseConfig

	// Isolation is how rows are kept out of the next test's way. Empty means
	// GORGANY_TEST_ISOLATION, else IsolateByTruncation.
	Isolation Isolation

	// EngineWait is how long to keep retrying the connection before giving up. Zero
	// means GORGANY_TEST_ENGINE_WAIT, else DefaultEngineWait.
	//
	// It exists because a container started in the same CI step is usually not accepting
	// connections yet, and a suite that fails on the first refused dial is a suite that
	// fails intermittently.
	EngineWait time.Duration

	// KeepData leaves rows in place after each test. For debugging a failure by hand;
	// tests will interfere with each other.
	//
	// GORGANY_TEST_KEEP_DATA turns it on even when this is false: a bool cannot say
	// "unset", and the variable is how you debug a suite without editing it.
	KeepData bool

	// MigrateDown runs every migration's Down before Up, so a schema left behind by an
	// interrupted run does not poison the next one.
	//
	// GORGANY_TEST_MIGRATE_DOWN turns it on even when this is false, as with KeepData.
	MigrateDown bool

	// AllowAnyTarget switches off the target guard, which refuses to point the harness at a
	// database it may not empty: an Azure SQL host, a driver it does not know how to
	// truncate, and a SQL Server database whose name does not say it is for tests. See
	// guardTarget for why each is refused.
	//
	// The harness empties every table its migrations create, before the first test and
	// after each one, so a stray GORGANY_TEST_HOST left over from a debugging session is all
	// it takes to empty a database somebody needed. Set this only for a target you know is
	// disposable. A driver that is not registered is refused either way, since no setting
	// can make it connect.
	//
	// GORGANY_TEST_ALLOW_ANY_TARGET switches it on even when this is false, as with
	// KeepData: the variable switches it on, never off.
	AllowAnyTarget bool
}

// DefaultEngineWait is how long the harness waits for an engine to accept connections.
const DefaultEngineWait = 30 * time.Second

// FromEnv builds a Config from the environment, with sensible defaults.
//
// Defaults are the Postgres container from docs/TESTING.md — 127.0.0.1:5433,
// postgres/test, gorgany_test — so a developer who starts that container needs no
// environment at all. The port, user, password and SSL defaults follow GORGANY_TEST_DRIVER, to
// the MySQL or SQL Server container from the same page, so switching engines is one variable.
func FromEnv() Config {
	driver := envOr(EnvDriver, DriverPostgres)

	cfg := Config{
		Databases: []DatabaseConfig{{
			Driver:                 driver,
			Host:                   envOr(EnvHost, "127.0.0.1"),
			Port:                   envIntOr(EnvPort, defaultPortFor(driver)),
			User:                   envOr(EnvUser, defaultUserFor(driver)),
			Password:               envOr(EnvPassword, defaultPasswordFor(driver)),
			Database:               envOr(EnvDatabase, "gorgany_test"),
			SSL:                    envOr(EnvSSL, defaultSSLFor(driver)),
			TrustServerCertificate: envOr(EnvTrustServerCert, ""),
		}},
		Isolation:      Isolation(envOr(EnvIsolation, string(IsolateByTruncation))),
		KeepData:       envBool(EnvKeepData),
		MigrateDown:    envBool(EnvMigrateDown),
		AllowAnyTarget: envBool(EnvAllowAnyTarget),
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
	switch driver {
	case DriverMySQL:
		return 3307
	case DriverSQLServer:
		return 14330
	default:
		return 5433
	}
}

// defaultUserFor is the administrator of the engine's container in docs/TESTING.md. On SQL
// Server that is sa, which the harness also needs for creating the test database.
func defaultUserFor(driver string) string {
	switch driver {
	case DriverMySQL:
		return "root"
	case DriverSQLServer:
		return "sa"
	default:
		return "postgres"
	}
}

// defaultPasswordFor is the administrator's password in the engine's container.
//
// SQL Server's is not the others' "test" because SQL Server refuses a password below its
// complexity policy, and its container then exits rather than start with one. The value is the
// one CI, e2e/docker-compose.yml and the e2e suite use; TestTheSQLServerTestSettingsAgreeEverywhere
// keeps them together.
func defaultPasswordFor(driver string) string {
	if driver == DriverSQLServer {
		return "Gorgany-Test-1"
	}
	return "test"
}

// defaultSSLFor is GORGANY_TEST_SSL's default. Postgres's disable, which is also what every
// other driver was given before SQL Server, would turn a SQL Server connection's encryption
// off, so SQL Server gets its own default, encrypted.
func defaultSSLFor(driver string) string {
	if driver == DriverSQLServer {
		return "true"
	}
	return "disable"
}

// resolved fills every zero field from the environment, then from the defaults, and
// validates.
//
// Every field, not only Databases: the default harness is New(Config{}), so a field
// resolved here without consulting FromEnv is a variable Main and RequireDatabase ignore.
func (c Config) resolved() (Config, error) {
	out := c
	env := FromEnv()

	if len(out.Databases) == 0 {
		out.Databases = env.Databases
	}
	if out.Isolation == "" {
		out.Isolation = env.Isolation
	}
	if out.EngineWait == 0 {
		out.EngineWait = env.EngineWait
	}
	out.KeepData = out.KeepData || env.KeepData
	out.MigrateDown = out.MigrateDown || env.MigrateDown
	out.AllowAnyTarget = out.AllowAnyTarget || env.AllowAnyTarget

	// An empty or unparseable variable leaves these zero too.
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

	// After Validate, so a config missing its host is reported as that. It covers Databases
	// set in code as well as the one from the environment: a suite's hard-coded list can
	// point at the wrong server as easily as a variable can.
	for _, database := range out.Databases {
		if err := guardTarget(database, out.AllowAnyTarget); err != nil {
			return out, err
		}
	}

	return out, nil
}

// guardTarget refuses a database the harness must not run against, and a config that cannot
// connect however long the harness waits.
//
// Its errors are config errors, so they fail the test whichever of RequireDatabase and
// MustDatabase asked. A refused target is not an absent engine: skipping would read as "no
// database running" to someone whose variables in fact point at a real one.
//
// The safety rules, which AllowAnyTarget switches off:
//
//   - An Azure SQL host is refused for any driver, and so is an Azure Database for
//     PostgreSQL or MySQL host (see azureDatabaseHostSuffixes), which is the Azure host a
//     Postgres or MySQL suite can actually reach. Truncation empties every table the
//     migrations created, and a cloud database is never the disposable one a harness is for;
//     the likeliest way to reach one is a production variable exported in the same shell.
//   - A driver other than Postgres, MySQL and SQL Server is refused. Isolation has to know how
//     to empty the engine's tables, and until this check an unknown driver was emptied as
//     though it were MySQL.
//   - A SQL Server database must have "test" as a word of its name (see namedForTests), and
//     must not be a system database. One SQL Server instance commonly hosts many databases,
//     some of them not disposable, such as a copy of production restored for debugging, so
//     the name is the one evidence the harness can check that this one is disposable.
//     Postgres and MySQL suites keep the names they had.
//
// The rest are not safety rules and hold whatever AllowAnyTarget says:
//
//   - The driver must be registered in this test binary. An unregistered one used to be
//     retried for the whole engine wait and then skipped, as though the engine were down; the
//     fix is an import, and the error names it.
//   - The datasource config the harness renders must parse, which is checked once here rather
//     than on every retry.
func guardTarget(database DatabaseConfig, allowAnyTarget bool) error {
	label := database.Label()

	if !allowAnyTarget {
		if kind := azureHostKind(database.Host); kind != "" {
			return fmt.Errorf("testsupport: %s: refusing %s, %s: the harness empties "+
				"every table its migrations create, so it runs only against a disposable local "+
				"engine. Point it at a container, or set %s=1 if this database really is disposable",
				label, database.Host, kind, EnvAllowAnyTarget)
		}

		switch database.Driver {
		case DriverPostgres, DriverMySQL:
		case DriverSQLServer:
			if err := guardSQLServerDatabase(label, database.Database); err != nil {
				return err
			}
		default:
			return fmt.Errorf("testsupport: %s: driver %q is not one the harness supports (%s, %s "+
				"or %s): isolation has to know how to empty its tables. Set %s=1 to use it anyway, "+
				"with IsolateByRollback",
				label, database.Driver, DriverPostgres, DriverMySQL, DriverSQLServer, EnvAllowAnyTarget)
		}
	}

	if err := requireRegistered(database); err != nil {
		return err
	}

	if _, err := dsconfig.Parse(database.datasourceConfig()); err != nil {
		return fmt.Errorf("testsupport: %s: %w", label, err)
	}
	return nil
}

// azureDatabaseHostSuffixes are the DNS suffixes of Azure Database for PostgreSQL and Azure
// Database for MySQL in the public cloud. dsconfig.IsAzureSQLHost answers for Azure SQL alone,
// hosts only the SQL Server engine can reach, and the engine relies on that meaning, so the
// harness keeps the managed Postgres and MySQL suffixes here. Their US Government and China
// endpoints end in .database.usgovcloudapi.net and .database.chinacloudapi.cn, which
// IsAzureSQLHost already matches.
var azureDatabaseHostSuffixes = []string{".postgres.database.azure.com", ".mysql.database.azure.com"}

// azureHostKind says what kind of Azure database host is, for the refusal, or "" when it is
// not one. host may be a comma-separated list, and each name in it may carry a :port or the
// root dot of a fully qualified name; a bare suffix, with no server name in front of it, is not
// a host.
func azureHostKind(host string) string {
	if dsconfig.IsAzureSQLHost(host) {
		return "an Azure SQL host"
	}
	for _, name := range strings.Split(host, ",") {
		name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
		if at := strings.LastIndexByte(name, ':'); at >= 0 {
			if _, err := strconv.Atoi(name[at+1:]); err == nil {
				name = strings.TrimSuffix(name[:at], ".")
			}
		}
		for _, suffix := range azureDatabaseHostSuffixes {
			if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
				return "an Azure Database for PostgreSQL or MySQL host"
			}
		}
	}
	return ""
}

// namedForTests reports whether name has test, tests or testing as one of its words (see
// nameWords), so gorgany_test, TestDb, AppTests, e2e-testing and test1 are named for tests, and
// Attestations, LatestOrders, Contests and Protest, whose "test" is inside another word, are
// not.
func namedForTests(name string) bool {
	for _, word := range nameWords(name) {
		switch strings.ToLower(word) {
		case "test", "tests", "testing":
			return true
		}
	}
	return false
}

// nameWords splits name into words: at every character that is not a letter or a digit,
// between letters and digits, between a lower-case letter and an upper-case one, and before
// the last capital of a run of them that goes on in lower case, as in APITests.
func nameWords(name string) []string {
	runes := []rune(name)
	var words []string
	start := 0
	cut := func(end int) {
		if end > start {
			words = append(words, string(runes[start:end]))
		}
		start = end
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			cut(i)
			start = i + 1
			continue
		}
		if i == start {
			continue
		}
		prev := runes[i-1]
		switch {
		case unicode.IsLetter(prev) != unicode.IsLetter(r),
			unicode.IsLower(prev) && unicode.IsUpper(r),
			unicode.IsUpper(prev) && unicode.IsUpper(r) && i+1 < len(runes) && unicode.IsLower(runes[i+1]):
			cut(i)
		}
	}
	cut(len(runes))
	return words
}

// sqlServerSystemDatabases are the databases every SQL Server instance has. Emptying one is
// never what a test means, whatever it is called.
var sqlServerSystemDatabases = []string{"master", "model", "msdb", "tempdb"}

// guardSQLServerDatabase refuses a SQL Server database whose name does not mark it as one
// for tests; see guardTarget.
func guardSQLServerDatabase(label, name string) error {
	for _, system := range sqlServerSystemDatabases {
		if strings.EqualFold(name, system) {
			return fmt.Errorf("testsupport: %s: refusing %q, a SQL Server system database; use a "+
				"database of its own for tests, such as gorgany_test", label, name)
		}
	}
	if !namedForTests(name) {
		return fmt.Errorf("testsupport: %s: refusing SQL Server database %q: on SQL Server the "+
			"harness runs only against a database with \"test\" in its name as a word of its own, "+
			"such as gorgany_test or AppTests, since it empties every table its migrations create. "+
			"Rename it, or set %s=1 if this database really is disposable", label, name, EnvAllowAnyTarget)
	}
	return nil
}

// requireRegistered refuses a database whose driver is not registered in this test binary,
// naming the import that registers it; see registrationHint.
func requireRegistered(database DatabaseConfig) error {
	if _, ok := driver.Lookup(database.Driver); ok {
		return nil
	}
	return fmt.Errorf("testsupport: %s: driver %q is not registered in this test binary "+
		"(registered: %v). %s", database.Label(), database.Driver, driver.Names(),
		registrationHint(database.Driver))
}

// registrationHint says how to register the driver name in a test binary.
//
// testsupport registers Postgres and MySQL itself, through driver/builtin, so only another
// engine can be missing. The import belongs in the test package, or in the package the tests
// already import for the app's wiring, since a registration made only in the app's main
// package is not linked into its test binaries.
func registrationHint(name string) string {
	if name == DriverSQLServer {
		return `Add _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver" to the test package, ` +
			"or to pkg/provider/bootstrap.go if the tests import it: testsupport registers only " +
			DriverPostgres + " and " + DriverMySQL + ", through driver/builtin"
	}
	return "Import the package that registers it, or call driver.Register, before the first " +
		"test asks for a database"
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
