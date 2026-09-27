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
