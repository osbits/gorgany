package config

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A datasource's client secret and certificate password are blanked like any other key when
// their placeholder is unset, and a blank one is not missing to the engine that reads it: an
// empty client_secret means "no secret configured" and an empty certificate_password means
// "not encrypted", so the failure that follows points at the wrong thing, and with
// lazy_connect it arrives on the first request instead of at deploy. These tests pin that the
// boot stops instead, and that nothing else changed on the way.

// datasourceSecretKeys are the keys the pattern matcher must catch, under a stand-in
// datasource name.
var datasourceSecretKeys = []string{
	"databases.legacy.auth.client_secret",
	"databases.legacy.auth.certificate_password",
}

func TestAnUnsetDatabaseSecretPlaceholderStopsTheBoot(t *testing.T) {
	for _, key := range datasourceSecretKeys {
		t.Run(key, func(t *testing.T) {
			loadYAML(t, buildNestedYAML(key, "${DEFINITELY_NOT_SET_ANYWHERE}"))

			err := ResolveEnvPlaceholders()

			require.Error(t, err, "%s must not be blanked silently", key)
			assert.Contains(t, err.Error(), key)
			assert.Contains(t, err.Error(), "DEFINITELY_NOT_SET_ANYWHERE",
				"the operator needs to be told which variable to set")

			assert.Equal(t, "${DEFINITELY_NOT_SET_ANYWHERE}", viper.GetString(key),
				"and it is not substituted, so a caller that ignored the error reads no usable blank")
		})
	}
}

// TestTheOptOutDoesNotReleaseASecret: KeepUnresolvedLiterals governs what happens to a key the
// boot survives, and a secret's is not one.
func TestTheOptOutDoesNotReleaseASecret(t *testing.T) {
	loadYAML(t, buildNestedYAML(datasourceSecretKeys[0], "${DEFINITELY_NOT_SET_ANYWHERE}"))

	require.Error(t, ResolveEnvPlaceholders(KeepUnresolvedLiterals()))
}

func TestASetDatabaseSecretPlaceholderIsSubstituted(t *testing.T) {
	t.Setenv("GORGANY_TEST_CLIENT_SECRET", "example-client-secret")
	t.Setenv("GORGANY_TEST_CERT_PASSWORD", "example-certificate-password")

	loadYAML(t, `
databases:
  legacy:
    host: example.database.windows.net
    auth:
      method: service_principal
      client_secret: ${GORGANY_TEST_CLIENT_SECRET}
      certificate_password: ${GORGANY_TEST_CERT_PASSWORD}
`)

	require.NoError(t, ResolveEnvPlaceholders())

	assert.Equal(t, "example-client-secret", viper.GetString("databases.legacy.auth.client_secret"))
	assert.Equal(t, "example-certificate-password",
		viper.GetString("databases.legacy.auth.certificate_password"))
	assert.Equal(t, "service_principal", viper.GetString("databases.legacy.auth.method"),
		"and the merge leaves the siblings alone")
	assert.Equal(t, "example.database.windows.net", viper.GetString("databases.legacy.host"))
}

// TestAnUnsetDatabasePasswordPlaceholderIsStillOnlyAWarning. A blank password is an ordinary
// sign-in failure that names the login; it keeps the blank-and-warn behaviour every other key
// has, and an app that relied on booting without one keeps booting.
func TestAnUnsetDatabasePasswordPlaceholderIsStillOnlyAWarning(t *testing.T) {
	read := captureWarnings(t)

	loadYAML(t, `
databases:
  legacy:
    password: ${DEFINITELY_NOT_SET_ANYWHERE}
`)

	require.NoError(t, ResolveEnvPlaceholders())
	assert.Equal(t, "", viper.GetString("databases.legacy.password"))

	warnings := read()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "databases.legacy.password")
}

// TestTheSecretErrorGivesSecretAdviceNotJwtAdvice. The security-key advice ends either with
// "remove the placeholder so the framework's secure default applies", which is false for a
// credential, or with a sentence about an empty signing key, which a client secret is not.
func TestTheSecretErrorGivesSecretAdviceNotJwtAdvice(t *testing.T) {
	loadYAML(t, buildNestedYAML(datasourceSecretKeys[0], "${DEFINITELY_NOT_SET_ANYWHERE}"))

	err := ResolveEnvPlaceholders()
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, "no secure fallback")
	assert.Contains(t, message, "the variable has to be set")
	assert.NotContains(t, message, "secure default applies",
		"there is no default for a credential to fall back to")
	assert.NotContains(t, message, "remove the placeholder")
	assert.NotContains(t, message, "signing key", "that is the JWT advice")
	assert.NotContains(t, message, "tokens anyone can mint")
}

// TestSecretPatternsMatchExactlyOneSegmentPerWildcard. The wildcard is the datasource's name,
// one segment; a looser match would stop the boot over keys the framework never reads.
func TestSecretPatternsMatchExactlyOneSegmentPerWildcard(t *testing.T) {
	matches := []string{
		"databases.legacy.auth.client_secret",
		"databases.default.auth.certificate_password",
		"DATABASES.Legacy.Auth.Client_Secret", // compared ignoring case, as isSecurityRelevant is
	}
	for _, key := range matches {
		assert.Truef(t, isSecretKey(key), "%s is a datasource credential", key)
	}

	misses := []string{
		"databases.auth.client_secret",              // no datasource name
		"databases..auth.client_secret",             // an empty one
		"databases.a.b.auth.client_secret",          // two segments where the wildcard allows one
		"databases.legacy.auth.client_secret.value", // a segment too many
		"databases.legacy.auth.client_secret_hint",  // not the whole segment
		"databases.legacy.client_secret",
		"databases.legacy.password", // blank-and-warn, deliberately
		"databases.legacy.auth.client_id",
		"integrations.crm.auth.client_secret", // an app's own key
		"auth.jwt.secret",                     // security-relevant, but not by this rule
	}
	for _, key := range misses {
		assert.Falsef(t, isSecretKey(key), "%s is not matched", key)
	}
}

// TestSecretAndSecurityFailuresAreReportedTogether, so one failed deploy names everything
// missing rather than one thing per attempt — and each group keeps the advice that is true for
// it.
func TestSecretAndSecurityFailuresAreReportedTogether(t *testing.T) {
	loadYAML(t, `
auth:
  jwt:
    secret: ${DEFINITELY_NOT_SET_JWT}
databases:
  legacy:
    auth:
      client_secret: ${DEFINITELY_NOT_SET_CLIENT_SECRET}
`)

	err := ResolveEnvPlaceholders()
	require.Error(t, err)

	message := err.Error()
	assert.Contains(t, message, "auth.jwt.secret (${DEFINITELY_NOT_SET_JWT})")
	assert.Contains(t, message, "databases.legacy.auth.client_secret (${DEFINITELY_NOT_SET_CLIENT_SECRET})")
	assert.Contains(t, message, "security-relevant key(s)")
	assert.Contains(t, message, "datasource credential(s)")
	assert.Contains(t, message, "; ", "the two reports are joined into one error")
	assert.Contains(t, message, "signing key", "the JWT key keeps its own advice")
}

// TestTheExportedSecurityListsAreUnchanged. They are public API, and the datasource patterns
// were kept out of them on purpose: their entries are exact keys, and an app that reads them
// must not start finding wildcards there.
func TestTheExportedSecurityListsAreUnchanged(t *testing.T) {
	assert.Equal(t, []string{"auth.jwt.secret", "auth.session.cookie.secure"}, SecurityRelevantKeys)
	assert.Equal(t, []string{"auth.jwt.secret"}, KeysWithoutSecureFallback)
}
