package azuread

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"golang.org/x/crypto/pkcs12"
)

// newCredential builds the one azidentity credential of the datasource req describes. It signs
// in nowhere; the token source decides when the credential is asked.
//
// Which keys req.Auth may carry for its method has been checked already, by the engine (see
// sqlserver.RegisterAuthenticator), so this reads them as they are. The option builders below
// are pure functions of req, so what each credential is told can be tested without it.
//
// Every credential that signs in at an Entra ID authority is told the cloud of the host, which
// decides where: a token issued by the public cloud's authority is refused by a US Government or
// China server, whatever its scope. Two methods have no authority to choose. The Azure CLI signs
// in to the cloud `az cloud set` selected, and a managed identity asks the platform's identity
// endpoint, which issues tokens in the resource's own cloud, and only the scope it asks for
// follows the host.
//
// Its errors say what is wrong and not which datasource it is: the engine wraps them with the
// method and the database (see sqlserver.Authenticator), and a second prefix would name both
// twice. No error repeats a secret, a certificate's content or a token.
func newCredential(req sqlserver.AuthRequest) (azcore.TokenCredential, error) {
	client, err := clientOptions(req)
	if err != nil {
		return nil, err
	}

	var cred azcore.TokenCredential
	switch req.Method {
	case sqlserver.AuthMethodInteractive:
		cred, err = credential(azidentity.NewInteractiveBrowserCredential(interactiveOptions(req, client)))
	case sqlserver.AuthMethodDeviceCode:
		cred, err = credential(azidentity.NewDeviceCodeCredential(deviceCodeOptions(req, client)))
	case sqlserver.AuthMethodAzureCLI:
		cred, err = credential(azidentity.NewAzureCLICredential(azureCLIOptions(req)))
	case sqlserver.AuthMethodAzureDefault:
		cred, err = credential(azidentity.NewDefaultAzureCredential(azureDefaultOptions(req, client)))
	case sqlserver.AuthMethodServicePrincipal:
		cred, err = servicePrincipal(req, client)
	case sqlserver.AuthMethodManagedIdentity:
		cred, err = managedIdentity(req, client)
	case sqlserver.AuthMethodWorkloadIdentity:
		cred, err = workloadIdentity(req, client)
	default:
		return nil, fmt.Errorf("azuread: auth.method %q is not a method this package signs in with", req.Method)
	}
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// credential turns an azidentity constructor's result into a TokenCredential, keeping a failed
// constructor's nil pointer from becoming a non-nil interface.
func credential[C azcore.TokenCredential](cred C, err error) (azcore.TokenCredential, error) {
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// clientOptions are the client options of every credential that takes them: the cloud req's host
// belongs to. A managed identity is handed them too, for its HTTP pipeline, and never reads the
// cloud.
func clientOptions(req sqlserver.AuthRequest) (azcore.ClientOptions, error) {
	c, err := cloudConfig(req)
	if err != nil {
		return azcore.ClientOptions{}, err
	}
	return azcore.ClientOptions{Cloud: c}, nil
}

// cloudConfig is the sign-in authority of the cloud req names, which the engine resolves from the
// host and the scope (sqlserver.ResolveCloud); a request built without one gets the same
// resolution here.
func cloudConfig(req sqlserver.AuthRequest) (cloud.Configuration, error) {
	name := req.Cloud
	if name == "" {
		scope, err := scopeOf(req)
		if err != nil {
			return cloud.Configuration{}, err
		}
		if name, err = sqlserver.ResolveCloud(req.Host, scope); err != nil {
			return cloud.Configuration{}, err
		}
	}
	switch name {
	case dsconfig.AzureCloudPublic:
		return cloud.AzurePublic, nil
	case dsconfig.AzureCloudUSGov:
		return cloud.AzureGovernment, nil
	case dsconfig.AzureCloudChina:
		return cloud.AzureChina, nil
	}
	return cloud.Configuration{}, fmt.Errorf("azuread: unknown Azure cloud %q", name)
}

// interactiveOptions sign a person in in the system browser.
//
// DisableAutomaticAuthentication is what keeps it to one prompt: without it, a GetToken that
// cannot be answered from the credential's cache opens the browser again, in the middle of
// whatever query needed a new connection. With it, only Authenticate opens it, and the token
// source calls that once (see cachingTokenSource).
//
// The username is the account the sign-in page suggests; the person may pick another. An empty
// tenant or client is left empty, and azidentity then signs in to the "organizations" tenant
// through Microsoft's development application. A guest account needs tenant_id, and a
// production app registration its own client_id and the redirect_url registered with it.
func interactiveOptions(req sqlserver.AuthRequest, client azcore.ClientOptions) *azidentity.InteractiveBrowserCredentialOptions {
	return &azidentity.InteractiveBrowserCredentialOptions{
		ClientOptions:                  client,
		TenantID:                       req.Auth.TenantID,
		ClientID:                       req.Auth.ClientID,
		LoginHint:                      req.Username,
		RedirectURL:                    req.Auth.RedirectURL,
		DisableAutomaticAuthentication: true,
	}
}

// deviceCodeOptions sign a person in on another device, with a code the credential prints to
// standard output. DisableAutomaticAuthentication keeps it to one code, as for interactive.
func deviceCodeOptions(req sqlserver.AuthRequest, client azcore.ClientOptions) *azidentity.DeviceCodeCredentialOptions {
	return &azidentity.DeviceCodeCredentialOptions{
		ClientOptions:                  client,
		TenantID:                       req.Auth.TenantID,
		ClientID:                       req.Auth.ClientID,
		DisableAutomaticAuthentication: true,
	}
}

// azureCLIOptions ask `az account get-access-token` for the account `az login` chose, in
// tenant_id when it is set.
func azureCLIOptions(req sqlserver.AuthRequest) *azidentity.AzureCLICredentialOptions {
	return &azidentity.AzureCLICredentialOptions{TenantID: req.Auth.TenantID}
}

// azureDefaultOptions build DefaultAzureCredential's chain. What it signs in as comes from the
// environment (AZURE_CLIENT_ID and the other AZURE_* variables azidentity reads), not from the
// config, which is why the engine refuses client_id here.
func azureDefaultOptions(req sqlserver.AuthRequest, client azcore.ClientOptions) *azidentity.DefaultAzureCredentialOptions {
	return &azidentity.DefaultAzureCredentialOptions{ClientOptions: client, TenantID: req.Auth.TenantID}
}

func clientSecretOptions(client azcore.ClientOptions) *azidentity.ClientSecretCredentialOptions {
	return &azidentity.ClientSecretCredentialOptions{ClientOptions: client}
}

func clientCertificateOptions(req sqlserver.AuthRequest, client azcore.ClientOptions) *azidentity.ClientCertificateCredentialOptions {
	return &azidentity.ClientCertificateCredentialOptions{
		ClientOptions:        client,
		SendCertificateChain: req.Auth.SendCertificateChain,
	}
}

// servicePrincipal signs an app registration in with its client secret or, when
// certificate_path is set, its certificate.
func servicePrincipal(req sqlserver.AuthRequest, client azcore.ClientOptions) (azcore.TokenCredential, error) {
	a := req.Auth
	if a.TenantID == "" || a.ClientID == "" {
		return nil, errors.New("a service principal needs auth.tenant_id and auth.client_id")
	}
	if a.CertificatePath == "" {
		return credential(azidentity.NewClientSecretCredential(a.TenantID, a.ClientID, a.ClientSecret,
			clientSecretOptions(client)))
	}

	certs, key, err := readCertificate(a.CertificatePath, a.CertificatePassword)
	if err != nil {
		return nil, err
	}
	return credential(azidentity.NewClientCertificateCredential(a.TenantID, a.ClientID, certs, key,
		clientCertificateOptions(req, client)))
}

// managedIdentity signs in as a managed identity of the Azure resource the app runs on.
func managedIdentity(req sqlserver.AuthRequest, client azcore.ClientOptions) (azcore.TokenCredential, error) {
	opts, err := managedIdentityOptions(req, client)
	if err != nil {
		return nil, err
	}
	return credential(azidentity.NewManagedIdentityCredential(opts))
}

// managedIdentityOptions select the identity: the user-assigned one auth.client_id,
// auth.resource_id or auth.object_id names, and when none is set, no ID at all, which leaves the
// choice to the platform: the resource's default identity, usually its system-assigned one, and
// on Azure ML the one DEFAULT_IDENTITY_CLIENT_ID names. The engine lets at most one through; a
// request built without the engine and carrying more is refused here too, rather than left to
// whichever azidentity would pick.
//
// Nothing here reads AZURE_CLIENT_ID, which only DefaultAzureCredential takes a managed
// identity's client ID from. Which endpoint answers is the platform's to say, and MSAL finds it
// in what the platform sets: IDENTITY_ENDPOINT and IDENTITY_HEADER on App Service, Functions and
// Container Apps, and with IDENTITY_SERVER_THUMBPRINT on Service Fabric; MSI_ENDPOINT in Cloud
// Shell, and with MSI_SECRET on Azure ML; IDENTITY_ENDPOINT and IMDS_ENDPOINT, or the Arc agent's
// himds file on disk even with neither set, on Azure Arc; and the instance metadata service
// everywhere else, a VM, a scale set or an AKS node. A platform that cannot select a
// user-assigned identity the requested way refuses it when the credential is built.
func managedIdentityOptions(req sqlserver.AuthRequest, client azcore.ClientOptions) (*azidentity.ManagedIdentityCredentialOptions, error) {
	a := req.Auth
	var ids []azidentity.ManagedIDKind
	if a.ClientID != "" {
		ids = append(ids, azidentity.ClientID(a.ClientID))
	}
	if a.ResourceID != "" {
		ids = append(ids, azidentity.ResourceID(a.ResourceID))
	}
	if a.ObjectID != "" {
		ids = append(ids, azidentity.ObjectID(a.ObjectID))
	}

	opts := &azidentity.ManagedIdentityCredentialOptions{ClientOptions: client}
	switch len(ids) {
	case 0:
	case 1:
		opts.ID = ids[0]
	default:
		return nil, errors.New("a managed identity is selected by at most one of auth.client_id, " +
			"auth.resource_id and auth.object_id")
	}
	return opts, nil
}

// workloadIdentity signs in as the app registration or user-assigned managed identity a
// Kubernetes service account is federated with, exchanging the service-account token Kubernetes
// projects into the pod for an Entra ID token.
//
// Each of client_id, tenant_id and token_file_path the config leaves empty is azidentity's to
// take from the environment, which the workload identity webhook fills in a pod labelled
// azure.workload.identity/use: "true". One that neither supplies is refused here, in the config's
// words: azidentity's own refusal tells the reader to set a field of its options, which is no key
// they can find.
func workloadIdentity(req sqlserver.AuthRequest, client azcore.ClientOptions) (azcore.TokenCredential, error) {
	opts := workloadIdentityOptions(req, client)
	const webhook = `the workload identity webhook sets it in a pod labelled azure.workload.identity/use: "true"`
	for _, need := range []struct{ value, key, env, what, setBy string }{
		{opts.ClientID, "auth.client_id", "AZURE_CLIENT_ID", "client ID",
			webhook + " whose service account is annotated azure.workload.identity/client-id"},
		{opts.TenantID, "auth.tenant_id", "AZURE_TENANT_ID", "tenant ID", webhook},
		{opts.TokenFilePath, "auth.token_file_path", "AZURE_FEDERATED_TOKEN_FILE", "service-account token file", webhook},
	} {
		if need.value == "" && os.Getenv(need.env) == "" {
			return nil, fmt.Errorf("a workload identity needs its %s: %s is empty and %s is not set; %s",
				need.what, need.key, need.env, need.setBy)
		}
	}
	return credential(azidentity.NewWorkloadIdentityCredential(opts))
}

// workloadIdentityOptions pass on what the config sets and leave the rest empty, for azidentity
// to read from AZURE_CLIENT_ID, AZURE_TENANT_ID and AZURE_FEDERATED_TOKEN_FILE. The authority is
// the host's cloud's, as for every other method that signs in at one, so AZURE_AUTHORITY_HOST,
// which the webhook sets too, is not read.
func workloadIdentityOptions(req sqlserver.AuthRequest, client azcore.ClientOptions) *azidentity.WorkloadIdentityCredentialOptions {
	return &azidentity.WorkloadIdentityCredentialOptions{
		ClientOptions: client,
		ClientID:      req.Auth.ClientID,
		TenantID:      req.Auth.TenantID,
		TokenFilePath: req.Auth.TokenFilePath,
	}
}

// certificateReasons are the errors azidentity.ParseCertificates reports in words of its own,
// which carry nothing of the file, each as the refusal says it. Any other error it returns comes
// from a decoder, and is replaced by a reason of readCertificate's own rather than repeated: a
// decoder's message is about the bytes it was given, and those bytes are a private key.
var certificateReasons = map[string]string{
	"found no certificate":                    "it holds no certificate",
	"didn't find any certificate content":     "it holds no certificate",
	"found no private key":                    "it holds no private key",
	"certData contains multiple private keys": "it holds more than one private key",
	"pkcs12: decryption password incorrect":   wrongPassword,
}

// wrongPassword is certificateReasons' reason for a PKCS#12 file the password does not open.
const wrongPassword = "auth.certificate_password does not decrypt it"

// The reasons readCertificate gives for what azidentity reads wrongly or not at all.
const (
	// pemWithPassword: azidentity reads a file as PEM only without a password, and with one
	// tries it as PKCS#12, which fails in words about ASN.1.
	pemWithPassword = "it is PEM, which takes no password; remove auth.certificate_password"
	// encryptedPEMKey: azidentity decrypts no PEM key. It skips an ENCRYPTED PRIVATE KEY block,
	// and would call the file keyless.
	encryptedPEMKey = "its PEM private key is encrypted, which azidentity cannot read; export it as " +
		"PKCS#12 (.pfx) protected by auth.certificate_password, or as PEM with an unencrypted key"
	// unreadablePKCS12: golang.org/x/crypto/pkcs12, which azidentity reads a .pfx with, knows
	// only the legacy profile, a SHA-1 MAC over 3DES or RC2, and not the AES and SHA-256 that
	// OpenSSL 3 and current Windows export by default.
	unreadablePKCS12 = "it is PKCS#12 protected in a way azidentity cannot read, such as the AES and " +
		"SHA-256 that OpenSSL 3 and current Windows exports use by default; re-export it with " +
		"`openssl pkcs12 -export -legacy`, or convert it to PEM with an unencrypted key"
	unparsablePEM   = "it is PEM, and its certificate or private key cannot be parsed"
	notACertificate = "it is neither PEM nor PKCS#12 (.pfx)"
)

// readCertificate reads a service principal's certificate and private key from path, a PEM file
// with an unencrypted key or a PKCS#12 (.pfx) file in the legacy profile that password decrypts.
//
// Its errors name the path and say what is wrong with the file, and never quote it. What
// azidentity would misreport is looked at before it is asked: a PEM file is recognised as one
// whether or not a password is set, and its key checked for encryption.
func readCertificate(path, password string) ([]*x509.Certificate, crypto.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		var pathErr *fs.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return nil, nil, fmt.Errorf("cannot read auth.certificate_path %q: %w", path, err)
	}
	refuse := func(reason string) ([]*x509.Certificate, crypto.PrivateKey, error) {
		return nil, nil, fmt.Errorf("auth.certificate_path %q cannot be used: %s", path, reason)
	}

	isPEM, encryptedKey := inspectPEM(data)
	switch {
	case encryptedKey:
		return refuse(encryptedPEMKey)
	case isPEM && password != "":
		return refuse(pemWithPassword)
	}

	certs, key, err := azidentity.ParseCertificates(data, []byte(password))
	if err != nil {
		reason, known := certificateReasons[err.Error()]
		switch {
		case known && reason == wrongPassword && password == "":
			reason = "it is PKCS#12 protected by a password, and auth.certificate_password is not set"
		case known:
		case errors.As(err, new(pkcs12.NotImplementedError)):
			reason = unreadablePKCS12
		case isPEM:
			reason = unparsablePEM
		default:
			reason = notACertificate
		}
		return refuse(reason)
	}
	return certs, key, nil
}

// inspectPEM reports whether data is PEM, and whether any block in it is an encrypted private
// key: PKCS#8's ENCRYPTED PRIVATE KEY, or a legacy OpenSSL key with a Proc-Type: 4,ENCRYPTED
// header.
func inspectPEM(data []byte) (isPEM, encryptedKey bool) {
	for {
		var block *pem.Block
		if block, data = pem.Decode(data); block == nil {
			return isPEM, encryptedKey
		}
		isPEM = true
		if block.Type == "ENCRYPTED PRIVATE KEY" || strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED") {
			encryptedKey = true
		}
	}
}
