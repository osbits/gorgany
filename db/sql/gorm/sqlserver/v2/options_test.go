package v2

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/microsoft/go-mssqldb/msdsn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// optionsDSN is what go-mssqldb reads from sqlLogin with options.
func optionsDSN(t *testing.T, options map[string]string) msdsn.Config {
	t.Helper()
	cfg := sqlLogin()
	cfg.Options = options
	return parsed(t, cfg)
}

// optionsError is the error sqlLogin with options is refused with.
func optionsError(t *testing.T, options map[string]string) error {
	t.Helper()
	cfg := sqlLogin()
	cfg.Options = options
	_, err := BuildDSN(cfg)
	require.Error(t, err)
	return err
}

// TestSnakeCaseOptionAliasesMapToDriverKeys: go-mssqldb's keys have spaces in them, which few
// people write in YAML; each is accepted in snake_case, and as go-mssqldb spells it.
func TestSnakeCaseOptionAliasesMapToDriverKeys(t *testing.T) {
	for _, spelling := range []string{"app_name", "app name", "application_name", "Application Name"} {
		assert.Equal(t, "my-app", optionsDSN(t, map[string]string{spelling: "my-app"}).AppName, spelling)
	}
	for _, spelling := range []string{"dial_timeout", "dial timeout"} {
		assert.Equal(t, 7*time.Second, optionsDSN(t, map[string]string{spelling: "7"}).DialTimeout, spelling)
	}
	for _, spelling := range []string{"connection_timeout", "connect_timeout", "connection timeout"} {
		assert.Equal(t, 9*time.Second, optionsDSN(t, map[string]string{spelling: "9"}).ConnTimeout, spelling)
	}
	for _, spelling := range []string{"keep_alive", "keepalive"} {
		assert.Equal(t, 11*time.Second, optionsDSN(t, map[string]string{spelling: "11"}).KeepAlive, spelling)
	}
	for _, spelling := range []string{"packet_size", "packet size"} {
		assert.Equal(t, uint16(8192), optionsDSN(t, map[string]string{spelling: "8192"}).PacketSize, spelling)
	}
	for _, spelling := range []string{"workstation_id", "workstation id", "wsid"} {
		assert.Equal(t, "ws1", optionsDSN(t, map[string]string{spelling: "ws1"}).Workstation, spelling)
	}
	for _, spelling := range []string{"trust_server_certificate", "trustservercertificate", "trust server certificate"} {
		assert.True(t, optionsDSN(t, map[string]string{spelling: "true"}).TrustServerCertificate, spelling)
	}
	for _, spelling := range []string{"hostname_in_certificate", "hostnameincertificate", "host name in certificate"} {
		assert.True(t, optionsDSN(t, map[string]string{spelling: "cert.example.com"}).HostInCertificateProvided, spelling)
	}
	for _, spelling := range []string{"server_spn", "serverspn"} {
		assert.Equal(t, "MSSQLSvc/sql.example.com", optionsDSN(t, map[string]string{spelling: "MSSQLSvc/sql.example.com"}).ServerSPN)
	}
	for _, spelling := range []string{"failover_partner", "failoverpartner"} {
		assert.Equal(t, "mirror.example.com", optionsDSN(t, map[string]string{spelling: "mirror.example.com"}).FailOverPartner)
	}
	assert.Equal(t, uint64(1434), optionsDSN(t, map[string]string{"failover_port": "1434"}).FailOverPort)
	assert.False(t, optionsDSN(t, map[string]string{"multi_subnet_failover": "no"}).MultiSubnetFailover)
	assert.True(t, optionsDSN(t, map[string]string{"disable_retry": "yes"}).DisableRetry)
	assert.True(t, optionsDSN(t, map[string]string{"no_trace_id": "1"}).NoTraceID)
	assert.True(t, optionsDSN(t, map[string]string{"epa_enabled": "true"}).EpaEnabled)
	assert.Equal(t, "Europe/Zurich", optionsDSN(t, map[string]string{"timezone": "Europe/Zurich"}).Encoding.Timezone.String())
	assert.Equal(t, msdsn.Log(3), optionsDSN(t, map[string]string{"driver_log": "3"}).LogFlags)
	assert.Equal(t, uint16(tls.VersionTLS12), optionsDSN(t, map[string]string{"tls_min": "1.2"}).TLSConfig.MinVersion)
}

// TestOptionsOverrideFrameworkDefaults: app name and guid conversion are defaults, not rules.
func TestOptionsOverrideFrameworkDefaults(t *testing.T) {
	p := optionsDSN(t, map[string]string{"app_name": "my-app", "guid_conversion": "false"})
	assert.Equal(t, "my-app", p.AppName)
	assert.False(t, p.Encoding.GuidConversion)
}

// TestTheSameOptionTwiceIsRefused: two spellings of one option leave no rule for which wins.
func TestTheSameOptionTwiceIsRefused(t *testing.T) {
	err := optionsError(t, map[string]string{"app_name": "a", "app name": "b"})
	assert.Contains(t, err.Error(), "options.app name and options.app_name both set app_name")
}

// TestOptionsOwnedByTypedConfigAreRefused: a setting with a typed key has one home.
func TestOptionsOwnedByTypedConfigAreRefused(t *testing.T) {
	for key, owner := range map[string]string{
		"database":            "db",
		"Initial Catalog":     "db",
		"user id":             "username",
		"uid":                 "username",
		"password":            "password",
		"pwd":                 "password",
		"server":              "host",
		"Data Source":         "host",
		"port":                "port",
		"encrypt":             "ssl",
		"fedauth":             "auth",
		"applicationclientid": "auth",
		"clientcertpath":      "auth",
	} {
		err := optionsError(t, map[string]string{key: "x"})
		assert.Containsf(t, err.Error(), "options."+key+" is set with the typed key "+owner, "option %q", key)
		assert.NotContains(t, err.Error(), `"x"`, "the value is never repeated")
	}
}

func TestUnsupportedOptionsAreRefused(t *testing.T) {
	for _, key := range []string{"change password", "columnencryption", "column encryption setting"} {
		err := optionsError(t, map[string]string{key: "x"})
		requireUnsupported(t, err, "options."+key)
	}
}

func TestUnknownOptionIsRefusedWithASuggestion(t *testing.T) {
	err := optionsError(t, map[string]string{"app_nmae": "x"})
	assert.Contains(t, err.Error(), "unknown option(s) 'app_nmae'")
	assert.Contains(t, err.Error(), "did you mean 'app_name' instead of 'app_nmae'?")
	assert.Contains(t, err.Error(), "recognised options are app_name, application_intent, certificate")

	err = optionsError(t, map[string]string{"frobnicate": "x", "sslmode": "y"})
	assert.Contains(t, err.Error(), "'frobnicate', 'sslmode'")
	assert.NotContains(t, err.Error(), "did you mean")
}

func TestOptionValuesAreValidated(t *testing.T) {
	for key, value := range map[string]string{
		"dial_timeout":             "10s",
		"connection_timeout":       "-1",
		"packet_size":              "70000",
		"failover_port":            "sixty",
		"trust_server_certificate": "maybe",
		"tls_min":                  "1.4",
		"protocol":                 "udp",
		"timezone":                 "Mars/Olympus_Mons",
		"application_intent":       "Sometimes",
		"log":                      "all",
		"app_name":                 "   ",
	} {
		err := optionsError(t, map[string]string{key: value})
		assert.Containsf(t, err.Error(), "options."+key, "option %q", key)
		if value != "   " {
			assert.NotContainsf(t, err.Error(), value, "option %q: the value is never repeated", key)
		}
	}

	err := optionsError(t, map[string]string{"app_name": ""})
	assert.Contains(t, err.Error(), "unset ${VAR} placeholder")
}

// TestOptionValuesCannotSmuggleParameters: a value with & or = in it stays one value.
func TestOptionValuesCannotSmuggleParameters(t *testing.T) {
	cfg := sqlLogin()
	cfg.Host = azureHost
	cfg.Options = map[string]string{"app_name": "my-app&encrypt=disable&trustservercertificate=true"}

	p := parsed(t, cfg)
	assert.Equal(t, "my-app&encrypt=disable&trustservercertificate=true", p.AppName)
	assert.Equal(t, msdsn.Encryption(msdsn.EncryptionRequired), p.Encryption)
	assert.False(t, p.TrustServerCertificate)
}

// TestHandBuiltMixedCaseOptionKeysAreFolded: Viper lowercases what it reads; a map built by
// hand means the same thing.
func TestHandBuiltMixedCaseOptionKeysAreFolded(t *testing.T) {
	p := optionsDSN(t, map[string]string{"App_Name": "my-app", "  Dial   Timeout ": "5", "TrustServerCertificate": "YES"})
	assert.Equal(t, "my-app", p.AppName)
	assert.Equal(t, 5*time.Second, p.DialTimeout)
	assert.True(t, p.TrustServerCertificate)
}
