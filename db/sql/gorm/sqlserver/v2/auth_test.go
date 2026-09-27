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
// the registered authenticators are fakes, as the Entra ID package's will be to this one.

const standInSecret = "stand-in-client-secret"

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

// TestAADWithoutAnAuthenticatorSaysItIsNotShipped: no package registers the Entra ID methods
// in this release, so a built-in one without an authenticator says so, and what to do instead.
// It names no import: one that does not exist would only fail the build.
func TestAADWithoutAnAuthenticatorSaysItIsNotShipped(t *testing.T) {
	resetAuthenticators(t)

	for method, auth := range map[string]dsconfig.Auth{
		AuthMethodInteractive:      {},
		AuthMethodDeviceCode:       {},
		AuthMethodAzureCLI:         {},
		AuthMethodAzureDefault:     {},
		AuthMethodServicePrincipal: {TenantID: "tenant", ClientID: "client", ClientSecret: standInSecret},
	} {
		_, err := resolveAuth(entra(method, auth))
		require.Errorf(t, err, "method %q", method)
		assert.Contains(t, err.Error(), "does not ship")
		assert.Contains(t, err.Error(), "sqlserver.RegisterAuthenticator")
		assert.NotContains(t, err.Error(), "azuread")
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
	assert.NotContains(t, err.Error(), "does not ship")
}

func TestUnknownAuthMethodListsValidOnes(t *testing.T) {
	resetAuthenticators(t)
	RegisterAuthenticator("vault_token", fakeAuthenticator)

	_, err := resolveAuth(entra("kerberoast", dsconfig.Auth{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown auth.method "kerberoast"; valid: azure_cli, azure_default, `+
		`device_code, interactive, service_principal, sql, vault_token`)
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
		"sqlpassword":                     AuthMethodSQL,
		"interactiv":                      AuthMethodInteractive,
		"service_principle":               AuthMethodServicePrincipal,
	} {
		_, err := resolveAuth(entra(method, dsconfig.Auth{}))
		require.Errorf(t, err, "method %q", method)
		assert.Containsf(t, err.Error(), `did you mean "`+want+`"?`, "method %q", method)
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

// TestManagedIdentityBeforeP3bSuggestsAzureDefault: the dedicated methods ship later, and
// azure_default covers both until then.
func TestManagedIdentityBeforeP3bSuggestsAzureDefault(t *testing.T) {
	resetAuthenticators(t)

	for _, method := range []string{"managed_identity", "workload_identity"} {
		_, err := resolveAuth(entra(method, dsconfig.Auth{}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "use azure_default")
	}
	_, err := resolveAuth(entra("activedirectorymsi", dsconfig.Auth{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `did you mean "managed_identity"? It is not available yet; use "azure_default"`)
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
	assert.Contains(t, err.Error(), "auth.tenant_id does not apply to auth.method sql")
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
