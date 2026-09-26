package config_test

import (
	"testing"

	"github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/stretchr/testify/assert"
)

// TestAzureHostDetection. Some callers treat the answer as a safety decision — the test
// harness refuses an Azure host outright — so a host that only resembles one must not pass for
// it, and a real one spelled in capitals, with its root dot, or with the port, instance or
// protocol a connection string attaches must not slip past.
func TestAzureHostDetection(t *testing.T) {
	azure := []string{
		"example.database.windows.net",
		"EXAMPLE.Database.Windows.NET",
		"example.database.windows.net.", // fully qualified
		"example-db.database.windows.net",
		"a.b.database.windows.net",
		"example.database.usgovcloudapi.net",
		"example.database.chinacloudapi.cn",
		"example.sql.azuresynapse.net",
		"example.datawarehouse.fabric.microsoft.com",
		"example.database.fabric.microsoft.com",
		// As a connection string or a SQL client writes the host. Validate checks only that
		// a host is set, and the server key is pointed at host, so each can reach a caller.
		"tcp:example.database.windows.net",
		"tcp:example.database.windows.net,1433",
		"TCP:Example.Database.Windows.Net, 1433",
		"example.database.windows.net,1433",
		"example.database.windows.net:1433",
		"example.database.windows.net.:1433",
		`example.database.windows.net\inst`,
		`example.database.windows.net\inst,1433`,
		"example.database.windows.net/Example-db",
		"db.example.com,example.database.windows.net", // a host list with an Azure host in it
	}
	for _, host := range azure {
		assert.Truef(t, config.IsAzureSQLHost(host), "%q is an Azure SQL host", host)
	}

	notAzure := []string{
		"database.windows.net",  // the bare suffix names no server
		".database.windows.net", // nor does an empty label
		"a..database.windows.net",
		"database.windows.net.evil.com",
		"example.database.windows.net.evil.com",
		"exampledatabase.windows.net", // no label boundary
		"example.windows.net",
		"example.fabric.microsoft.com",
		"localhost",
		"127.0.0.1",
		"db.example.com",
		"",
		// Stripping the port, the instance and the protocol must not make a bare suffix, or
		// a host that only contains one, pass.
		"tcp:database.windows.net",
		"database.windows.net,1433",
		"database.windows.net:1433",
		`database.windows.net\inst`,
		"db.example.com/example.database.windows.net",
		`db.example.com\example.database.windows.net`,
		"db.example.com,1433",
	}
	for _, host := range notAzure {
		assert.Falsef(t, config.IsAzureSQLHost(host), "%q is not an Azure SQL host", host)
	}
}

// TestAzureCloudOfHost. Each cloud has its own sign-in authority and token audience, so the
// host has to select both.
func TestAzureCloudOfHost(t *testing.T) {
	tests := map[string]string{
		"example.database.windows.net":                config.AzureCloudPublic,
		"example.sql.azuresynapse.net":                config.AzureCloudPublic,
		"example.datawarehouse.fabric.microsoft.com":  config.AzureCloudPublic,
		"example.database.fabric.microsoft.com":       config.AzureCloudPublic,
		"example.database.usgovcloudapi.net":          config.AzureCloudUSGov,
		"EXAMPLE.DATABASE.USGOVCLOUDAPI.NET.":         config.AzureCloudUSGov,
		"example.database.chinacloudapi.cn":           config.AzureCloudChina,
		"localhost":                                   config.AzureCloudPublic, // not Azure: the SDK default
		"database.usgovcloudapi.net":                  config.AzureCloudPublic, // the bare suffix is no host
		"tcp:example.database.usgovcloudapi.net,1433": config.AzureCloudUSGov,
		`example.database.chinacloudapi.cn\inst`:      config.AzureCloudChina,
	}

	for host, want := range tests {
		assert.Equalf(t, want, config.AzureCloudOf(host), "AzureCloudOf(%q)", host)
	}

	assert.Equal(t, "public", config.AzureCloudPublic)
	assert.Equal(t, "usgov", config.AzureCloudUSGov)
	assert.Equal(t, "china", config.AzureCloudChina)
}

func TestSuggest(t *testing.T) {
	candidates := []string{"app_name", "encrypt", "packet_size", "dial_timeout"}

	tests := map[string]string{
		"app_nme":     "app_name",
		"APP_NAME":    "app_name",
		"encrytp":     "encrypt", // a transposition is one edit
		"packet_sise": "packet_size",
	}
	for input, want := range tests {
		got, ok := config.Suggest(input, candidates)
		assert.Truef(t, ok, "%q should get a suggestion", input)
		assert.Equalf(t, want, got, "Suggest(%q)", input)
	}

	for _, input := range []string{"columnencryption", "x", "db", ""} {
		got, ok := config.Suggest(input, candidates)
		assert.Falsef(t, ok, "%q should not suggest %q", input, got)
	}

	_, ok := config.Suggest("app_name", nil)
	assert.False(t, ok, "no candidates, no suggestion")
}

// TestSuggestReturnsTheCandidateAsSpelled: matching ignores case, but the answer is the name
// the caller recognises.
func TestSuggestReturnsTheCandidateAsSpelled(t *testing.T) {
	got, ok := config.Suggest("appname", []string{"AppName"})
	assert.True(t, ok)
	assert.Equal(t, "AppName", got)
}

// TestSuggestBreaksTiesTheSameWayWhateverTheOrder, so an error message does not depend on map
// iteration somewhere upstream.
func TestSuggestBreaksTiesTheSameWayWhateverTheOrder(t *testing.T) {
	first, ok := config.Suggest("cat1", []string{"cat3", "cat2"})
	assert.True(t, ok)
	second, _ := config.Suggest("cat1", []string{"cat2", "cat3"})

	assert.Equal(t, "cat2", first)
	assert.Equal(t, first, second)
}
