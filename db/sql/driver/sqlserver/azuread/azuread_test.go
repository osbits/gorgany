package azuread

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"reflect"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Registration, and what each method's credential is told. Nothing here signs in anywhere: the
// credentials are built and inspected, never asked for a token.

const (
	azureHost    = "example.database.windows.net"
	usGovHost    = "example.database.usgovcloudapi.net"
	chinaHost    = "example.database.chinacloudapi.cn"
	standInUser  = "user@example.com"
	standInToken = "eyJ0eXAiOiJKV1QiLCJzdGFuZC1pbiI6InRva2VuIn0"
	// The tenant and client IDs are well-formed and belong to no one.
	standInTenant = "00000000-0000-0000-0000-00000000000a"
	standInClient = "00000000-0000-0000-0000-00000000000b"
	standInSecret = "stand-in-client-secret"
)

// standInTarget is the Azure host's database as the engine's errors name it.
const standInTarget = azureHost + ":1433/Example-db"

// request is the AuthRequest the engine would build for method against host.
func request(method, host string, auth dsconfig.Auth) sqlserver.AuthRequest {
	auth.Method = method
	scope, _ := sqlserver.ResolveScope(host, auth.Scope)
	cloud, _ := sqlserver.ResolveCloud(host, scope)
	return sqlserver.AuthRequest{
		Method:   method,
		Auth:     auth,
		Username: standInUser,
		Host:     host,
		Database: "Example-db",
		Target:   host + ":1433/Example-db",
		Scope:    scope,
		Cloud:    cloud,
	}
}

func TestEveryAzureADMethodIsRegistered(t *testing.T) {
	registered := sqlserver.RegisteredAuthMethods()
	for _, method := range []string{"interactive", "device_code", "azure_cli", "azure_default", "service_principal"} {
		assert.Containsf(t, registered, method, "importing azuread registers %s", method)
	}
	assert.ElementsMatch(t, methods, []string{
		sqlserver.AuthMethodInteractive, sqlserver.AuthMethodDeviceCode, sqlserver.AuthMethodAzureCLI,
		sqlserver.AuthMethodAzureDefault, sqlserver.AuthMethodServicePrincipal,
	})
	assert.NotContains(t, registered, sqlserver.AuthMethodSQL, "the SQL login needs no authenticator")
}

// TestImportingAzureADAlsoRegistersTheDriver: an app that signs in with Entra ID imports this
// package alone, and must not also need the plain driver import to boot.
func TestImportingAzureADAlsoRegistersTheDriver(t *testing.T) {
	ctor, ok := driver.Lookup("sqlserver_gorm")
	require.True(t, ok, "the blank import of driver/sqlserver registered it")
	assert.NotNil(t, ctor)
}

// TestTheEngineNamesThisPackage: the engine's missing-import error quotes
// sqlserver.AzureADImportPath, and an import line that names another path would fail the build.
func TestTheEngineNamesThisPackage(t *testing.T) {
	assert.Equal(t, sqlserver.AzureADImportPath, reflect.TypeOf(acquisition{}).PkgPath())
}

// TestInteractiveOptions: the person is asked once, by Authenticate, and never by a GetToken
// that finds the cache empty; the username is only the account the sign-in page suggests.
func TestInteractiveOptions(t *testing.T) {
	req := request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{
		TenantID: standInTenant, ClientID: standInClient, RedirectURL: "http://localhost:8400",
	})
	client, err := clientOptions(req)
	require.NoError(t, err)

	opts := interactiveOptions(req, client)
	assert.True(t, opts.DisableAutomaticAuthentication, "a GetToken must never open a browser")
	assert.Equal(t, standInUser, opts.LoginHint)
	assert.Equal(t, standInTenant, opts.TenantID)
	assert.Equal(t, standInClient, opts.ClientID)
	assert.Equal(t, "http://localhost:8400", opts.RedirectURL)
	assert.Equal(t, cloud.AzurePublic, opts.Cloud)
	assert.Zero(t, opts.Cache, "the persistent cache links the keychain and is not this package's")
	assert.Zero(t, opts.AuthenticationRecord)

	// An empty tenant and client stay empty, so azidentity's own defaults apply: the
	// organizations tenant and its development application.
	bare := interactiveOptions(request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{}), client)
	assert.Empty(t, bare.TenantID)
	assert.Empty(t, bare.ClientID)
	assert.True(t, bare.DisableAutomaticAuthentication)

	device := deviceCodeOptions(request(sqlserver.AuthMethodDeviceCode, azureHost, dsconfig.Auth{
		TenantID: standInTenant, ClientID: standInClient,
	}), client)
	assert.True(t, device.DisableAutomaticAuthentication, "one device code per datasource, as one browser")
	assert.Equal(t, standInTenant, device.TenantID)
	assert.Equal(t, standInClient, device.ClientID)
	assert.Zero(t, device.Cache)
}

func TestTheToolMethodsTakeOnlyATenant(t *testing.T) {
	req := request(sqlserver.AuthMethodAzureCLI, azureHost, dsconfig.Auth{TenantID: standInTenant})
	assert.Equal(t, &azidentity.AzureCLICredentialOptions{TenantID: standInTenant}, azureCLIOptions(req))

	client, err := clientOptions(req)
	require.NoError(t, err)
	assert.Equal(t, &azidentity.DefaultAzureCredentialOptions{ClientOptions: client, TenantID: standInTenant},
		azureDefaultOptions(request(sqlserver.AuthMethodAzureDefault, azureHost, dsconfig.Auth{TenantID: standInTenant}), client))
}

// TestSovereignHostsSelectTheirAuthority: a US Government or China server refuses a token the
// public cloud issued, whatever its scope, so the authority follows the host as the scope does.
func TestSovereignHostsSelectTheirAuthority(t *testing.T) {
	for host, want := range map[string]cloud.Configuration{
		azureHost:   cloud.AzurePublic,
		usGovHost:   cloud.AzureGovernment,
		chinaHost:   cloud.AzureChina,
		"127.0.0.1": cloud.AzurePublic,
	} {
		req := request(sqlserver.AuthMethodInteractive, host, dsconfig.Auth{
			TenantID: standInTenant, ClientID: standInClient, ClientSecret: standInSecret,
		})
		client, err := clientOptions(req)
		require.NoError(t, err)

		assert.Equalf(t, want, interactiveOptions(req, client).Cloud, "interactive, %s", host)
		assert.Equalf(t, want, deviceCodeOptions(req, client).Cloud, "device_code, %s", host)
		assert.Equalf(t, want, azureDefaultOptions(req, client).Cloud, "azure_default, %s", host)
		assert.Equalf(t, want, clientSecretOptions(client).Cloud, "service_principal secret, %s", host)
		assert.Equalf(t, want, clientCertificateOptions(req, client).Cloud, "service_principal certificate, %s", host)

		// A request built without a cloud gets the host's.
		req.Cloud = ""
		derived, err := cloudConfig(req)
		require.NoError(t, err)
		assert.Equalf(t, want, derived, "derived from %s", host)
	}

	req := request(sqlserver.AuthMethodInteractive, usGovHost, dsconfig.Auth{})
	assert.Equal(t, "https://database.usgovcloudapi.net/.default", req.Scope, "and the scope follows it too")

	req.Cloud = "mars"
	_, err := newCredential(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown Azure cloud "mars"`)
}

// TestAScopeSelectsTheAuthorityOfAHostThatNamesNoCloud: a private endpoint behind a DNS name of
// its own says nothing about its cloud, and a scope that names US Government Azure SQL would get
// a token only from the US Government authority, so the scope chooses it, and the credential is
// told so whether the engine resolved the cloud or not.
func TestAScopeSelectsTheAuthorityOfAHostThatNamesNoCloud(t *testing.T) {
	req := request(sqlserver.AuthMethodInteractive, "sql.example.com",
		dsconfig.Auth{Scope: "https://database.usgovcloudapi.net"})
	assert.Equal(t, dsconfig.AzureCloudUSGov, req.Cloud)
	client, err := clientOptions(req)
	require.NoError(t, err)
	assert.Equal(t, cloud.AzureGovernment, client.Cloud)

	req.Cloud = ""
	client, err = clientOptions(req)
	require.NoError(t, err)
	assert.Equal(t, cloud.AzureGovernment, client.Cloud, "resolved here as the engine does")

	// On an Azure SQL host the same scope contradicts the host, and no credential is built.
	contradiction := request(sqlserver.AuthMethodInteractive, azureHost,
		dsconfig.Auth{Scope: "https://database.usgovcloudapi.net"})
	contradiction.Cloud = ""
	_, err = newCredential(contradiction)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is an Azure SQL host in the public cloud")
}

// TestEachMethodBuildsItsCredential: one azidentity credential per method, built without
// signing in.
func TestEachMethodBuildsItsCredential(t *testing.T) {
	for method, want := range map[string]azcore.TokenCredential{
		sqlserver.AuthMethodInteractive:  (*azidentity.InteractiveBrowserCredential)(nil),
		sqlserver.AuthMethodDeviceCode:   (*azidentity.DeviceCodeCredential)(nil),
		sqlserver.AuthMethodAzureCLI:     (*azidentity.AzureCLICredential)(nil),
		sqlserver.AuthMethodAzureDefault: (*azidentity.DefaultAzureCredential)(nil),
	} {
		cred, err := newCredential(request(method, azureHost, dsconfig.Auth{TenantID: standInTenant}))
		require.NoErrorf(t, err, "method %s", method)
		assert.IsTypef(t, want, cred, "method %s", method)
	}

	_, err := newCredential(request("vault_token", azureHost, dsconfig.Auth{}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `auth.method "vault_token" is not a method this package signs in with`)
}

// TestCredentialErrorsLeaveTheDatasourceToTheEngine: the engine wraps an Authenticator's error
// with the method and the database, so the credential's own error names neither, and the one a
// reader sees names each once.
func TestCredentialErrorsLeaveTheDatasourceToTheEngine(t *testing.T) {
	_, err := newCredential(request(sqlserver.AuthMethodInteractive, azureHost, dsconfig.Auth{TenantID: "not a tenant!"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid tenantID")
	assert.NotContains(t, err.Error(), "auth.method")
	assert.NotContains(t, err.Error(), azureHost)
}

// TestTheTargetIsTheEnginesOrHostAndDatabase: a request the engine built names the database as
// the engine does, and one built without a target by host and database.
func TestTheTargetIsTheEnginesOrHostAndDatabase(t *testing.T) {
	req := request(sqlserver.AuthMethodAzureCLI, azureHost, dsconfig.Auth{})
	assert.Equal(t, standInTarget, target(req))
	req.Target = ""
	assert.Equal(t, azureHost+"/Example-db", target(req))
}

func TestServicePrincipalSecretCredential(t *testing.T) {
	cred, err := newCredential(request(sqlserver.AuthMethodServicePrincipal, azureHost, dsconfig.Auth{
		TenantID: standInTenant, ClientID: standInClient, ClientSecret: standInSecret,
	}))
	require.NoError(t, err)
	assert.IsType(t, (*azidentity.ClientSecretCredential)(nil), cred)

	_, err = newCredential(request(sqlserver.AuthMethodServicePrincipal, azureHost, dsconfig.Auth{
		ClientID: standInClient, ClientSecret: standInSecret,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs auth.tenant_id and auth.client_id")
	assert.NotContains(t, err.Error(), standInSecret)
}

// TestServicePrincipalCertificateFromPEMAndPKCS12: the two forms a certificate arrives in, `az ad
// sp create-for-rbac --create-cert`'s PEM and a Key Vault or Windows export's .pfx, each made
// here rather than committed.
func TestServicePrincipalCertificateFromPEMAndPKCS12(t *testing.T) {
	cert := newTestCertificate(t)
	const password = "stand-in-pfx-password"

	for name, auth := range map[string]dsconfig.Auth{
		"PEM":     {CertificatePath: writeFile(t, "sp.pem", cert.pem(t))},
		"PKCS#12": {CertificatePath: writeFile(t, "sp.pfx", cert.pkcs12(t, password)), CertificatePassword: password},
		"chain":   {CertificatePath: writeFile(t, "sp-chain.pem", cert.pem(t)), SendCertificateChain: true},
	} {
		t.Run(name, func(t *testing.T) {
			auth.TenantID, auth.ClientID = standInTenant, standInClient
			req := request(sqlserver.AuthMethodServicePrincipal, usGovHost, auth)

			cred, err := newCredential(req)
			require.NoError(t, err)
			assert.IsType(t, (*azidentity.ClientCertificateCredential)(nil), cred)

			client, err := clientOptions(req)
			require.NoError(t, err)
			opts := clientCertificateOptions(req, client)
			assert.Equal(t, auth.SendCertificateChain, opts.SendCertificateChain)
			assert.Equal(t, cloud.AzureGovernment, opts.Cloud)
		})
	}

	certs, key, err := readCertificate(writeFile(t, "sp.pfx", cert.pkcs12(t, password)), password)
	require.NoError(t, err)
	require.Len(t, certs, 1)
	assert.Equal(t, cert.der, certs[0].Raw, "the certificate that went in came out")
	assert.True(t, cert.key.Equal(key))
}

// TestCertificateErrorsNameThePathNotTheContent: the file holds a private key, and an error ends
// up in a log.
func TestCertificateErrorsNameThePathNotTheContent(t *testing.T) {
	cert := newTestCertificate(t)
	pemFile := cert.pem(t)
	const password = "stand-in-pfx-password"
	pfx := cert.pkcs12(t, password)

	// A key block whose DER is cut short, so the decoders see key material and fail on it.
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.key)
	require.NoError(t, err)
	corrupt := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER[:len(keyDER)/2]})...)
	onlyKey := pemFile[strings.Index(string(pemFile), "-----BEGIN PRIVATE KEY"):]
	onlyCert := pemFile[:strings.Index(string(pemFile), "-----BEGIN PRIVATE KEY")]

	// An encrypted key, in PKCS#8's form and in OpenSSL's legacy one. The ciphertext is random:
	// what matters is that the file says its key is encrypted.
	ciphertext := make([]byte, 256)
	_, err = rand.Read(ciphertext)
	require.NoError(t, err)
	encryptedPKCS8 := append(append([]byte{}, onlyCert...),
		pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: ciphertext})...)
	encryptedLegacy := append(append([]byte{}, onlyCert...), pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00112233445566778899AABBCCDDEEFF"},
		Bytes:   ciphertext,
	})...)
	// A .pfx whose MAC is HMAC-SHA256, as OpenSSL 3 and current Windows export one by default.
	modernPFX := cert.pkcs12WithMAC(t, password, crypto.SHA256)

	for name, tc := range map[string]struct {
		data     []byte
		password string
		want     string
	}{
		"a corrupt key":                {corrupt, "", "it is PEM, and its certificate or private key cannot be parsed"},
		"a key and no certificate":     {onlyKey, "", "it holds no certificate"},
		"a certificate and no key":     {onlyCert, "", "it holds no private key"},
		"a .pfx and a wrong password":  {pfx, "not-the-password", "auth.certificate_password does not decrypt it"},
		"a .pfx and no password":       {pfx, "", "it is PKCS#12 protected by a password, and auth.certificate_password is not set"},
		"a PEM and a password":         {pemFile, password, "it is PEM, which takes no password; remove auth.certificate_password"},
		"an encrypted PKCS#8 key":      {encryptedPKCS8, "", "its PEM private key is encrypted, which azidentity cannot read"},
		"an encrypted key, a password": {encryptedPKCS8, password, "its PEM private key is encrypted"},
		"a legacy encrypted key":       {encryptedLegacy, password, "its PEM private key is encrypted"},
		"a .pfx with a SHA-256 MAC":    {modernPFX, password, "it is PKCS#12 protected in a way azidentity cannot read"},
		"not a certificate at all":     {[]byte("stand-in-not-a-certificate"), "", "it is neither PEM nor PKCS#12 (.pfx)"},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, "sp.pem", tc.data)
			_, err := newCredential(request(sqlserver.AuthMethodServicePrincipal, azureHost, dsconfig.Auth{
				TenantID: standInTenant, ClientID: standInClient, CertificatePath: path, CertificatePassword: tc.password,
			}))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), path, "the error names the file")
			assert.NotContains(t, err.Error(), "BEGIN", "and never quotes it")
			assertNoContent(t, err.Error(), tc.data)
			if tc.password != "" {
				assert.NotContains(t, err.Error(), tc.password)
			}
		})
	}

	missing := t.TempDir() + "/missing.pem"
	_, err = newCredential(request(sqlserver.AuthMethodServicePrincipal, azureHost, dsconfig.Auth{
		TenantID: standInTenant, ClientID: standInClient, CertificatePath: missing,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot read auth.certificate_path "`+missing+`": no such file or directory`)
}

// assertNoContent fails when message repeats any 16-byte run of data, printable or not.
func assertNoContent(t *testing.T, message string, data []byte) {
	t.Helper()
	const run = 16
	for i := 0; i+run <= len(data); i += run / 2 {
		if strings.Contains(message, string(data[i:i+run])) {
			t.Errorf("the error repeats bytes %d-%d of the certificate file", i, i+run)
			return
		}
	}
}
