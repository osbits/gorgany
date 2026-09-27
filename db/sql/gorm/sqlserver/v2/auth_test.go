package v2

import (
	"context"
	"testing"
	"time"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The authenticator registry and how auth.method resolves. No test here signs in anywhere:
// the registered authenticators are fakes, as the Entra ID package's are to this one.

const standInSecret = "stand-in-client-secret"

// Stand-in values for the keys that select an identity. No refusal may repeat one, and each is
// distinctive enough that a refusal naming its key cannot contain it by accident.
const (
	standInClientID   = "stand-in-client-id"
	standInResourceID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example/" +
		"providers/Microsoft.ManagedIdentity/userAssignedIdentities/stand-in"
	standInObjectID  = "stand-in-object-id"
	standInTenantID  = "stand-in-tenant-id"
	standInTokenFile = "/var/run/secrets/stand-in/token"
)

// assertNoValueEchoed fails when err repeats any value a test put in an auth block.
func assertNoValueEchoed(t *testing.T, err error) {
	t.Helper()
	for _, value := range []string{standInSecret, standInClientID, standInResourceID, standInObjectID,
		standInTenantID, standInTokenFile, "/run/secrets/sp.pem", "http://localhost:8400"} {
		assert.NotContainsf(t, err.Error(), value, "the refusal repeats a value from the auth block")
	}
}

// staticTokens is a TokenSource that hands out one token.
type staticTokens string

func (s staticTokens) Token(context.Context) (string, error) { return string(s), nil }

func fakeAuthenticator(AuthRequest) (TokenSource, error) { return staticTokens("token"), nil }

// entra is a config that signs in to Azure SQL with method and auth.
func entra(method string, auth dsconfig.Auth) dsconfig.DataSource {
	auth.Method = method
	return dsconfig.DataSource{
		Driver:   "sqlserver_gorm",
		Host:     azureHost,
		Port:     1433,
		Database: "Example-db",
		Auth:     auth,
	}
}

// registerBuiltins registers a fake for every built-in token method, as importing the Entra
// ID package will.
func registerBuiltins(t *testing.T) {
	t.Helper()
	resetAuthenticators(t)
	for method := range tokenMethodRules {
		RegisterAuthenticator(method, fakeAuthenticator)
	}
}

func TestRegisterAuthenticatorRejectsEmptyNilSqlAndDuplicates(t *testing.T) {
	resetAuthenticators(t)

	assert.PanicsWithValue(t, "sqlserver: RegisterAuthenticator requires a non-empty method name", func() {
		RegisterAuthenticator("", fakeAuthenticator)
	})
	assert.Panics(t, func() { RegisterAuthenticator("vault_token", nil) })
	assert.Panics(t, func() { RegisterAuthenticator(AuthMethodSQL, fakeAuthenticator) })
	assert.Panics(t, func() { RegisterAuthenticator("Vault_Token", fakeAuthenticator) }, "auth.method arrives lowercased")
	assert.Panics(t, func() { RegisterAuthenticator(" vault_token", fakeAuthenticator) })

	RegisterAuthenticator("vault_token", fakeAuthenticator)
	assert.PanicsWithValue(t, `sqlserver: auth method "vault_token" is already registered`, func() {
		RegisterAuthenticator("vault_token", fakeAuthenticator)
	})
}

func TestRegisteredAuthMethodsIsSorted(t *testing.T) {
	resetAuthenticators(t)
	assert.Empty(t, RegisteredAuthMethods())

	RegisterAuthenticator(AuthMethodInteractive, fakeAuthenticator)
	RegisterAuthenticator(AuthMethodAzureCLI, fakeAuthenticator)
	assert.Equal(t, []string{AuthMethodAzureCLI, AuthMethodInteractive}, RegisteredAuthMethods())
}

// TestAADWithoutTheImportNamesIt: the Entra ID methods register from their own package, so a
// built-in one without an authenticator is a missing import, and the error is the line that
// fixes it and the file it goes in.
func TestAADWithoutTheImportNamesIt(t *testing.T) {
	resetAuthenticators(t)

	for method, auth := range map[string]dsconfig.Auth{
		AuthMethodInteractive:      {},
		AuthMethodDeviceCode:       {},
		AuthMethodAzureCLI:         {},
		AuthMethodAzureDefault:     {},
		AuthMethodServicePrincipal: {TenantID: "tenant", ClientID: "client", ClientSecret: standInSecret},
		AuthMethodManagedIdentity:  {},
		AuthMethodWorkloadIdentity: {},
	} {
		_, err := resolveAuth(entra(method, auth))
		require.Errorf(t, err, "method %q", method)
		assert.Contains(t, err.Error(), `add _ "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread" `+
			`in pkg/provider/bootstrap.go next to your driver import`)
		assert.Contains(t, err.Error(), `auth.method "`+method+`"`)
		assert.NotContains(t, err.Error(), standInSecret)
	}
}

// TestFieldRulesComeBeforeTheAuthenticator: a config that is wrong is told so before it is told
// the method has no authenticator.
func TestFieldRulesComeBeforeTheAuthenticator(t *testing.T) {
	resetAuthenticators(t)

	_, err := resolveAuth(entra(AuthMethodInteractive, dsconfig.Auth{ClientSecret: standInSecret}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.client_secret does not apply to auth.method interactive")
	assert.NotContains(t, err.Error(), AzureADImportPath)
}

// TestTheLoginTimeoutDefaultsByMethod: a person signing in gets five minutes, a managed identity
// two, which cover azidentity's retries of the instance metadata service, and any other method
// one; a configured login_timeout wins for each.
func TestTheLoginTimeoutDefaultsByMethod(t *testing.T) {
	registerBuiltins(t)

	for method, want := range map[string]time.Duration{
		AuthMethodInteractive:      DefaultInteractiveLoginTimeout,
		AuthMethodDeviceCode:       DefaultInteractiveLoginTimeout,
		AuthMethodAzureCLI:         DefaultLoginTimeout,
		AuthMethodAzureDefault:     DefaultLoginTimeout,
		AuthMethodManagedIdentity:  DefaultManagedIdentityLoginTimeout,
		AuthMethodWorkloadIdentity: DefaultLoginTimeout,
	} {
		plan, err := planConnection(entra(method, dsconfig.Auth{}))
		require.NoErrorf(t, err, "method %q", method)
		assert.Equalf(t, want, plan.request.LoginTimeout, "method %q", method)

		plan, err = planConnection(entra(method, dsconfig.Auth{LoginTimeout: 45 * time.Second}))
		require.NoError(t, err)
		assert.Equal(t, 45*time.Second, plan.request.LoginTimeout)
	}
	assert.Equal(t, 5*time.Minute, DefaultInteractiveLoginTimeout)
	assert.Equal(t, 2*time.Minute, DefaultManagedIdentityLoginTimeout)
	assert.Equal(t, time.Minute, DefaultLoginTimeout)
}

func TestUnknownAuthMethodListsValidOnes(t *testing.T) {
	resetAuthenticators(t)
	RegisterAuthenticator("vault_token", fakeAuthenticator)

	_, err := resolveAuth(entra("kerberoast", dsconfig.Auth{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown auth.method "kerberoast"; valid: azure_cli, azure_default, `+
		`device_code, interactive, managed_identity, service_principal, sql, vault_token, workload_identity`)
}

// TestFedauthAndLegacySpellingsGetASuggestion: go-mssqldb's fedauth values and ADO.NET's
// Authentication values are nowhere near ours by letters, so each gets its own suggestion;
// a near-miss gets the edit-distance one.
func TestFedauthAndLegacySpellingsGetASuggestion(t *testing.T) {
	resetAuthenticators(t)

	for method, want := range map[string]string{
		"activedirectoryinteractive":      AuthMethodInteractive,
		"activedirectorydevicecode":       AuthMethodDeviceCode,
		"activedirectoryazcli":            AuthMethodAzureCLI,
		"azcli":                           AuthMethodAzureCLI,
		"activedirectorydefault":          AuthMethodAzureDefault,
		"default":                         AuthMethodAzureDefault,
		"activedirectoryserviceprincipal": AuthMethodServicePrincipal,
		"activedirectoryapplication":      AuthMethodServicePrincipal,
		"activedirectorymanagedidentity":  AuthMethodManagedIdentity,
		"activedirectorymsi":              AuthMethodManagedIdentity,
		"msi":                             AuthMethodManagedIdentity,
		"activedirectoryworkloadidentity": AuthMethodWorkloadIdentity,
		"workloadidentity":                AuthMethodWorkloadIdentity,
		"sqlpassword":                     AuthMethodSQL,
		"interactiv":                      AuthMethodInteractive,
		"service_principle":               AuthMethodServicePrincipal,
		"managed_identy":                  AuthMethodManagedIdentity,
	} {
		_, err := resolveAuth(entra(method, dsconfig.Auth{}))
		require.Errorf(t, err, "method %q", method)
		assert.Containsf(t, err.Error(), `did you mean "`+want+`"?`, "method %q", method)
		assert.NotContainsf(t, err.Error(), "not available yet", "method %q", method)
	}
}

func TestRefusedMethodsSayWhy(t *testing.T) {
	resetAuthenticators(t)

	_, err := resolveAuth(entra("activedirectorypassword", dsconfig.Auth{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MFA")

	_, err = resolveAuth(entra("activedirectoryintegrated", dsconfig.Auth{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
}

// TestManagedIdentityAcceptsAtMostOneIdentity: client_id, resource_id and object_id each select
// one user-assigned identity, and none leaves it to the platform; two would leave which identity
// signs in to chance.
func TestManagedIdentityAcceptsAtMostOneIdentity(t *testing.T) {
	registerBuiltins(t)

	for name, auth := range map[string]dsconfig.Auth{
		"system-assigned":   {},
		"by client ID":      {ClientID: standInClientID},
		"by resource ID":    {ResourceID: standInResourceID},
		"by object ID":      {ObjectID: standInObjectID},
		"with scope, limit": {ClientID: standInClientID, Scope: "https://sql.example.com", LoginTimeout: time.Minute},
	} {
		resolved, err := resolveAuth(entra(AuthMethodManagedIdentity, auth))
		require.NoErrorf(t, err, "%s", name)
		assert.Equal(t, AuthMethodManagedIdentity, resolved.method)
	}

	for want, auth := range map[string]dsconfig.Auth{
		"and auth.client_id and auth.resource_id are set":                 {ClientID: standInClientID, ResourceID: standInResourceID},
		"and auth.client_id and auth.object_id are set":                   {ClientID: standInClientID, ObjectID: standInObjectID},
		"and auth.resource_id and auth.object_id are set":                 {ResourceID: standInResourceID, ObjectID: standInObjectID},
		"and auth.client_id, auth.resource_id and auth.object_id are set": {ClientID: standInClientID, ResourceID: standInResourceID, ObjectID: standInObjectID},
	} {
		_, err := resolveAuth(entra(AuthMethodManagedIdentity, auth))
		require.Errorf(t, err, "want %q", want)
		assert.Contains(t, err.Error(), "sqlserver: auth.method managed_identity takes at most one of "+
			"auth.client_id, auth.resource_id and auth.object_id, "+want)
		assert.Contains(t, err.Error(), "none selects the resource's default identity, usually its system-assigned one")
		assertNoValueEchoed(t, err)
	}
}

// TestManagedIdentityRefusesWhatItNeverSends: a managed identity has no secret, no certificate,
// no redirect and one tenant, and a key it would ignore changes what the config means without
// saying so.
func TestManagedIdentityRefusesWhatItNeverSends(t *testing.T) {
	registerBuiltins(t)

	for _, tc := range []struct {
		auth dsconfig.Auth
		want string
	}{
		{dsconfig.Auth{ClientSecret: standInSecret}, "auth.client_secret does not apply to auth.method managed_identity; " +
			"a managed identity has no secret or certificate of its own"},
		{dsconfig.Auth{CertificatePath: "/run/secrets/sp.pem"}, "auth.certificate_path does not apply to auth.method managed_identity; a managed identity has no secret"},
		{dsconfig.Auth{CertificatePassword: standInSecret}, "auth.certificate_password does not apply to auth.method managed_identity"},
		{dsconfig.Auth{SendCertificateChain: true}, "auth.send_certificate_chain does not apply to auth.method managed_identity"},
		{dsconfig.Auth{RedirectURL: "http://localhost:8400"}, "auth.redirect_url does not apply to auth.method managed_identity"},
		{dsconfig.Auth{TenantID: standInTenantID}, "auth.tenant_id does not apply to auth.method managed_identity; " +
			"a managed identity signs in to the one tenant its Azure subscription trusts"},
		{dsconfig.Auth{TokenFilePath: standInTokenFile}, "auth.token_file_path does not apply to auth.method managed_identity; " +
			"it is the Kubernetes service-account token auth.method workload_identity signs in with"},
		{dsconfig.Auth{ClientID: standInClientID, ClientSecret: standInSecret}, "auth.client_secret does not apply"},
	} {
		_, err := resolveAuth(entra(AuthMethodManagedIdentity, tc.auth))
		require.Errorf(t, err, "want %q", tc.want)
		assert.Contains(t, err.Error(), tc.want)
		assertNoValueEchoed(t, err)
	}

	cfg := entra(AuthMethodManagedIdentity, dsconfig.Auth{ClientID: standInClientID})
	cfg.Password = standInSecret
	_, err := resolveAuth(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "password does not apply to auth.method managed_identity")
	assertNoValueEchoed(t, err)
}

// TestWorkloadIdentityFieldRules: tenant_id, client_id and token_file_path are each optional,
// because the workload identity webhook puts all three in the pod's environment; anything else
// is refused.
func TestWorkloadIdentityFieldRules(t *testing.T) {
	registerBuiltins(t)

	for name, auth := range map[string]dsconfig.Auth{
		"all from the environment": {},
		"all in the config":        {TenantID: standInTenantID, ClientID: standInClientID, TokenFilePath: standInTokenFile},
		"only the client":          {ClientID: standInClientID},
		"only the tenant":          {TenantID: standInTenantID},
		"only the token file":      {TokenFilePath: standInTokenFile},
		"with scope, limit":        {Scope: "https://sql.example.com", LoginTimeout: time.Minute},
	} {
		resolved, err := resolveAuth(entra(AuthMethodWorkloadIdentity, auth))
		require.NoErrorf(t, err, "%s", name)
		assert.Equal(t, AuthMethodWorkloadIdentity, resolved.method)
	}

	for _, tc := range []struct {
		auth dsconfig.Auth
		want string
	}{
		{dsconfig.Auth{ClientID: standInClientID, ClientSecret: standInSecret}, "auth.client_secret does not apply to " +
			"auth.method workload_identity; a workload identity signs in with the Kubernetes service-account token"},
		{dsconfig.Auth{CertificatePath: "/run/secrets/sp.pem"}, "auth.certificate_path does not apply to auth.method workload_identity"},
		{dsconfig.Auth{CertificatePassword: standInSecret}, "auth.certificate_password does not apply to auth.method workload_identity"},
		{dsconfig.Auth{SendCertificateChain: true}, "auth.send_certificate_chain does not apply to auth.method workload_identity"},
		{dsconfig.Auth{RedirectURL: "http://localhost:8400"}, "auth.redirect_url does not apply to auth.method workload_identity"},
		{dsconfig.Auth{ResourceID: standInResourceID}, "auth.resource_id does not apply to auth.method workload_identity; " +
			"a workload identity is selected by auth.client_id"},
		{dsconfig.Auth{ObjectID: standInObjectID}, "auth.object_id does not apply to auth.method workload_identity; " +
			"a workload identity is selected by auth.client_id"},
	} {
		_, err := resolveAuth(entra(AuthMethodWorkloadIdentity, tc.auth))
		require.Errorf(t, err, "want %q", tc.want)
		assert.Contains(t, err.Error(), tc.want)
		assertNoValueEchoed(t, err)
	}
}

// TestIdentityKeysPointToTheirMethod: resource_id, object_id and token_file_path belong to one
// method each, and whoever sets one under another method most likely meant that one. That holds
// for sql too, and above all for an auth block that sets such a key and forgets the method.
func TestIdentityKeysPointToTheirMethod(t *testing.T) {
	registerBuiltins(t)

	for _, tc := range []struct {
		method string
		auth   dsconfig.Auth
		want   string
	}{
		{AuthMethodSQL, dsconfig.Auth{ResourceID: standInResourceID}, "sqlserver: auth.resource_id does not apply " +
			"to auth.method sql, which signs in with the top-level username and password; remove it, or choose " +
			"the method it belongs to: it selects a user-assigned managed identity, which auth.method " +
			"managed_identity signs in as"},
		{AuthMethodSQL, dsconfig.Auth{ObjectID: standInObjectID}, "auth.method managed_identity signs in as"},
		{AuthMethodSQL, dsconfig.Auth{TokenFilePath: standInTokenFile}, "auth.method workload_identity signs in with"},
		{"", dsconfig.Auth{ResourceID: standInResourceID}, "sqlserver: auth.resource_id does not apply to " +
			"auth.method sql, which an empty auth.method means, and which signs in with the top-level username " +
			"and password; remove it, or set auth.method to the method it belongs to: it selects a user-assigned " +
			"managed identity, which auth.method managed_identity signs in as; an unset ${VAR} placeholder in " +
			"the config reads as empty"},
		{"", dsconfig.Auth{ObjectID: standInObjectID}, "auth.method managed_identity signs in as"},
		{"", dsconfig.Auth{TokenFilePath: standInTokenFile}, "set auth.method to the method it belongs to: it is " +
			"the Kubernetes service-account token auth.method workload_identity signs in with"},
		{AuthMethodAzureDefault, dsconfig.Auth{ResourceID: standInResourceID}, "auth.method managed_identity signs in as"},
		{AuthMethodAzureDefault, dsconfig.Auth{ClientID: standInClientID}, "auth.method managed_identity takes it here"},
		{AuthMethodAzureDefault, dsconfig.Auth{TokenFilePath: standInTokenFile}, "auth.method workload_identity signs in with"},
		{AuthMethodInteractive, dsconfig.Auth{ObjectID: standInObjectID}, "auth.method managed_identity signs in as"},
		{AuthMethodServicePrincipal, dsconfig.Auth{TenantID: standInTenantID, ClientID: standInClientID,
			ClientSecret: standInSecret, TokenFilePath: standInTokenFile}, "auth.method workload_identity signs in with"},
	} {
		_, err := resolveAuth(entra(tc.method, tc.auth))
		require.Errorf(t, err, "%s %s", tc.method, tc.want)
		assert.Contains(t, err.Error(), tc.want)
		assertNoValueEchoed(t, err)
	}
}

func TestSQLLoginRequiresUsernameThenPassword(t *testing.T) {
	resetAuthenticators(t)

	cfg := sqlLogin()
	cfg.Username = ""
	_, err := resolveAuth(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "username is required")

	cfg = sqlLogin()
	cfg.Password = ""
	_, err = resolveAuth(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "password is required")

	cfg = sqlLogin()
	cfg.Auth.Method = " SQL "
	resolved, err := resolveAuth(cfg)
	require.NoError(t, err)
	assert.Equal(t, AuthMethodSQL, resolved.method)
	assert.True(t, resolved.sqlLogin())
}

func TestNoCredentialsIsRefused(t *testing.T) {
	cfg := sqlLogin()
	cfg.Username, cfg.Password = "", ""
	_, err := resolveAuth(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no credentials")
	assert.Contains(t, err.Error(), "unset ${VAR} placeholder")
}

func TestPasswordWithAADMethodIsRefused(t *testing.T) {
	registerBuiltins(t)

	cfg := entra(AuthMethodAzureCLI, dsconfig.Auth{})
	cfg.Password = "Gorgany-Test-1"
	_, err := resolveAuth(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "password does not apply to auth.method azure_cli")
	assert.NotContains(t, err.Error(), "Gorgany-Test-1")
}

func TestServicePrincipalFieldRules(t *testing.T) {
	registerBuiltins(t)

	valid := dsconfig.Auth{TenantID: "tenant", ClientID: "client"}
	withSecret := valid
	withSecret.ClientSecret = standInSecret
	withCert := valid
	withCert.CertificatePath = "/run/secrets/sp.pem"
	withCert.CertificatePassword = standInSecret
	withCert.SendCertificateChain = true

	for _, auth := range []dsconfig.Auth{withSecret, withCert} {
		_, err := resolveAuth(entra(AuthMethodServicePrincipal, auth))
		require.NoError(t, err)
	}

	for want, auth := range map[string]dsconfig.Auth{
		"auth.tenant_id is required":            {ClientID: "client", ClientSecret: standInSecret},
		"auth.client_id is required":            {TenantID: "tenant", ClientSecret: standInSecret},
		"needs auth.client_secret or":           valid,
		"not both":                              {TenantID: "t", ClientID: "c", ClientSecret: standInSecret, CertificatePath: "/p.pem"},
		"auth.certificate_password decrypts":    {TenantID: "t", ClientID: "c", ClientSecret: standInSecret, CertificatePassword: standInSecret},
		"auth.send_certificate_chain applies":   {TenantID: "t", ClientID: "c", ClientSecret: standInSecret, SendCertificateChain: true},
		"auth.redirect_url does not apply":      {TenantID: "t", ClientID: "c", ClientSecret: standInSecret, RedirectURL: "http://localhost"},
		"auth.resource_id does not apply":       {TenantID: "t", ClientID: "c", ClientSecret: standInSecret, ResourceID: "/subscriptions/x"},
		"auth.token_file_path does not apply":   {TenantID: "t", ClientID: "c", ClientSecret: standInSecret, TokenFilePath: "/var/run/token"},
		"auth.object_id does not apply to auth": {TenantID: "t", ClientID: "c", ClientSecret: standInSecret, ObjectID: "x"},
	} {
		_, err := resolveAuth(entra(AuthMethodServicePrincipal, auth))
		require.Errorf(t, err, "want %q", want)
		assert.Contains(t, err.Error(), want)
		assert.NotContains(t, err.Error(), standInSecret)
	}
}

// TestMethodSpecificFieldsAreRefusedElsewhere: a key a method would ignore changes what the
// config means without saying so.
func TestMethodSpecificFieldsAreRefusedElsewhere(t *testing.T) {
	registerBuiltins(t)

	for _, tc := range []struct {
		method string
		auth   dsconfig.Auth
		want   string
	}{
		{AuthMethodInteractive, dsconfig.Auth{ClientSecret: standInSecret}, "auth.client_secret does not apply to auth.method interactive; an interactive sign-in is a person's"},
		{AuthMethodInteractive, dsconfig.Auth{CertificatePath: "/p.pem"}, "auth.certificate_path does not apply"},
		{AuthMethodDeviceCode, dsconfig.Auth{RedirectURL: "http://localhost"}, "auth.redirect_url does not apply to auth.method device_code"},
		{AuthMethodAzureCLI, dsconfig.Auth{ClientID: "client"}, "`az login`"},
		{AuthMethodAzureDefault, dsconfig.Auth{ClientID: "client"}, "AZURE_CLIENT_ID"},
		{AuthMethodAzureDefault, dsconfig.Auth{ResourceID: "/subscriptions/x"}, "auth.resource_id does not apply"},
	} {
		_, err := resolveAuth(entra(tc.method, tc.auth))
		require.Errorf(t, err, "%s %+v", tc.method, tc.auth)
		assert.Contains(t, err.Error(), tc.want)
		assert.NotContains(t, err.Error(), standInSecret)
	}

	for method, auth := range map[string]dsconfig.Auth{
		AuthMethodInteractive:  {TenantID: "t", ClientID: "c", RedirectURL: "http://localhost:8400", Scope: "https://x.example.com", LoginTimeout: time.Minute},
		AuthMethodDeviceCode:   {TenantID: "t", ClientID: "c"},
		AuthMethodAzureCLI:     {TenantID: "t"},
		AuthMethodAzureDefault: {TenantID: "t"},
	} {
		_, err := resolveAuth(entra(method, auth))
		require.NoErrorf(t, err, "method %q", method)
	}

	cfg := sqlLogin()
	cfg.Auth = dsconfig.Auth{TenantID: "t"}
	_, err := resolveAuth(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.tenant_id does not apply to auth.method sql, which an empty auth.method means")
	assert.Contains(t, err.Error(), "an unset ${VAR} placeholder in the config reads as empty",
		"method: ${VAR} left unset is sql")

	cfg.Auth.Method = AuthMethodSQL
	_, err = resolveAuth(cfg)
	require.Error(t, err)
	assert.Equal(t, "sqlserver: auth.tenant_id does not apply to auth.method sql, which signs in with the "+
		"top-level username and password; remove it, or choose the method it belongs to", err.Error(),
		"a key several methods take points to none of them, and a method that is set was not left unset")
}

// TestUsernameIsAllowedButOnlyAHintForAAD: a UPN copied from DataGrip into username is the
// account an interactive sign-in suggests; it never becomes a SQL login.
func TestUsernameIsAllowedButOnlyAHintForAAD(t *testing.T) {
	registerBuiltins(t)

	cfg := entra(AuthMethodInteractive, dsconfig.Auth{})
	cfg.Username = "user@example.com"
	plan, err := planConnection(cfg)
	require.NoError(t, err)
	assert.Equal(t, "user@example.com", plan.request.Username)
	assert.NotContains(t, plan.dsn, "user")
	assert.NotContains(t, plan.dsn, "@")
}

// TestACustomRegisteredMethodSkipsBuiltInRules: a method the framework does not define is the
// registrant's to define, auth block and all.
func TestACustomRegisteredMethodSkipsBuiltInRules(t *testing.T) {
	resetAuthenticators(t)
	RegisterAuthenticator("vault_token", fakeAuthenticator)

	resolved, err := resolveAuth(entra("vault_token", dsconfig.Auth{ClientSecret: standInSecret, ResourceID: "r"}))
	require.NoError(t, err)
	assert.Equal(t, "vault_token", resolved.method)
	assert.False(t, resolved.sqlLogin())
}
