package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F6: rejecting an unrecognised key under `databases.<name>` is right — it is what turns a
// typo like `databse` into a boot failure instead of a setting silently ignored, which is
// the whole reason the check exists. But the message was a bare "unknown key(s) 'pool'",
// which does not say that the framework now owns this namespace.
//
// A real app carried an app-owned `pool:` block there *because* the framework's own
// `properties` path was dead before v2 — the camelCase lookups viper's lowercasing could
// never match. So v2 fixed `properties` and made the workaround fatal in the same release,
// and it is fatal at Register time, which a `go build && go vet` migration check passes
// straight over. The message is the only thing standing between the reader and a puzzling
// boot panic, so it has to be self-sufficient.

// parseWithKeys runs Parse over a minimal valid config plus the given extra keys.
func parseWithKeys(t *testing.T, extra map[string]any) error {
	t.Helper()

	raw := map[string]any{
		"driver": "postgres_gorm",
		"host":   "localhost",
		"port":   5432,
		"db":     "app",
	}
	for k, v := range extra {
		raw[k] = v
	}

	_, err := Parse(raw)
	return err
}

// TestAnAppOwnedNamespaceIsRefusedWithAWayOut is the reported case.
func TestAnAppOwnedNamespaceIsRefusedWithAWayOut(t *testing.T) {
	err := parseWithKeys(t, map[string]any{
		"pool": map[string]any{"maxOpen": 25},
	})

	require.Error(t, err)
	message := err.Error()

	assert.Contains(t, message, "'pool'", "the offending key must be named")
	assert.Contains(t, message, "properties",
		"and the escape hatch — properties is passed through untouched")
	assert.Contains(t, message, "databases.<name>",
		"and that moving it out entirely is the other option")
}

// TestTheRemedyIsPresentEvenWhenThereIsASuggestion. The two answer different questions: an
// app whose `pool` block holds its own settings needs to be told it can move them, not to
// rename them to `properties`.
func TestTheRemedyIsPresentEvenWhenThereIsASuggestion(t *testing.T) {
	err := parseWithKeys(t, map[string]any{"pool": map[string]any{"maxOpen": 25}})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "did you mean")
	assert.Contains(t, err.Error(), "move it under 'properties'")
}

// TestATypoGetsASuggestion — the case the check was added for.
func TestATypoGetsASuggestion(t *testing.T) {
	tests := map[string]string{
		"hosts":     "host",
		"usernam":   "username",
		"drive":     "driver",
		"properies": "properties",
		"log2":      "log",
	}

	for typo, want := range tests {
		t.Run(typo, func(t *testing.T) {
			err := parseWithKeys(t, map[string]any{typo: "x"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "did you mean '"+want+"'")
		})
	}
}

// TestATransposedTypoGetsASuggestion. Plain Levenshtein scores `prot` against `port` as 2,
// which a one-edit threshold rejects — and transposing two adjacent letters is among the
// most common typos there is, so the distance is Damerau-Levenshtein.
func TestATransposedTypoGetsASuggestion(t *testing.T) {
	for _, typo := range []string{"prot", "hsot", "dirver"} {
		nearest, ok := nearestKnownKey(typo)
		assert.Truef(t, ok, "%s should suggest something", typo)
		assert.NotEmptyf(t, nearest, "%s", typo)
	}
}

// TestVocabularyConfusionsGetASuggestion. These are not typos — someone arriving from a
// libpq connection string writes `sslmode` and `dbname`, someone from a MySQL DSN writes
// `user` and `pass`. Each is far enough from our spelling that no edit-distance threshold
// would match, and the reader is certain they got it right.
func TestVocabularyConfusionsGetASuggestion(t *testing.T) {
	tests := map[string]string{
		"sslmode":  "ssl",
		"dbname":   "db",
		"database": "db",
		"user":     "username",
		"pass":     "password",
		"hostname": "host",
		"schema":   "search_path",
		"params":   "options",
	}

	for written, want := range tests {
		t.Run(written, func(t *testing.T) {
			nearest, ok := nearestKnownKey(written)
			require.True(t, ok)
			assert.Equal(t, want, nearest)
		})
	}
}

// TestAzureVocabularyGetsASuggestion. Someone arriving from SQL Server writes what a
// go-mssqldb or ADO.NET connection string says — encrypt, server, fedauth, ApplicationIntent —
// and an Azure snippet puts the tenant and client at the top level. The setting exists here
// under another name, sometimes nested, and the suggestion has to say where.
func TestAzureVocabularyGetsASuggestion(t *testing.T) {
	tests := map[string]string{
		"encrypt":               "ssl",
		"tls":                   "ssl",
		"fedauth":               "auth",
		"authentication":        "auth",
		"auth_method":           "auth",
		"authentication_method": "auth",
		"tenant_id":             "auth.tenant_id",
		"client_id":             "auth.client_id",
		"client_secret":         "auth.client_secret",
		"applicationintent":     "read_only",
		"ApplicationIntent":     "read_only",
		"application_intent":    "read_only",
		"readonly":              "read_only",
		"instance_name":         "instance",
		"server":                "host",
		"unmanaged":             "external_schema",
		"schema_owner":          "external_schema",
		"lazy":                  "lazy_connect",
	}

	for written, want := range tests {
		t.Run(written, func(t *testing.T) {
			nearest, ok := nearestKnownKey(written)
			require.True(t, ok)
			assert.Equal(t, want, nearest)
		})
	}
}

// TestAMisleadingAzureSpellingGetsNoSuggestion. Each of these would be renamed as suggested
// and keep its value, and the result would not mean what the reader meant. `managed: false`
// says gorgany does not manage the schema, and `external_schema: false` says it does. And
// TrustServerCertificate has no key on any engine in this build: under `options` Postgres and
// MySQL would forward it to a server that refuses it at connect time, which is a worse failure
// than the unknown key at boot.
func TestAMisleadingAzureSpellingGetsNoSuggestion(t *testing.T) {
	for _, key := range []string{"managed", "Managed", "trustservercertificate", "TrustServerCertificate", "trust_server_certificate"} {
		t.Run(key, func(t *testing.T) {
			nearest, ok := nearestKnownKey(key)
			assert.Falsef(t, ok, "%s should not suggest %q", key, nearest)

			err := parseWithKeys(t, map[string]any{key: false})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "did you mean")
			assert.Contains(t, err.Error(), "recognised keys are", "the reader still gets the list to choose from")
		})
	}
}

// TestANestedSuggestionNamesTheNestedKey, through the error a reader actually sees.
func TestANestedSuggestionNamesTheNestedKey(t *testing.T) {
	err := parseWithKeys(t, map[string]any{"tenant_id": "00000000-0000-0000-0000-000000000000"})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "did you mean 'auth.tenant_id' instead of 'tenant_id'?")
	assert.NotContains(t, err.Error(), "00000000-0000-0000-0000-000000000000",
		"the unknown-key error names keys, never values")
}

// TestTheNewKeysCatchTheirOwnTypos, by distance like every other key.
func TestTheNewKeysCatchTheirOwnTypos(t *testing.T) {
	tests := map[string]string{
		"instnace":       "instance",
		"external_schem": "external_schema",
		"read_onyl":      "read_only",
		"lazy_conect":    "lazy_connect",
		"auht":           "auth",
	}

	for typo, want := range tests {
		t.Run(typo, func(t *testing.T) {
			nearest, ok := nearestKnownKey(typo)
			require.True(t, ok)
			assert.Equal(t, want, nearest)
		})
	}
}

// TestAnUnrelatedKeyGetsNoSuggestion. Suggesting on distance alone would be worse than
// saying nothing: it reads as authoritative and sends the reader to rename a key that was
// never meant to be one of ours.
func TestAnUnrelatedKeyGetsNoSuggestion(t *testing.T) {
	for _, key := range []string{"maxOpen", "tenant", "cache", "featureFlags", "x"} {
		nearest, ok := nearestKnownKey(key)
		assert.Falsef(t, ok, "%s should not suggest %q", key, nearest)
	}
}

// TestEveryUnknownKeyIsNamed, so a config with two mistakes needs one boot to find both.
func TestEveryUnknownKeyIsNamed(t *testing.T) {
	err := parseWithKeys(t, map[string]any{"pool": 1, "cache": 2})
	require.Error(t, err)

	assert.Contains(t, err.Error(), "'cache'")
	assert.Contains(t, err.Error(), "'pool'")
}

// TestTheKeysAreListedInAStableOrder: an error message that reorders itself between runs is
// hard to grep for and hard to test against.
func TestTheKeysAreListedInAStableOrder(t *testing.T) {
	first := parseWithKeys(t, map[string]any{"zeta": 1, "alpha": 2})
	require.Error(t, first)

	for i := 0; i < 20; i++ {
		again := parseWithKeys(t, map[string]any{"zeta": 1, "alpha": 2})
		require.Error(t, again)
		require.Equal(t, first.Error(), again.Error())
	}

	assert.Less(t,
		strings.Index(first.Error(), "'alpha'"),
		strings.Index(first.Error(), "'zeta'"),
		"unknown keys are listed alphabetically")
}

// TestAValidConfigIsStillAccepted, including every recognised key at once — the guard
// against a knownKeys or knownAuthKeys entry being dropped.
func TestAValidConfigIsStillAccepted(t *testing.T) {
	_, err := Parse(map[string]any{
		"driver": "postgres_gorm", "host": "localhost", "port": 5432,
		"username": "u", "password": "p", "db": "app", "ssl": "disable",
		"search_path": "tenant", "options": map[string]any{"application_name": "app"},
		"prefer_simple_protocol": true, "log": false,
		"properties":              map[string]any{"maxOpenConnections": 5},
		"allow_unfaithful_upsert": false,
		"instance":                "legacy",
		"external_schema":         true,
		"read_only":               true,
		"lazy_connect":            true,
		"auth": map[string]any{
			"method": "service_principal", "tenant_id": "t", "client_id": "c",
			"client_secret": "s", "certificate_path": "/p", "certificate_password": "pw",
			"send_certificate_chain": true, "resource_id": "r", "object_id": "o",
			"token_file_path": "/f", "redirect_url": "http://localhost:8400",
			"scope": "https://database.windows.net/.default", "login_timeout": 60,
		},
	})
	assert.NoError(t, err)
}

// TestEditDistance pins the metric, since the suggestion threshold is expressed in it.
func TestEditDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"port", "port", 0},
		{"prot", "port", 1}, // transposition — 2 under plain Levenshtein
		{"hosts", "host", 1},
		{"host", "hosts", 1},
		{"drive", "driver", 1},
		{"pool", "db", 4},
		{"", "db", 2},
		{"db", "", 2},
	}

	for _, tc := range tests {
		assert.Equalf(t, tc.want, editDistance(tc.a, tc.b), "editDistance(%q, %q)", tc.a, tc.b)
	}
}
