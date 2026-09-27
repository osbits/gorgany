package v2

import (
	"strings"
	"testing"

	"github.com/microsoft/go-mssqldb/msdsn"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The DSN is pinned two ways: as text, where the text is the contract, and through
// go-mssqldb's own parser, which is what decides what a DSN means.

const azureHost = "example.database.windows.net"

// sqlLogin is a config that signs in with a SQL login to a server on premises.
func sqlLogin() dsconfig.DataSource {
	return dsconfig.DataSource{
		Driver:   "sqlserver_gorm",
		Host:     "sql.example.com",
		Database: "Example-db",
		Username: "sa",
		Password: "Gorgany-Test-1",
	}
}

// parsed builds cfg's DSN and parses it as go-mssqldb does.
func parsed(t *testing.T, cfg dsconfig.DataSource) msdsn.Config {
	t.Helper()
	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	p, err := msdsn.Parse(dsn)
	require.NoError(t, err, "go-mssqldb must parse what BuildDSN builds")
	return p
}

func TestBuildDSNDefaults(t *testing.T) {
	dsn, err := BuildDSN(sqlLogin())
	require.NoError(t, err)
	assert.Equal(t, "sqlserver://sa:Gorgany-Test-1@sql.example.com:1433"+
		"?app+name=gorgany&database=Example-db&encrypt=true&guid+conversion=true&trustservercertificate=false", dsn)
}

// TestBuildDSNEscapesUserinfo: a UPN login has an "@", and a password may have anything.
func TestBuildDSNEscapesUserinfo(t *testing.T) {
	cfg := sqlLogin()
	cfg.Username = "user@example.com"
	cfg.Password = "p@ss:w/rd?#%&= ;"

	p := parsed(t, cfg)
	assert.Equal(t, "user@example.com", p.User)
	assert.Equal(t, "p@ss:w/rd?#%&= ;", p.Password)
	assert.Equal(t, "sql.example.com", p.Host)
	assert.Equal(t, "Example-db", p.Database)
}

// TestBuildDSNRoundTripsThroughTheDriverParser: what the engine means by each default is what
// go-mssqldb reads.
func TestBuildDSNRoundTripsThroughTheDriverParser(t *testing.T) {
	p := parsed(t, sqlLogin())

	assert.Equal(t, "sql.example.com", p.Host)
	assert.Equal(t, uint64(1433), p.Port)
	assert.Empty(t, p.Instance)
	assert.Equal(t, "Example-db", p.Database)
	assert.Equal(t, "sa", p.User)
	assert.Equal(t, "Gorgany-Test-1", p.Password)
	assert.Equal(t, msdsn.Encryption(msdsn.EncryptionRequired), p.Encryption)
	assert.False(t, p.TrustServerCertificate, "go-mssqldb trusts any certificate unless told otherwise")
	require.NotNil(t, p.TLSConfig)
	assert.False(t, p.TLSConfig.InsecureSkipVerify)
	assert.Equal(t, DefaultAppName, p.AppName)
	assert.True(t, p.Encoding.GuidConversion, "a uniqueidentifier must scan into uuid.UUID in its shown order")
	assert.False(t, p.ReadOnlyIntent)
}

func TestBuildDSNDefaultsPortTo1433(t *testing.T) {
	assert.Equal(t, uint64(DefaultPort), parsed(t, sqlLogin()).Port)

	cfg := sqlLogin()
	cfg.Port = 14330
	assert.Equal(t, uint64(14330), parsed(t, cfg).Port)
}

// TestBuildDSNOmitsThePortForANamedInstanceWithoutOne: on premises, an instance with no port
// is found through SQL Server Browser, which go-mssqldb asks only when no port is set.
func TestBuildDSNOmitsThePortForANamedInstanceWithoutOne(t *testing.T) {
	cfg := sqlLogin()
	cfg.Instance = "SQLEXPRESS"

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(dsn, "sqlserver://sa:Gorgany-Test-1@sql.example.com/SQLEXPRESS?"), dsn)

	p := parsed(t, cfg)
	assert.Equal(t, "sql.example.com", p.Host)
	assert.Equal(t, "SQLEXPRESS", p.Instance)
	assert.Zero(t, p.Port)
	assert.Equal(t, `sql.example.com\SQLEXPRESS/Example-db`, describe(cfg))
}

// TestASetPortWinsOverTheInstance: JDBC's rule, and so DataGrip's: with both set, the port is
// dialled and the instance ignored — here with a warning, since one key is dead.
func TestASetPortWinsOverTheInstance(t *testing.T) {
	cfg := sqlLogin()
	cfg.Instance = "SQLEXPRESS"
	cfg.Port = 1433

	dsn, _, warnings, err := buildDSN(cfg, true)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(dsn, "sqlserver://sa:Gorgany-Test-1@sql.example.com:1433?"), dsn)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], `instance "SQLEXPRESS" is ignored because port 1433 is set`)

	p := parsed(t, cfg)
	assert.Empty(t, p.Instance, "the instance must not reach go-mssqldb, which would send it in the login")
	assert.Equal(t, "sql.example.com:1433/Example-db", describe(cfg))
}

// TestAzureHostDropsTheInstance: Azure SQL has no named instances and answers SQL Server
// Browser on no port, so a JDBC-style instance copied from DataGrip is dropped, with a
// warning, whether or not a port is set.
func TestAzureHostDropsTheInstance(t *testing.T) {
	for _, port := range []int{0, 1433} {
		cfg := sqlLogin()
		cfg.Host = azureHost
		cfg.Instance = "legacy"
		cfg.Port = port

		dsn, e, warnings, err := buildDSN(cfg, true)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(dsn, "sqlserver://sa:Gorgany-Test-1@"+azureHost+":1433?"), dsn)
		assert.Equal(t, azureHost+":1433", e.String())
		require.Len(t, warnings, 1)
		assert.Contains(t, warnings[0], "Azure SQL has no named instances")
		assert.Empty(t, parsed(t, cfg).Instance)
	}
}

func TestAzureHostRefusesDisabledEncryption(t *testing.T) {
	for _, ssl := range []string{"false", "optional", "no", "0", "disable", "Disable"} {
		cfg := sqlLogin()
		cfg.Host = azureHost
		cfg.SSL = ssl
		_, err := BuildDSN(cfg)
		require.Errorf(t, err, "ssl %q", ssl)
		assert.Contains(t, err.Error(), "Azure")
	}
	for _, ssl := range []string{"", "true", "strict"} {
		cfg := sqlLogin()
		cfg.Host = azureHost
		cfg.SSL = ssl
		_, err := BuildDSN(cfg)
		require.NoErrorf(t, err, "ssl %q", ssl)
	}
}

func TestAzureHostRefusesTrustServerCertificate(t *testing.T) {
	cfg := sqlLogin()
	cfg.Host = azureHost
	cfg.Options = map[string]string{"trust_server_certificate": "true"}
	_, err := BuildDSN(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Azure SQL host")

	cfg.Options = map[string]string{"trust_server_certificate": "false"}
	_, err = BuildDSN(cfg)
	require.NoError(t, err, "saying false says the default")
}

func TestTrustServerCertificateWithStrictIsRefused(t *testing.T) {
	cfg := sqlLogin()
	cfg.SSL = "strict"
	cfg.Options = map[string]string{"trust_server_certificate": "yes"}
	_, err := BuildDSN(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strict")
}

// TestTrustServerCertificateOnPremises: a local SQL Server in a container presents a
// self-signed certificate, which is what the option is for.
func TestTrustServerCertificateOnPremises(t *testing.T) {
	cfg := sqlLogin()
	cfg.Options = map[string]string{"trust_server_certificate": "true"}
	p := parsed(t, cfg)
	assert.True(t, p.TrustServerCertificate)
	assert.Equal(t, msdsn.Encryption(msdsn.EncryptionRequired), p.Encryption)
}

func TestSSLValuesMapToEncrypt(t *testing.T) {
	for ssl, want := range map[string]msdsn.Encryption{
		"":          msdsn.EncryptionRequired,
		"true":      msdsn.EncryptionRequired,
		"mandatory": msdsn.EncryptionRequired,
		"yes":       msdsn.EncryptionRequired,
		"1":         msdsn.EncryptionRequired,
		" TRUE ":    msdsn.EncryptionRequired,
		"strict":    msdsn.EncryptionStrict,
		"false":     msdsn.EncryptionOff,
		"optional":  msdsn.EncryptionOff,
		"no":        msdsn.EncryptionOff,
		"0":         msdsn.EncryptionOff,
		"disable":   msdsn.EncryptionDisabled,
	} {
		cfg := sqlLogin()
		cfg.SSL = ssl
		assert.Equalf(t, want, parsed(t, cfg).Encryption, "ssl %q", ssl)
	}
}

// TestUnknownSSLValueIsRefused: Postgres's sslmode values mean nothing to go-mssqldb, which
// would refuse them at connect time — or, for some, read them as something else.
func TestUnknownSSLValueIsRefused(t *testing.T) {
	for _, ssl := range []string{"require", "verify-full", "skip-verify", "on"} {
		cfg := sqlLogin()
		cfg.SSL = ssl
		_, err := BuildDSN(cfg)
		require.Errorf(t, err, "ssl %q", ssl)
		assert.Contains(t, err.Error(), "strict, false or disable")
	}
}

func TestSearchPathIsRefused(t *testing.T) {
	cfg := sqlLogin()
	cfg.SearchPath = "sales"
	_, err := BuildDSN(cfg)
	requireUnsupported(t, err, "search_path")
	assert.Contains(t, err.Error(), `"sales.Orders"`)
}

// TestHostWithPortOrInstanceIsRefused: connection strings write a port, an instance or a
// protocol into the server name; the typed config has a key for each.
func TestHostWithPortOrInstanceIsRefused(t *testing.T) {
	for host, want := range map[string]string{
		"sql.example.com:1433":       "write the port in port",
		"sql.example.com,1433":       "write the port in port",
		`sql.example.com\SQLEXPRESS`: "write a named instance in instance",
		"tcp:" + azureHost:           "tcp:",
		"TCP:sql.example.com":        "tcp:",
		"sql.example.com/x":          "host name or an IPv4 address alone",
		"user@sql.example.com":       "host name or an IPv4 address alone",
		"sql example":                "host name or an IPv4 address alone",
		"[::1]":                      "write the port in port",
	} {
		cfg := sqlLogin()
		cfg.Host = host
		_, err := BuildDSN(cfg)
		require.Errorf(t, err, "host %q", host)
		assert.Containsf(t, err.Error(), want, "host %q", host)
	}
}

func TestInstanceNameIsValidated(t *testing.T) {
	for _, bad := range []string{"bad name", "abcdefghijklmnopq", "a;b", "a/b", `a\b`} {
		cfg := sqlLogin()
		cfg.Instance = bad
		_, err := BuildDSN(cfg)
		require.Errorf(t, err, "instance %q", bad)
		assert.Contains(t, err.Error(), "not an instance name")
	}

	cfg := sqlLogin()
	cfg.Instance = "SQL$1#-_"
	assert.Equal(t, "SQL$1#-_", parsed(t, cfg).Instance, "every character an instance name may hold survives the URL")
}

func TestReadOnlySetsApplicationIntentReadOnly(t *testing.T) {
	cfg := sqlLogin()
	cfg.ReadOnly = true
	assert.True(t, parsed(t, cfg).ReadOnlyIntent)

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Contains(t, dsn, "applicationintent=ReadOnly")
}

// TestApplicationIntentReadWriteOverridesReadOnly: read_only guards the connection; the intent
// only routes it. Asking for the primary while still refusing writes is a coherent choice.
func TestApplicationIntentReadWriteOverridesReadOnly(t *testing.T) {
	cfg := sqlLogin()
	cfg.ReadOnly = true
	cfg.Options = map[string]string{"application_intent": "ReadWrite"}
	assert.False(t, parsed(t, cfg).ReadOnlyIntent)
}

// TestApplicationIntentReadOnlyWithoutReadOnlyIsRefused: it would route writes to a node that
// refuses every one of them.
func TestApplicationIntentReadOnlyWithoutReadOnlyIsRefused(t *testing.T) {
	cfg := sqlLogin()
	cfg.Options = map[string]string{"application_intent": "ReadOnly"}
	_, err := BuildDSN(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read_only: true")
}

// TestApplicationIntentIsSentInTheDriversCasing: go-mssqldb compares the value
// case-sensitively and reads anything but "ReadOnly" as read-write.
func TestApplicationIntentIsSentInTheDriversCasing(t *testing.T) {
	cfg := sqlLogin()
	cfg.ReadOnly = true
	cfg.Options = map[string]string{"applicationintent": "readonly"}
	assert.True(t, parsed(t, cfg).ReadOnlyIntent)
}

// TestAADMethodsOmitUserinfo: a token method signs in with the token; its username is at most
// a hint to the identity provider and must not become a SQL login attempt.
func TestAADMethodsOmitUserinfo(t *testing.T) {
	cfg := sqlLogin()
	cfg.Host = azureHost
	cfg.Username = "user@example.com"
	cfg.Password = ""
	cfg.Auth = dsconfig.Auth{Method: AuthMethodInteractive}

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(dsn, "sqlserver://"+azureHost+":1433?"), dsn)
	p := parsed(t, cfg)
	assert.Empty(t, p.User)
	assert.Empty(t, p.Password)
}

// TestDescribeNamesTheServerOnly: describe is what errors say instead of the DSN.
func TestDescribeNamesTheServerOnly(t *testing.T) {
	cfg := sqlLogin()
	assert.Equal(t, "sql.example.com:1433/Example-db", describe(cfg))
	assert.NotContains(t, describe(cfg), cfg.Password)

	cfg.Host = azureHost
	cfg.Instance = "legacy"
	assert.Equal(t, azureHost+":1433/Example-db", describe(cfg), "it names where the connection goes")
}

func TestBuildDSNIsDeterministic(t *testing.T) {
	cfg := sqlLogin()
	cfg.Options = map[string]string{"dial_timeout": "5", "app_name": "my-app", "keep_alive": "10", "packet_size": "8192"}
	first, err := BuildDSN(cfg)
	require.NoError(t, err)
	for range 20 {
		again, err := BuildDSN(cfg)
		require.NoError(t, err)
		assert.Equal(t, first, again)
	}
}

func TestBuildDSNIsNotAnUnsupportedErrorForAConfigMistake(t *testing.T) {
	cfg := sqlLogin()
	cfg.SSL = "require"
	_, err := BuildDSN(cfg)
	require.Error(t, err)
	assert.False(t, dbCore.IsUnsupported(err), "a value that is merely wrong is not a construct SQL Server lacks")
}
