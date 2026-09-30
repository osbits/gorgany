//go:build azuresql

package azuread

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	sqlserver "github.com/osbits/gorgany/v2/db/sql/gorm/sqlserver/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The manual check against a real Azure SQL database, run by hand; CI only compiles it (go vet
// -tags=azuresql), so that an API change cannot break it unnoticed:
//
//	GORGANY_AZURESQL_HOST=set_me.database.windows.net GORGANY_AZURESQL_DB=Example-db \
//	GORGANY_AZURESQL_USER=user@example.com GORGANY_AZURESQL_METHOD=azure_cli \
//	  go test -tags=azuresql ./db/sql/driver/sqlserver/azuread -run AzureSQL -count=1 -v
//
// HOST, DB and METHOD are required, and a missing one fails the run: the build tag is the
// opt-in, so a run that reached no database must not pass. USER is the account an interactive
// sign-in suggests, and when set, the login the server reports must be it. PROBE_TABLE, such as
// dbo.__EFMigrationsHistory, is read with SELECT TOP (1) when set. A service principal takes
// GORGANY_AZURESQL_TENANT_ID, _CLIENT_ID and _CLIENT_SECRET, or _CERTIFICATE_PATH and
// _CERTIFICATE_PASSWORD, and interactive, device_code, azure_cli and azure_default take
// _TENANT_ID. managed_identity takes one of _CLIENT_ID, _RESOURCE_ID and _OBJECT_ID, or none for
// the resource's default identity, and workload_identity takes _TENANT_ID, _CLIENT_ID and
// _TOKEN_FILE_PATH, each optional in a pod the workload identity webhook mutated. They are not
// AZURE_* names, which azidentity reads by itself.
//
// GORGANY_AZURESQL_LAZY=1 sets lazy_connect, so that the first query signs in, as an app with
// lazy_connect does: with interactive or device_code, take your time over the sign-in, well
// over a minute, and the query must still succeed, since the connection is opened only after it.
//
// The datasource is read_only and external_schema, and the test only reads. One datasource
// serves every subtest, so interactive prompts once for the whole run.

// liveEnv reads GORGANY_AZURESQL_<name>, failing t when required and unset.
func liveEnv(t *testing.T, name string, required bool) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv("GORGANY_AZURESQL_" + name))
	if value == "" && required {
		t.Fatalf("GORGANY_AZURESQL_%s is not set; the azuresql tag runs against a real Azure SQL database, "+
			"so set GORGANY_AZURESQL_HOST, _DB and _METHOD (see azure_live_test.go)", name)
	}
	return value
}

func liveConfig(t *testing.T) dsconfig.DataSource {
	t.Helper()
	cfg := dsconfig.DataSource{
		Driver:         "sqlserver_gorm",
		Host:           liveEnv(t, "HOST", true),
		Port:           sqlserver.DefaultPort,
		Database:       liveEnv(t, "DB", true),
		Username:       liveEnv(t, "USER", false),
		ReadOnly:       true,
		ExternalSchema: true,
		LazyConnect:    liveEnv(t, "LAZY", false) == "1",
		Auth: dsconfig.Auth{
			// Lowercased as the config parser does, so that the checks below see the method
			// the engine signs in with.
			Method:              strings.ToLower(liveEnv(t, "METHOD", true)),
			TenantID:            liveEnv(t, "TENANT_ID", false),
			ClientID:            liveEnv(t, "CLIENT_ID", false),
			ClientSecret:        liveEnv(t, "CLIENT_SECRET", false),
			CertificatePath:     liveEnv(t, "CERTIFICATE_PATH", false),
			CertificatePassword: liveEnv(t, "CERTIFICATE_PASSWORD", false),
			ResourceID:          liveEnv(t, "RESOURCE_ID", false),
			ObjectID:            liveEnv(t, "OBJECT_ID", false),
			TokenFilePath:       liveEnv(t, "TOKEN_FILE_PATH", false),
		},
	}
	if port := liveEnv(t, "PORT", false); port != "" {
		n, err := strconv.Atoi(port)
		require.NoError(t, err, "GORGANY_AZURESQL_PORT")
		cfg.Port = n
	}
	return cfg
}

func TestAzureSQLWithTheConfiguredMethod(t *testing.T) {
	cfg := liveConfig(t)

	var counted *countingCredential
	saved := credentialFor
	credentialFor = func(req sqlserver.AuthRequest, remembered *rememberedSignIn) (azcore.TokenCredential, error) {
		cred, err := newCredential(req, remembered)
		if err != nil {
			return nil, err
		}
		counted = &countingCredential{TokenCredential: cred}
		return counted, nil
	}
	t.Cleanup(func() { credentialFor = saved })

	t.Logf("auth.method %s, lazy_connect %v", cfg.Auth.Method, cfg.LazyConnect)
	ds, err := sqlserver.NewDataSourceWithConfig(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, ds.Close()) })
	require.NotNil(t, counted)

	session, err := ds.NewSession()
	require.NoError(t, err)
	ctx := context.Background()

	t.Run("login", func(t *testing.T) {
		var who []struct {
			DatabaseName string
			LoginName    string
		}
		require.NoError(t, session.Executor().FindRaw(ctx, &who,
			"SELECT DB_NAME() AS [database_name], SUSER_SNAME() AS [login_name]").Error)
		require.Len(t, who, 1)
		t.Logf("DB_NAME() = %s, SUSER_SNAME() = %s", who[0].DatabaseName, who[0].LoginName)

		assert.Equal(t, cfg.Database, who[0].DatabaseName)
		assert.NotEmpty(t, who[0].LoginName)
		if cfg.Username != "" {
			assert.True(t, strings.EqualFold(cfg.Username, who[0].LoginName),
				"signed in as %s, not %s", who[0].LoginName, cfg.Username)
		}

		var options []struct{ XactAbort int }
		require.NoError(t, session.Executor().FindRaw(ctx, &options,
			"SELECT CAST(@@OPTIONS & 16384 AS int) AS [xact_abort]").Error)
		require.Len(t, options, 1)
		assert.Equal(t, 16384, options[0].XactAbort, "the session's init set XACT_ABORT ON")
	})

	t.Run("probe", func(t *testing.T) {
		probe := liveEnv(t, "PROBE_TABLE", false)
		if probe == "" {
			t.Skip("GORGANY_AZURESQL_PROBE_TABLE is not set")
		}
		q := session.Query().From(probe).Limit(1)
		sql, _, err := q.ToSQL()
		require.NoError(t, err)
		assert.Contains(t, sql, "SELECT TOP (1) * FROM [")

		var rows []map[string]any
		require.NoError(t, session.Executor().Find(ctx, q, &rows).Error)
		t.Logf("%s: %d row(s)", sql, len(rows))
	})

	// Eight connections opened at once, each a new physical login, sign in with the token the
	// warm-up fetched, or under LAZY the first query: the credential is asked once for the whole
	// run.
	t.Run("concurrent logins fetch one token", func(t *testing.T) {
		handle, err := ds.GetDriver()
		require.NoError(t, err)
		db, err := handle.(*gorm.DB).DB()
		require.NoError(t, err)

		const logins = 8
		var opened, release sync.WaitGroup
		release.Add(1)
		errs := make(chan error, logins)
		for range logins {
			opened.Add(1)
			go func() {
				conn, err := db.Conn(ctx)
				if err == nil {
					err = conn.PingContext(ctx)
				}
				opened.Done()
				release.Wait() // hold every connection until all are open, so none is reused
				if conn != nil {
					_ = conn.Close()
				}
				errs <- err
			}()
		}
		opened.Wait()
		assert.GreaterOrEqual(t, db.Stats().OpenConnections, logins)
		release.Done()
		for range logins {
			require.NoError(t, <-errs)
		}

		assert.Equal(t, int32(1), counted.getTokens.Load(), "one token for every login")
		if cfg.Auth.Method == sqlserver.AuthMethodInteractive || cfg.Auth.Method == sqlserver.AuthMethodDeviceCode {
			assert.Equal(t, int32(1), counted.authenticates.Load(), "one sign-in per datasource")
		} else {
			assert.Zero(t, counted.authenticates.Load(), "no method but interactive and device_code asks a person")
		}
	})
}
