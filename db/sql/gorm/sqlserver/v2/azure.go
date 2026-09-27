package v2

import (
	"fmt"
	"net/url"
	"strings"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
)

// sqlScopes are the Entra ID token scopes of Azure SQL in each cloud. A token is only
// accepted by a server whose resource it names, so a US Government host needs the US
// Government scope, not the public one.
var sqlScopes = map[string]string{
	dsconfig.AzureCloudPublic: "https://database.windows.net/.default",
	dsconfig.AzureCloudUSGov:  "https://database.usgovcloudapi.net/.default",
	dsconfig.AzureCloudChina:  "https://database.chinacloudapi.cn/.default",
}

// sqlAudiences are the hosts of sqlScopes, each with its cloud: the Azure SQL resource a token
// is issued for, which only that cloud's authority issues tokens for.
var sqlAudiences = map[string]string{
	"database.windows.net":       dsconfig.AzureCloudPublic,
	"database.usgovcloudapi.net": dsconfig.AzureCloudUSGov,
	"database.chinacloudapi.cn":  dsconfig.AzureCloudChina,
}

// cloudNames are the clouds as an error names them.
var cloudNames = map[string]string{
	dsconfig.AzureCloudPublic: "public",
	dsconfig.AzureCloudUSGov:  "US Government",
	dsconfig.AzureCloudChina:  "China",
}

// ResolveScope returns the token scope a sign-in to host asks for.
//
// Without an override it is Azure SQL's scope in the host's cloud (see dsconfig.AzureCloudOf),
// which is the public one for a host that is not an Azure SQL endpoint at all — a SQL Server
// on a VM that accepts Entra ID tokens takes that too. An override, auth.scope, is for an
// endpoint that takes a different resource. It must be an https URL; "/.default" is appended
// when it is not already there, since that is how a client-credential or user sign-in asks
// for the resource's configured permissions.
func ResolveScope(host, override string) (string, error) {
	override = strings.TrimSpace(override)
	if override == "" {
		return sqlScopes[dsconfig.AzureCloudOf(host)], nil
	}

	parsed, err := url.Parse(override)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("sqlserver: auth.scope must be an https URL such as %s, got %q",
			sqlScopes[dsconfig.AzureCloudPublic], override)
	}
	if strings.HasSuffix(override, "/.default") {
		return override, nil
	}
	return strings.TrimRight(override, "/") + "/.default", nil
}

// ResolveCloud returns the Azure cloud whose authority a sign-in to host for scope, as
// ResolveScope returned it, goes to.
//
// The authority and the token's audience must come from the same cloud: a token for the US
// Government's Azure SQL is issued only by the US Government's authority, and a server in one
// cloud refuses a token issued in another. Without an auth.scope both follow the host, and so
// they agree. With one that names another cloud's Azure SQL audience, two cases differ:
//
//   - An Azure SQL host names its cloud, so the scope contradicts it, and it is refused here,
//     where the config is, rather than at sign-in as an AADSTS error that names neither.
//   - Any other host, a private endpoint behind a DNS name of its own or an address, names no
//     cloud at all, and would otherwise get the public one; the scope is then the only word on
//     the cloud, so it decides.
//
// A scope that is no Azure SQL audience, the resource of some other endpoint, leaves the host's
// cloud.
func ResolveCloud(host, scope string) (string, error) {
	hostCloud := dsconfig.AzureCloudOf(host)
	scopeCloud, isAudience := audienceCloud(scope)
	switch {
	case !isAudience || scopeCloud == hostCloud:
		return hostCloud, nil
	case dsconfig.IsAzureSQLHost(host):
		return "", fmt.Errorf("sqlserver: auth.scope %q is Azure SQL in the %s cloud, and %s is an Azure SQL "+
			"host in the %s cloud, which refuses a token issued for another cloud; remove auth.scope, "+
			"whose default follows the host", scope, cloudNames[scopeCloud], host, cloudNames[hostCloud])
	default:
		return scopeCloud, nil
	}
}

// audienceCloud reports the cloud whose Azure SQL audience scope names, if it names one.
func audienceCloud(scope string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(scope))
	if err != nil {
		return "", false
	}
	cloud, ok := sqlAudiences[strings.TrimRight(strings.ToLower(parsed.Hostname()), ".")]
	return cloud, ok
}
