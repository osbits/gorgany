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
