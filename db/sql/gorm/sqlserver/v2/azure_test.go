package v2

import (
	"testing"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScopeIsDerivedFromTheHost: a token is accepted only by a server whose resource it
// names, so the scope follows the host's cloud.
func TestScopeIsDerivedFromTheHost(t *testing.T) {
	for host, want := range map[string]string{
		azureHost:                             "https://database.windows.net/.default",
		"example.database.usgovcloudapi.net":  "https://database.usgovcloudapi.net/.default",
		"example.database.chinacloudapi.cn":   "https://database.chinacloudapi.cn/.default",
		"example.sql.azuresynapse.net":        "https://database.windows.net/.default",
		"sql.example.com":                     "https://database.windows.net/.default",
		"EXAMPLE.DATABASE.USGOVCLOUDAPI.NET.": "https://database.usgovcloudapi.net/.default",
	} {
		scope, err := ResolveScope(host, "")
		require.NoError(t, err)
		assert.Equalf(t, want, scope, "host %q", host)
	}
}

func TestScopeOverrideIsNormalised(t *testing.T) {
	for override, want := range map[string]string{
		"https://api.example.com":           "https://api.example.com/.default",
		"https://api.example.com/":          "https://api.example.com/.default",
		" https://api.example.com/.default": "https://api.example.com/.default",
	} {
		scope, err := ResolveScope(azureHost, override)
		require.NoError(t, err)
		assert.Equal(t, want, scope)
	}
}

func TestNonHttpsScopeIsRefused(t *testing.T) {
	for _, override := range []string{"http://api.example.com", "api.example.com", "https://", "::"} {
		_, err := ResolveScope(azureHost, override)
		require.Errorf(t, err, "scope %q", override)
		assert.Contains(t, err.Error(), "auth.scope must be an https URL")
	}
}

// TestCloudIsDerivedFromTheHost: the authenticator is told the host's cloud, whose sign-in
// authority it uses, alongside the scope.
func TestCloudIsDerivedFromTheHost(t *testing.T) {
	registerBuiltins(t)

	for host, want := range map[string]string{
		azureHost:                            dsconfig.AzureCloudPublic,
		"example.database.usgovcloudapi.net": dsconfig.AzureCloudUSGov,
		"example.database.chinacloudapi.cn":  dsconfig.AzureCloudChina,
	} {
		cfg := entra(AuthMethodAzureCLI, dsconfig.Auth{})
		cfg.Host = host
		plan, err := planConnection(cfg)
		require.NoError(t, err)
		assert.Equal(t, want, plan.request.Cloud)
		assert.Equal(t, host, plan.request.Host)
		assert.Equal(t, sqlScopes[want], plan.request.Scope)
	}
}

// TestAScopeFromAnotherCloudIsRefusedForAnAzureHost: the authority follows an Azure SQL host,
// so a scope naming another cloud's Azure SQL could only fail at sign-in, with an AADSTS error
// that names neither.
func TestAScopeFromAnotherCloudIsRefusedForAnAzureHost(t *testing.T) {
	registerBuiltins(t)

	for host, scope := range map[string]string{
		azureHost:                            "https://database.usgovcloudapi.net/",
		"example.database.usgovcloudapi.net": "https://database.windows.net",
		"example.database.chinacloudapi.cn":  "https://DATABASE.USGOVCLOUDAPI.NET./.default",
		"example.sql.azuresynapse.net":       "https://database.chinacloudapi.cn",
	} {
		cfg := entra(AuthMethodAzureCLI, dsconfig.Auth{Scope: scope})
		cfg.Host = host
		_, err := planConnection(cfg)
		require.Errorf(t, err, "host %s, scope %s", host, scope)
		assert.Contains(t, err.Error(), "is an Azure SQL host in the")
		assert.Contains(t, err.Error(), "remove auth.scope, whose default follows the host")
	}

	_, err := ResolveCloud(azureHost, "https://database.usgovcloudapi.net/.default")
	require.Error(t, err)
	assert.Equal(t, `sqlserver: auth.scope "https://database.usgovcloudapi.net/.default" is Azure SQL in the `+
		`US Government cloud, and `+azureHost+` is an Azure SQL host in the public cloud, which refuses a `+
		`token issued for another cloud; remove auth.scope, whose default follows the host`, err.Error())
}

// TestAScopeChoosesTheCloudOfAHostThatNamesNone: a private endpoint behind a DNS name of its own,
// or an address, says nothing about its cloud, so a scope naming a cloud's Azure SQL decides the
// authority too, and the two cannot disagree.
func TestAScopeChoosesTheCloudOfAHostThatNamesNone(t *testing.T) {
	registerBuiltins(t)

	for scope, want := range map[string]string{
		"https://database.usgovcloudapi.net": dsconfig.AzureCloudUSGov,
		"https://database.chinacloudapi.cn/": dsconfig.AzureCloudChina,
		"https://database.windows.net":       dsconfig.AzureCloudPublic,
		"https://sql.example.com":            dsconfig.AzureCloudPublic,
		"":                                   dsconfig.AzureCloudPublic,
	} {
		for _, host := range []string{"sql.example.com", "10.0.0.4"} {
			cfg := entra(AuthMethodAzureCLI, dsconfig.Auth{Scope: scope})
			cfg.Host = host
			plan, err := planConnection(cfg)
			require.NoErrorf(t, err, "host %s, scope %q", host, scope)
			assert.Equalf(t, want, plan.request.Cloud, "host %s, scope %q", host, scope)
		}
	}

	// A scope that names the host's own cloud, or no Azure SQL at all, is the host's.
	for host, scope := range map[string]string{
		"example.database.usgovcloudapi.net": "https://database.usgovcloudapi.net",
		azureHost:                            "https://sql.example.com",
	} {
		cloud, err := ResolveCloud(host, scope)
		require.NoError(t, err)
		assert.Equal(t, dsconfig.AzureCloudOf(host), cloud)
	}
}

// TestSQLAudiencesAreTheScopesHosts: the audiences ResolveCloud reads a scope by are the ones
// ResolveScope derives, so the two tables cannot drift apart.
func TestSQLAudiencesAreTheScopesHosts(t *testing.T) {
	require.Len(t, sqlAudiences, len(sqlScopes))
	for cloud, scope := range sqlScopes {
		got, ok := audienceCloud(scope)
		assert.Truef(t, ok, "scope %s", scope)
		assert.Equalf(t, cloud, got, "scope %s", scope)
		assert.NotEmptyf(t, cloudNames[cloud], "cloud %s", cloud)
	}
}

// TestTheRequestNamesTheTargetAsTheEngineDoes: an authenticator's errors name the database from
// the request, and must name it as the engine's own errors around them do.
func TestTheRequestNamesTheTargetAsTheEngineDoes(t *testing.T) {
	registerBuiltins(t)

	plan, err := planConnection(entra(AuthMethodAzureCLI, dsconfig.Auth{}))
	require.NoError(t, err)
	assert.Equal(t, azureHost+":1433/Example-db", plan.request.Target)
	assert.Equal(t, plan.target, plan.request.Target)
}
