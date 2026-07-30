package v2

import (
	"testing"

	dsconfig "github.com/osbits/gorgany/v2/db/sql/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseConfig() dsconfig.DataSource {
	return dsconfig.DataSource{
		Driver:   "postgres_gorm",
		Host:     "localhost",
		Port:     5432,
		Username: "gorgany",
		Password: "secret",
		Database: "gorgany_test",
		SSL:      "disable",
	}
}

// TestBuildDSNMatchesLegacyOutputForValidConfig pins that a config which
// connected before v2 produces the same DSN.
func TestBuildDSNMatchesLegacyOutputForValidConfig(t *testing.T) {
	dsn, err := BuildDSN(baseConfig())
	require.NoError(t, err)
	assert.Equal(t,
		"host=localhost port=5432 user=gorgany password=secret dbname=gorgany_test sslmode=disable",
		dsn)
}

// TestBuildDSNCarriesSearchPath is the T1.4 headline. Without search_path,
// schema-per-test isolation is impossible and consumers are forced into
// CREATE DATABASE per test run plus truncate-between-tests.
func TestBuildDSNCarriesSearchPath(t *testing.T) {
	cfg := baseConfig()
	cfg.SearchPath = "tenant_a"

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Contains(t, dsn, "search_path=tenant_a")
}

func TestBuildDSNCarriesArbitraryOptionsSorted(t *testing.T) {
	cfg := baseConfig()
	cfg.Options = map[string]string{
		"connect_timeout":  "5",
		"application_name": "gorgany",
	}

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	// Sorted, so the DSN is byte-for-byte reproducible.
	assert.Equal(t,
		"host=localhost port=5432 user=gorgany password=secret dbname=gorgany_test "+
			"sslmode=disable application_name=gorgany connect_timeout=5",
		dsn)
}

// TestBuildDSNEscapesValues covers the injection/corruption the old
// fmt.Sprintf-based builder allowed: a password containing a space silently
// truncated the DSN, and one containing a quote corrupted it.
func TestBuildDSNEscapesValues(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     string
	}{
		{name: "space", password: "p a55", want: `password='p a55'`},
		{name: "single quote", password: "it's", want: `password='it\'s'`},
		{name: "backslash", password: `a\b`, want: `password='a\\b'`},
		{name: "tab", password: "a\tb", want: "password='a\tb'"},
		{
			name:     "smuggled second parameter",
			password: "x sslmode=disable",
			want:     `password='x sslmode=disable'`,
		},
		{name: "plain needs no quoting", password: "simple", want: "password=simple"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig()
			cfg.Password = tt.password

			dsn, err := BuildDSN(cfg)
			require.NoError(t, err)
			assert.Contains(t, dsn, tt.want)
		})
	}
}

// TestBuildDSNSmuggledParameterIsInert proves the escaped value cannot introduce
// a real second sslmode pair.
func TestBuildDSNSmuggledParameterIsInert(t *testing.T) {
	cfg := baseConfig()
	cfg.SSL = "verify-full"
	cfg.Password = "x sslmode=disable"

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Contains(t, dsn, "sslmode=verify-full")
	assert.NotContains(t, dsn, " sslmode=disable ")
	assert.Contains(t, dsn, `password='x sslmode=disable'`)
}

func TestBuildDSNOmitsEmptyValues(t *testing.T) {
	cfg := baseConfig()
	cfg.Password = ""
	cfg.SSL = ""
	cfg.Port = 0

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Equal(t, "host=localhost user=gorgany dbname=gorgany_test", dsn)
	assert.NotContains(t, dsn, "password=")
	assert.NotContains(t, dsn, "sslmode=")
	assert.NotContains(t, dsn, "port=")
}

func TestBuildDSNRejectsMalformedOptionKey(t *testing.T) {
	for _, key := range []string{"", "bad key", "a=b", "x'y"} {
		cfg := baseConfig()
		cfg.Options = map[string]string{key: "v"}

		_, err := BuildDSN(cfg)
		require.Errorf(t, err, "key %q must be rejected", key)
	}
}

// TestNewDataSourceReturnsErrorInsteadOfPanicking is the T1.3 regression at the
// datasource boundary: NewDataSource used to panic on a missing or mistyped key.
func TestNewDataSourceReturnsErrorInsteadOfPanicking(t *testing.T) {
	tests := []struct {
		name        string
		config      map[string]any
		wantInError string
	}{
		{
			name: "log key omitted entirely and host unreachable",
			config: map[string]any{
				"driver": "postgres_gorm",
				"host":   "127.0.0.1",
				"port":   1,
				"db":     "nope",
				"ssl":    "disable",
			},
			// Parsing and validation succeed; only the connection fails, and it
			// fails as an error rather than a nil-map panic.
			wantInError: "postgres:",
		},
		{
			name: "log mistyped",
			config: map[string]any{
				"driver": "postgres_gorm",
				"host":   "127.0.0.1",
				"db":     "nope",
				"log":    "definitely",
			},
			wantInError: "'log'",
		},
		{
			name: "missing host",
			config: map[string]any{
				"driver": "postgres_gorm",
				"db":     "nope",
			},
			wantInError: "'host' is required",
		},
		{
			name: "properties mistyped",
			config: map[string]any{
				"driver":     "postgres_gorm",
				"host":       "127.0.0.1",
				"db":         "nope",
				"properties": "lots",
			},
			wantInError: "'properties'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { _, err = NewDataSource(tt.config) })
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantInError)
		})
	}
}

func TestEscapeDSNValue(t *testing.T) {
	assert.Equal(t, "plain", escapeDSNValue("plain"))
	assert.Equal(t, `'a b'`, escapeDSNValue("a b"))
	assert.Equal(t, `'a\'b'`, escapeDSNValue("a'b"))
	assert.Equal(t, `'a\\b'`, escapeDSNValue(`a\b`))
	assert.Equal(t, "", escapeDSNValue(""))
}
