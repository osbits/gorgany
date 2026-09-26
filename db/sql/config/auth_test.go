package config_test

import (
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The auth block is parsed as strictly as `properties`, and more carefully: two of its keys
// are secrets, so no error it raises may carry their values.

// withAuth returns a valid config carrying the given auth block.
func withAuth(auth any) map[string]any {
	raw := validRaw()
	raw["auth"] = auth
	return raw
}

func TestParseReadsTheAuthBlock(t *testing.T) {
	cfg, err := config.Parse(withAuth(map[string]any{
		"method":                 "service_principal",
		"tenant_id":              "00000000-0000-0000-0000-000000000000",
		"client_id":              "11111111-1111-1111-1111-111111111111",
		"client_secret":          "example-client-secret",
		"certificate_path":       "/etc/example/client.pem",
		"certificate_password":   "example-certificate-password",
		"send_certificate_chain": true,
		"resource_id":            "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example/providers/Microsoft.ManagedIdentity/userAssignedIdentities/example",
		"object_id":              "22222222-2222-2222-2222-222222222222",
		"token_file_path":        "/var/run/secrets/example/token",
		"redirect_url":           "http://localhost:8400",
		"scope":                  "https://database.windows.net/.default",
		"login_timeout":          120,
	}))
	require.NoError(t, err)

	assert.Equal(t, config.Auth{
		Method:               "service_principal",
		TenantID:             "00000000-0000-0000-0000-000000000000",
		ClientID:             "11111111-1111-1111-1111-111111111111",
		ClientSecret:         "example-client-secret",
		CertificatePath:      "/etc/example/client.pem",
		CertificatePassword:  "example-certificate-password",
		SendCertificateChain: true,
		ResourceID:           "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/example/providers/Microsoft.ManagedIdentity/userAssignedIdentities/example",
		ObjectID:             "22222222-2222-2222-2222-222222222222",
		TokenFilePath:        "/var/run/secrets/example/token",
		RedirectURL:          "http://localhost:8400",
		Scope:                "https://database.windows.net/.default",
		LoginTimeout:         120 * time.Second,
	}, cfg.Auth)
	assert.False(t, cfg.Auth.IsZero())
	require.NoError(t, cfg.Validate())
}

// TestAuthMethodIsLowercasedAndTrimmed: the method is matched like a key, so it is folded like
// one, and a trailing space from a hand-edited env file must not turn it into an unknown one.
func TestAuthMethodIsLowercasedAndTrimmed(t *testing.T) {
	for _, written := range []string{"Service_Principal", " service_principal ", "SERVICE_PRINCIPAL\t"} {
		cfg, err := config.Parse(withAuth(map[string]any{"method": written}))
		require.NoErrorf(t, err, "%q", written)
		assert.Equalf(t, "service_principal", cfg.Auth.Method, "%q", written)
	}
}

// TestOtherAuthValuesAreKeptVerbatim. Only the method is folded; a tenant, a path or a URL is
// the operator's exact text.
func TestOtherAuthValuesAreKeptVerbatim(t *testing.T) {
	cfg, err := config.Parse(withAuth(map[string]any{
		"certificate_path": "/etc/Example/Client.pem",
		"redirect_url":     "http://localhost:8400/Callback",
	}))
	require.NoError(t, err)

	assert.Equal(t, "/etc/Example/Client.pem", cfg.Auth.CertificatePath)
	assert.Equal(t, "http://localhost:8400/Callback", cfg.Auth.RedirectURL)
}

func TestAuthKeysAreCaseInsensitive(t *testing.T) {
	cfg, err := config.Parse(withAuth(map[string]any{
		"Method":    "interactive",
		"Tenant_ID": "00000000-0000-0000-0000-000000000000",
	}))
	require.NoError(t, err)

	assert.Equal(t, "interactive", cfg.Auth.Method)
	assert.Equal(t, "00000000-0000-0000-0000-000000000000", cfg.Auth.TenantID)
}

// TestAnAbsentAuthBlockIsTheZeroValue, which is what every existing Postgres and MySQL config
// parses to, and which those engines accept as SQL login.
func TestAnAbsentAuthBlockIsTheZeroValue(t *testing.T) {
	absent, err := config.Parse(validRaw())
	require.NoError(t, err)
	assert.True(t, absent.Auth.IsZero())

	// `auth:` with nothing under it is a YAML null.
	null, err := config.Parse(withAuth(nil))
	require.NoError(t, err)
	assert.True(t, null.Auth.IsZero())

	empty, err := config.Parse(withAuth(map[string]any{}))
	require.NoError(t, err)
	assert.True(t, empty.Auth.IsZero())
}

// TestIsZeroIsOnlyTheZeroValue. Method "sql" says the same thing as an absent block, but it is
// a value the operator wrote, and IsZero does not decide whether an engine accepts it.
func TestIsZeroIsOnlyTheZeroValue(t *testing.T) {
	assert.True(t, config.Auth{}.IsZero())
	assert.False(t, config.Auth{Method: "sql"}.IsZero())
	assert.False(t, config.Auth{LoginTimeout: time.Second}.IsZero())
	assert.False(t, config.Auth{SendCertificateChain: true}.IsZero())
}

func TestAuthMustBeAMap(t *testing.T) {
	for _, value := range []any{"interactive", 1, []any{"method"}} {
		_, err := config.Parse(withAuth(value))
		require.Errorf(t, err, "%#v must be refused", value)
		assert.Contains(t, err.Error(), "'auth'")
		assert.Contains(t, err.Error(), "map")
	}
}

func TestAuthAcceptsAMapAnyAny(t *testing.T) {
	cfg, err := config.Parse(withAuth(map[any]any{"method": "azure_cli"}))
	require.NoError(t, err)
	assert.Equal(t, "azure_cli", cfg.Auth.Method)
}

// TestAnUnknownAuthKeyIsRefusedWithASuggestion. A misspelt tenant ignored would sign in
// against the identity SDK's default tenant, so the key is refused; and the spellings a
// connection string or an Azure snippet uses get pointed at ours.
func TestAnUnknownAuthKeyIsRefusedWithASuggestion(t *testing.T) {
	tests := map[string]string{
		"tenant":              "'tenant_id'",
		"tenantid":            "'tenant_id'",
		"TenantId":            "'tenant_id'",
		"client":              "'client_id'",
		"clientid":            "'client_id'",
		"app_id":              "'client_id'",
		"application_id":      "'client_id'",
		"applicationclientid": "'client_id'",
		"secret":              "'client_secret'",
		"clientsecret":        "'client_secret'",
		"cert":                "'certificate_path'",
		"certificate":         "'certificate_path'",
		"cert_path":           "'certificate_path'",
		"clientcertpath":      "'certificate_path'",
		"cert_password":       "'certificate_password'",
		"cert_pass":           "'certificate_password'",
		"cert_pwd":            "'certificate_password'",
		"certificate_pass":    "'certificate_password'", // two edits from certificate_path
		"certificate_pwd":     "'certificate_password'",
		"type":                "'method'",
		"mode":                "'method'",
		"fedauth":             "'method'",
		"authentication":      "'method'",
		"tenant_di":           "'tenant_id'", // a typo, caught by distance
		"scop":                "'scope'",
	}

	for written, want := range tests {
		t.Run(written, func(t *testing.T) {
			_, err := config.Parse(withAuth(map[string]any{written: "x"}))
			require.Error(t, err)

			message := err.Error()
			assert.Contains(t, message, "unknown key(s) '"+written+"' under 'auth'")
			assert.Contains(t, message, "did you mean "+want+" instead of '"+written+"'?")
			assert.Contains(t, message, "recognised keys are")
			assert.Contains(t, message, "tenant_id", "the recognised keys are listed")
		})
	}
}

// TestALoginNameInsideAuthPointsToTheTopLevelUsername. The login name has one home, the
// datasource's own username, which an interactive sign-in reads as the account hint.
func TestALoginNameInsideAuthPointsToTheTopLevelUsername(t *testing.T) {
	for _, written := range []string{"login_hint", "user", "user_id", "username", "UserName"} {
		_, err := config.Parse(withAuth(map[string]any{written: "user@example.com"}))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "did you mean the top-level username instead of '"+written+"'?")
	}
}

// TestAPasswordInsideAuthPointsToTheTopLevelPassword, which is what method sql signs in with,
// and the suggestion never repeats it.
func TestAPasswordInsideAuthPointsToTheTopLevelPassword(t *testing.T) {
	_, err := config.Parse(withAuth(map[string]any{"method": "sql", "username": "user@example.com", "password": "example-password"}))
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, "unknown key(s) 'password', 'username' under 'auth'")
	assert.Contains(t, message, "did you mean the top-level password instead of 'password'?")
	assert.Contains(t, message, "did you mean the top-level username instead of 'username'?")
	assert.NotContains(t, message, "example-password")
}

func TestAnUnrelatedAuthKeyGetsNoSuggestion(t *testing.T) {
	_, err := config.Parse(withAuth(map[string]any{"featureflags": "x"}))
	require.Error(t, err)

	assert.Contains(t, err.Error(), "'featureflags'")
	assert.NotContains(t, err.Error(), "did you mean")
	assert.Contains(t, err.Error(), "recognised keys are")
}

// TestAnUnknownAuthKeyDoesNotEchoItsValue. The key most likely to be misspelt is a secret's,
// and the refusal names keys only.
func TestAnUnknownAuthKeyDoesNotEchoItsValue(t *testing.T) {
	_, err := config.Parse(withAuth(map[string]any{"secret": "example-secret-value"}))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "example-secret-value")
}

// TestAuthSecretTypeErrorsDoNotEchoTheValue. optString echoes the offending value, which is
// right for a port and wrong for a secret: the boot error lands in the deploy log.
func TestAuthSecretTypeErrorsDoNotEchoTheValue(t *testing.T) {
	for _, key := range []string{"client_secret", "certificate_password"} {
		for _, value := range []any{
			12345678,
			int64(87654321),
			1e3, // YAML's reading of an unquoted 1e3
			true,
			map[string]any{"nested": "example-secret-value"},
			[]any{"example-secret-value"},
		} {
			_, err := config.Parse(withAuth(map[string]any{key: value}))
			require.Errorf(t, err, "%s: %T must be refused", key, value)

			message := err.Error()
			assert.Contains(t, message, "'auth."+key+"'")
			assert.Contains(t, message, "must be a string")
			for _, leaked := range []string{"12345678", "87654321", "1000", "true", "example-secret-value"} {
				assert.NotContainsf(t, message, leaked, "%s: the error must not echo the value", key)
			}
		}
	}
}

// TestPasswordAndOptionTypeErrorsDoNotEchoTheValue, for the auth secrets' reason. The
// top-level password is the datasource's other secret, and options are where a driver's own
// credentials go when the typed config has no key for them.
func TestPasswordAndOptionTypeErrorsDoNotEchoTheValue(t *testing.T) {
	for _, value := range []any{
		1234.5678, // YAML's reading of an unquoted 1234.5678
		map[string]any{"nested": "example-secret-value"},
		[]any{"example-secret-value"},
	} {
		raw := validRaw()
		raw["password"] = value
		_, err := config.Parse(raw)
		require.Errorf(t, err, "password: %T must be refused", value)
		assert.Contains(t, err.Error(), "'password'")
		assert.Contains(t, err.Error(), "must be a string")
		assert.NotContains(t, err.Error(), "1234.5678")
		assert.NotContains(t, err.Error(), "example-secret-value")

		raw = validRaw()
		raw["options"] = map[string]any{"password": value}
		_, err = config.Parse(raw)
		require.Errorf(t, err, "options.password: %T must be refused", value)
		assert.Contains(t, err.Error(), "'options.password'")
		assert.Contains(t, err.Error(), "must be a scalar")
		assert.NotContains(t, err.Error(), "1234.5678")
		assert.NotContains(t, err.Error(), "example-secret-value")
	}
}

// TestAnUnquotedPasswordStillParses. The top-level password converts an unquoted int or bool
// as it did before its errors stopped echoing the value; refusing one would stop a working
// deploy over a value that reads back as written.
func TestAnUnquotedPasswordStillParses(t *testing.T) {
	for value, want := range map[any]string{123456: "123456", int64(654321): "654321", true: "true", "secret": "secret"} {
		raw := validRaw()
		raw["password"] = value
		cfg, err := config.Parse(raw)
		require.NoErrorf(t, err, "%T", value)
		assert.Equal(t, want, cfg.Password)
	}

	raw := validRaw()
	raw["options"] = map[string]any{"connect_timeout": 10, "application_name": "app", "unset": nil}
	cfg, err := config.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"connect_timeout": "10", "application_name": "app", "unset": ""}, cfg.Options)
}

// TestANonSecretAuthTypeErrorNamesTheQualifiedKey, so the operator can find it.
func TestANonSecretAuthTypeErrorNamesTheQualifiedKey(t *testing.T) {
	_, err := config.Parse(withAuth(map[string]any{"tenant_id": []string{"a"}}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'auth.tenant_id'")

	_, err = config.Parse(withAuth(map[string]any{"send_certificate_chain": "sometimes"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'auth.send_certificate_chain'")
	assert.Contains(t, err.Error(), "boolean")
}

func TestLoginTimeoutIsInSeconds(t *testing.T) {
	for _, value := range []any{30, "30", float64(30)} {
		cfg, err := config.Parse(withAuth(map[string]any{"login_timeout": value}))
		require.NoErrorf(t, err, "%T", value)
		assert.Equal(t, 30*time.Second, cfg.Auth.LoginTimeout)
	}
}

func TestParseRejectsNegativeLoginTimeout(t *testing.T) {
	_, err := config.Parse(withAuth(map[string]any{"login_timeout": -1}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "'auth.login_timeout'")
	assert.Contains(t, err.Error(), "negative")
}

// TestValidateRejectsNegativeLoginTimeout covers a DataSource built by hand, which never went
// through Parse.
func TestValidateRejectsNegativeLoginTimeout(t *testing.T) {
	cfg := config.DataSource{Host: "h", Database: "d", Auth: config.Auth{LoginTimeout: -time.Second}}

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.login_timeout")

	cfg.Auth.LoginTimeout = 0
	assert.NoError(t, cfg.Validate(), "zero leaves the engine's default in place")
}
