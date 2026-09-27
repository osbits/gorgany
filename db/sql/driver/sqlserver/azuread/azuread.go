// Package azuread signs SQL Server and Azure SQL datasources in with Microsoft Entra ID (formerly
// Azure Active Directory), and registers the sqlserver_gorm driver they need.
//
// Import it for its side effects, in pkg/provider/bootstrap.go next to the app's other driver
// import, in place of the plain SQL Server driver:
//
//	import _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread"
//
// It blank-imports db/sql/driver/sqlserver, so the driver is registered either way, and
// importing both is harmless.
//
// # Why it is opt-in
//
// A sign-in with Entra ID links the Azure identity SDK (azidentity, azcore), the Microsoft
// Authentication Library and a browser opener. An app that signs in with a SQL login needs none
// of that, and an app on Postgres or MySQL needs even less, so the weight is paid only by an app
// that imports this package. The split is enforced by db/sql/driver/builtin's split test: the
// SQL Server driver links no Azure SDK, and this package links neither go-mssqldb's own azuread
// package, which builds a credential per connection and so prompts per connection, nor the
// persistent token cache, which links the operating system's keychain.
//
// # Methods
//
// init registers these values of databases.<name>.auth.method (sqlserver.AuthMethod*):
//
//   - interactive: a person signs in in the system browser, once per datasource. username is
//     the account the sign-in page suggests. Development only.
//   - device_code: a person signs in on another device with a code the process prints. For a
//     machine with no browser. Development only.
//   - azure_cli: the account `az login` signed in with, and no prompt at all. Development only.
//   - azure_default: azidentity's DefaultAzureCredential chain, which covers the environment's
//     service principal, workload identity and managed identity. For deployed apps.
//   - service_principal: an app registration's client secret or certificate. For deployed apps.
//   - managed_identity: the managed identity of the Azure resource the app runs on, its default
//     one, usually system-assigned, or the user-assigned one client_id, resource_id or object_id
//     selects. For apps deployed on Azure.
//   - workload_identity: the app registration or user-assigned identity a Kubernetes service
//     account is federated with, through the token Kubernetes projects into the pod. For apps
//     deployed on AKS, or another cluster with a federated credential.
//
// The keys each method takes are checked by the SQL Server engine before any of this runs; see
// docs/SQLSERVER.md.
//
// # One sign-in, one token source, per datasource
//
// Each datasource gets one credential and one token source for its whole life. The source hands
// every connection the same token, renews it in the background from its RefreshOn and makes
// connections wait only within five minutes of its expiry, and connections that arrive together
// share one acquisition. interactive and device_code ask the person once per datasource, when it
// warms up at boot, or on its first connection with lazy_connect; after that a token that cannot
// be renewed without them is an error, never a second prompt the process opens on its own.
// The credential and the token source are the datasource's own: an app with two interactive
// datasources asks the person twice. A managed identity's tokens, though, are also cached by MSAL,
// in one cache for the whole process keyed by identity and scope, so datasources that sign in as
// the same managed identity with the same scope, which every Azure SQL server in one cloud has,
// may be handed the same token.
package azuread

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	_ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
)

// methods are the auth.method values this package registers.
var methods = []string{
	sqlserver.AuthMethodInteractive,
	sqlserver.AuthMethodDeviceCode,
	sqlserver.AuthMethodAzureCLI,
	sqlserver.AuthMethodAzureDefault,
	sqlserver.AuthMethodServicePrincipal,
	sqlserver.AuthMethodManagedIdentity,
	sqlserver.AuthMethodWorkloadIdentity,
}

func init() {
	for _, method := range methods {
		sqlserver.RegisterAuthenticator(method, authenticate)
	}
}

// credentialFor builds the credential of one datasource. It is newCredential, and a variable
// only so that tests can hand the engine a credential that signs in nowhere.
var credentialFor func(sqlserver.AuthRequest) (azcore.TokenCredential, error) = newCredential

// authenticate is the sqlserver.Authenticator of every method this package registers. The engine
// calls it once per datasource, and it must not sign in: it builds the one credential the
// datasource will use and wraps it in the token source that decides when to use it.
func authenticate(req sqlserver.AuthRequest) (sqlserver.TokenSource, error) {
	scope, err := scopeOf(req)
	if err != nil {
		return nil, err
	}
	cred, err := credentialFor(req)
	if err != nil {
		return nil, err
	}
	return newTokenSource(cred, req, scope)
}

// scopeOf is the token scope req asks for: the one the engine resolved, or, for a request built
// without one, the one sqlserver.ResolveScope derives from the host and auth.scope.
func scopeOf(req sqlserver.AuthRequest) (string, error) {
	if req.Scope != "" {
		return req.Scope, nil
	}
	return sqlserver.ResolveScope(req.Host, req.Auth.Scope)
}
