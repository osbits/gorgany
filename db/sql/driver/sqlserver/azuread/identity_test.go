package azuread

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// managed_identity and workload_identity: which identity each credential is told to sign in as,
// and what it leaves to the environment. The credentials are built, never asked for a token, and
// the environment is the test's: nothing here reaches an identity endpoint or Entra ID. The one
// endpoint a test points a managed identity at is its own, on loopback, and it counts requests
// that must not come.

const (
	standInObject     = "00000000-0000-0000-0000-00000000000c"
	standInResourceID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example/" +
		"providers/Microsoft.ManagedIdentity/userAssignedIdentities/stand-in"
	standInTokenFile = "/var/run/secrets/stand-in/token"
)

// withoutIdentityEnvironment empties the variables azidentity and MSAL read for a managed or a
// workload identity, so that what a test builds does not depend on the variables of the machine
// it runs on: Cloud Shell sets MSI_ENDPOINT, App Service IDENTITY_ENDPOINT, and a runner in an AKS
// pod the AZURE_* ones. An empty value reads as unset to MSAL and to workloadIdentity.
//
// A file it cannot hide: MSAL also takes a machine with the Azure Arc agent's himds file for Azure
// Arc, which has no user-assigned identities. A test that builds one there skips (see
// skipUserAssignedOnAzureArc), or points MSAL at an endpoint of its own (see identityEndpoint).
func withoutIdentityEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AZURE_CLIENT_ID", "AZURE_TENANT_ID", "AZURE_FEDERATED_TOKEN_FILE", "AZURE_AUTHORITY_HOST",
		"IDENTITY_ENDPOINT", "IDENTITY_HEADER", "IDENTITY_SERVER_THUMBPRINT", "IMDS_ENDPOINT",
		"MSI_ENDPOINT", "MSI_SECRET", "DEFAULT_IDENTITY_CLIENT_ID",
	} {
		t.Setenv(name, "")
	}
}

// skipUserAssignedOnAzureArc skips a test about to build a user-assigned managed identity on a
// machine MSAL takes for Azure Arc, where it refuses every one when the credential is built. It
// builds one to ask, which signs in nowhere, so it agrees with MSAL on what counts as Arc, the
// agent's himds file included; any other refusal is left to the test.
func skipUserAssignedOnAzureArc(t *testing.T) {
	t.Helper()
	_, err := azidentity.NewManagedIdentityCredential(&azidentity.ManagedIdentityCredentialOptions{
		ID: azidentity.ClientID(standInClient),
	})
	if err != nil && strings.Contains(err.Error(), "Azure Arc doesn't support user-assigned managed identities") {
		t.Skip("MSAL takes this machine for Azure Arc, which has no user-assigned managed identities")
	}
}

// identityEndpoint points MSAL at an App Service identity endpoint on loopback, which answers
// every request with 500, and returns how many requests it has had. App Service takes every kind
// of ID, and its variables come before Arc's himds file, so what a test builds there does not
// depend on the machine.
func identityEndpoint(t *testing.T) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "stand-in identity endpoint", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	t.Setenv("IDENTITY_ENDPOINT", server.URL)
	t.Setenv("IDENTITY_HEADER", "stand-in-header")
	return &requests
}

// countingCredential counts what the real credential it wraps is asked. azure_live_test.go
// counts the same way.
type countingCredential struct {
	azcore.TokenCredential
	getTokens, authenticates atomic.Int32
}

func (c *countingCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.getTokens.Add(1)
	return c.TokenCredential.GetToken(ctx, opts)
}

func (c *countingCredential) Authenticate(ctx context.Context, opts *policy.TokenRequestOptions) (azidentity.AuthenticationRecord, error) {
	c.authenticates.Add(1)
	if cred, ok := c.TokenCredential.(signInCredential); ok {
		return cred.Authenticate(ctx, opts)
	}
	return azidentity.AuthenticationRecord{}, errors.New("the credential takes no sign-in")
}

// TestManagedIdentityIDSelection: client_id, resource_id and object_id each select a
// user-assigned identity by its own kind of ID, and none leaves the ID unset, which leaves the
// choice to the platform, usually the system-assigned identity; an empty ClientID would ask for a
// user-assigned identity with no ID.
func TestManagedIdentityIDSelection(t *testing.T) {
	withoutIdentityEnvironment(t)

	for name, tc := range map[string]struct {
		auth dsconfig.Auth
		want azidentity.ManagedIDKind
	}{
		"system-assigned": {dsconfig.Auth{}, nil},
		"by client ID":    {dsconfig.Auth{ClientID: standInClient}, azidentity.ClientID(standInClient)},
		"by resource ID":  {dsconfig.Auth{ResourceID: standInResourceID}, azidentity.ResourceID(standInResourceID)},
		"by object ID":    {dsconfig.Auth{ObjectID: standInObject}, azidentity.ObjectID(standInObject)},
	} {
		t.Run(name, func(t *testing.T) {
			req := request(sqlserver.AuthMethodManagedIdentity, usGovHost, tc.auth)
			client, err := clientOptions(req)
			require.NoError(t, err)

			opts, err := managedIdentityOptions(req, client)
			require.NoError(t, err)
			if tc.want == nil {
				assert.Nil(t, opts.ID)
			} else {
				assert.IsType(t, tc.want, opts.ID, "the kind of ID, not only its value")
				assert.Equal(t, tc.want, opts.ID)
			}
			assert.Equal(t, client, opts.ClientOptions, "for its HTTP pipeline; a managed identity "+
				"asks the platform's endpoint, and never reads the cloud in it")

			if tc.want != nil {
				skipUserAssignedOnAzureArc(t)
			}
			cred, err := newCredential(req)
			require.NoError(t, err)
			assert.IsType(t, (*azidentity.ManagedIdentityCredential)(nil), cred)
		})
	}

	// The engine lets one through at most; a request built without it and carrying two is
	// refused rather than left to whichever azidentity would pick, without repeating either.
	_, err := newCredential(request(sqlserver.AuthMethodManagedIdentity, azureHost,
		dsconfig.Auth{ClientID: standInClient, ObjectID: standInObject}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a managed identity is selected by at most one of auth.client_id, "+
		"auth.resource_id and auth.object_id")
	assert.NotContains(t, err.Error(), standInClient)
	assert.NotContains(t, err.Error(), standInObject)
}

// TestManagedIdentityIgnoresAzureClientID: only DefaultAzureCredential takes a managed identity's
// client ID from AZURE_CLIENT_ID. The workload identity webhook sets it in the pods it mutates,
// and a managed identity that picked it up would sign in as the workload's identity instead of
// the node's default identity, which a config with no ID key asks for.
func TestManagedIdentityIgnoresAzureClientID(t *testing.T) {
	withoutIdentityEnvironment(t)
	t.Setenv("AZURE_CLIENT_ID", standInClient)

	req := request(sqlserver.AuthMethodManagedIdentity, azureHost, dsconfig.Auth{})
	client, err := clientOptions(req)
	require.NoError(t, err)
	opts, err := managedIdentityOptions(req, client)
	require.NoError(t, err)
	assert.Nil(t, opts.ID)
}

// TestAPlatformThatCannotSelectTheIdentityRefusesAtBoot: Cloud Shell has no user-assigned
// identities, and MSAL says so when the credential is built, which is when the datasource is
// constructed, not on its first query.
func TestAPlatformThatCannotSelectTheIdentityRefusesAtBoot(t *testing.T) {
	withoutIdentityEnvironment(t)
	t.Setenv("MSI_ENDPOINT", "http://localhost:50342/oauth2/token")

	_, err := newCredential(request(sqlserver.AuthMethodManagedIdentity, azureHost, dsconfig.Auth{ClientID: standInClient}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Cloud Shell doesn't support user-assigned managed identities")
	assert.NotContains(t, err.Error(), standInClient)

	cred, err := newCredential(request(sqlserver.AuthMethodManagedIdentity, azureHost, dsconfig.Auth{}))
	require.NoError(t, err, "with no ID, its default identity is still there")
	assert.IsType(t, (*azidentity.ManagedIdentityCredential)(nil), cred)
}

// TestAManagedIdentityAsksForTheHostsScope: a managed identity has no authority to choose. It
// asks the platform's identity endpoint, and what follows the host is the scope it asks for.
func TestAManagedIdentityAsksForTheHostsScope(t *testing.T) {
	withoutIdentityEnvironment(t)

	for host, want := range map[string]string{
		azureHost: "https://database.windows.net/.default",
		usGovHost: "https://database.usgovcloudapi.net/.default",
		chinaHost: "https://database.chinacloudapi.cn/.default",
	} {
		src, err := authenticate(request(sqlserver.AuthMethodManagedIdentity, host, dsconfig.Auth{}))
		require.NoErrorf(t, err, "%s", host)
		require.IsType(t, (*cachingTokenSource)(nil), src)
		assert.Equalf(t, want, src.(*cachingTokenSource).scope, "%s", host)
	}
}

// TestWorkloadIdentityOptions: what the config sets reaches the credential, and what it leaves
// empty stays empty, for azidentity to take from the environment the workload identity webhook
// sets; what neither supplies is refused in the config's words.
func TestWorkloadIdentityOptions(t *testing.T) {
	withoutIdentityEnvironment(t)

	full := request(sqlserver.AuthMethodWorkloadIdentity, usGovHost, dsconfig.Auth{
		TenantID: standInTenant, ClientID: standInClient, TokenFilePath: standInTokenFile,
	})
	client, err := clientOptions(full)
	require.NoError(t, err)
	assert.Equal(t, &azidentity.WorkloadIdentityCredentialOptions{
		ClientOptions: client,
		ClientID:      standInClient,
		TenantID:      standInTenant,
		TokenFilePath: standInTokenFile,
	}, workloadIdentityOptions(full, client))
	assert.Equal(t, cloud.AzureGovernment, workloadIdentityOptions(full, client).Cloud)
	// azidentity reads AZURE_AUTHORITY_HOST only for a cloud that names no authority host, so
	// the host's cloud naming one is what keeps the webhook's value out.
	assert.NotEmpty(t, workloadIdentityOptions(full, client).Cloud.ActiveDirectoryAuthorityHost)

	cred, err := newCredential(full)
	require.NoError(t, err, "the token file is read on the first token request, not here")
	assert.IsType(t, (*azidentity.WorkloadIdentityCredential)(nil), cred)

	bare := request(sqlserver.AuthMethodWorkloadIdentity, azureHost, dsconfig.Auth{})
	opts := workloadIdentityOptions(bare, client)
	assert.Empty(t, opts.ClientID)
	assert.Empty(t, opts.TenantID)
	assert.Empty(t, opts.TokenFilePath)

	// In a pod the webhook mutated, method: workload_identity alone is enough.
	t.Run("from the environment", func(t *testing.T) {
		t.Setenv("AZURE_CLIENT_ID", standInClient)
		t.Setenv("AZURE_TENANT_ID", standInTenant)
		t.Setenv("AZURE_FEDERATED_TOKEN_FILE", standInTokenFile)
		cred, err := newCredential(bare)
		require.NoError(t, err)
		assert.IsType(t, (*azidentity.WorkloadIdentityCredential)(nil), cred)
	})

	for _, tc := range []struct {
		env, key, want string
	}{
		{"AZURE_CLIENT_ID", "client_id", "a workload identity needs its client ID: auth.client_id is empty and " +
			"AZURE_CLIENT_ID is not set; the workload identity webhook sets it in a pod labelled " +
			`azure.workload.identity/use: "true" whose service account is annotated azure.workload.identity/client-id`},
		{"AZURE_TENANT_ID", "tenant_id", "a workload identity needs its tenant ID: auth.tenant_id is empty and " +
			"AZURE_TENANT_ID is not set"},
		{"AZURE_FEDERATED_TOKEN_FILE", "token_file_path", "a workload identity needs its service-account token " +
			"file: auth.token_file_path is empty and AZURE_FEDERATED_TOKEN_FILE is not set"},
	} {
		t.Run("without "+tc.key, func(t *testing.T) {
			t.Setenv("AZURE_CLIENT_ID", standInClient)
			t.Setenv("AZURE_TENANT_ID", standInTenant)
			t.Setenv("AZURE_FEDERATED_TOKEN_FILE", standInTokenFile)
			t.Setenv(tc.env, "")

			_, err := newCredential(bare)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			for _, value := range []string{standInClient, standInTenant, standInTokenFile} {
				assert.NotContains(t, err.Error(), value)
			}

			// The config's own key fills the gap the environment leaves.
			filled := bare
			switch tc.key {
			case "client_id":
				filled.Auth.ClientID = standInClient
			case "tenant_id":
				filled.Auth.TenantID = standInTenant
			case "token_file_path":
				filled.Auth.TokenFilePath = standInTokenFile
			}
			_, err = newCredential(filled)
			require.NoError(t, err)
		})
	}
}

// TestManagedAndWorkloadIdentityRenewWithoutAPerson: both are token sources like the Azure CLI's
// and a service principal's. They never ask anyone, so they need no Authenticate and never call
// one. A workload identity gets the one-minute bound of a sign-in no person takes part in, and a
// managed identity two, which cover azidentity's retries of the instance metadata service.
func TestManagedAndWorkloadIdentityRenewWithoutAPerson(t *testing.T) {
	for method, timeout := range map[string]time.Duration{
		sqlserver.AuthMethodManagedIdentity:  sqlserver.DefaultManagedIdentityLoginTimeout,
		sqlserver.AuthMethodWorkloadIdentity: sqlserver.DefaultLoginTimeout,
	} {
		t.Run(method, func(t *testing.T) {
			c := newClock()
			req := request(method, azureHost, dsconfig.Auth{})
			_, err := newTokenSource(silentCredential{&fakeCredential{clock: c}}, req, req.Scope)
			require.NoError(t, err, "no Authenticate needed")

			cred := &fakeCredential{clock: c}
			s, _ := source(t, method, cred, c)
			assert.False(t, s.interactive)
			assert.Equal(t, timeout, s.loginTimeout)

			tok, err := s.Token(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "token-1", tok)
			c.advance(time.Hour - renewBefore)
			tok, err = s.Token(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "token-2", tok, "renewed within the margin")

			getTokens, authenticates := cred.counts()
			assert.Equal(t, 2, getTokens)
			assert.Zero(t, authenticates, "no one is asked")
		})
	}
}

// TestTheConstructorBuildsTheCredentialAndSignsInNowhere: with lazy_connect a managed identity
// datasource is constructed with its real credential and closed again without asking it for
// anything, and without a request to the identity endpoint, which would catch a request made
// anywhere below the token source too. A workload identity the environment cannot complete fails
// the constructor, named once by method and database.
func TestTheConstructorBuildsTheCredentialAndSignsInNowhere(t *testing.T) {
	withoutIdentityEnvironment(t)
	var built []*countingCredential
	saved := credentialFor
	credentialFor = func(req sqlserver.AuthRequest) (azcore.TokenCredential, error) {
		cred, err := newCredential(req)
		if err != nil {
			return nil, err
		}
		counted := &countingCredential{TokenCredential: cred}
		built = append(built, counted)
		return counted, nil
	}
	t.Cleanup(func() { credentialFor = saved })

	cfg := func(auth dsconfig.Auth) dsconfig.DataSource {
		return dsconfig.DataSource{
			Driver: "sqlserver_gorm", Host: azureHost, Port: 1433, Database: "Example-db", LazyConnect: true,
			Auth: auth,
		}
	}

	for name, auth := range map[string]dsconfig.Auth{
		"default identity": {Method: sqlserver.AuthMethodManagedIdentity},
		"by resource ID":   {Method: sqlserver.AuthMethodManagedIdentity, ResourceID: standInResourceID},
	} {
		t.Run(name, func(t *testing.T) {
			requests := identityEndpoint(t)
			built = nil
			ds, err := sqlserver.NewDataSourceWithConfig(cfg(auth))
			require.NoError(t, err)
			require.NoError(t, ds.Close())

			require.Len(t, built, 1, "one credential for the datasource")
			assert.IsType(t, (*azidentity.ManagedIdentityCredential)(nil), built[0].TokenCredential, "the real one")
			assert.Zero(t, built[0].getTokens.Load(), "no token was asked for")
			assert.Zero(t, built[0].authenticates.Load(), "no sign-in was asked for")
			assert.Zero(t, requests.Load(), "the identity endpoint was never asked")
		})
	}

	_, err := sqlserver.NewDataSourceWithConfig(cfg(dsconfig.Auth{
		Method: sqlserver.AuthMethodWorkloadIdentity, TenantID: standInTenant, TokenFilePath: standInTokenFile,
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sqlserver: auth.method workload_identity for "+standInTarget+": "+
		"a workload identity needs its client ID: auth.client_id is empty and AZURE_CLIENT_ID is not set")
	assert.Equal(t, 1, strings.Count(err.Error(), "auth.method"), "the method and database are named once")
	assert.NotContains(t, err.Error(), standInTokenFile)
}
