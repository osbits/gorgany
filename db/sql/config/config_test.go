package config_test

import (
	"testing"
	"time"

	"github.com/osbits/gorgany/db/sql/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validRaw() map[string]any {
	return map[string]any{
		"driver":   "postgres_gorm",
		"host":     "localhost",
		"port":     5432,
		"username": "gorgany",
		"password": "secret",
		"db":       "gorgany_test",
		"ssl":      "disable",
		"log":      false,
	}
}

func TestParseFullConfig(t *testing.T) {
	raw := validRaw()
	raw["search_path"] = "tenant_a"
	raw["prefer_simple_protocol"] = true
	raw["log"] = true
	raw["options"] = map[string]any{"application_name": "gorgany", "connect_timeout": 5}
	raw["properties"] = map[string]any{
		"maxOpenConnections":        25,
		"maxIdleConnections":        5,
		"connectionMaxLifetime":     300,
		"connectionMaxIdleLifetime": 60,
	}

	cfg, err := config.Parse(raw)
	require.NoError(t, err)

	assert.Equal(t, "postgres_gorm", cfg.Driver)
	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 5432, cfg.Port)
	assert.Equal(t, "gorgany", cfg.Username)
	assert.Equal(t, "secret", cfg.Password)
	assert.Equal(t, "gorgany_test", cfg.Database)
	assert.Equal(t, "disable", cfg.SSL)
	assert.Equal(t, "tenant_a", cfg.SearchPath)
	assert.True(t, cfg.PreferSimpleProtocol)
	assert.True(t, cfg.Log)
	assert.Equal(t, map[string]string{"application_name": "gorgany", "connect_timeout": "5"}, cfg.Options)
	assert.Equal(t, 25, cfg.Pool.MaxOpenConnections)
	assert.Equal(t, 5, cfg.Pool.MaxIdleConnections)
	assert.Equal(t, 300*time.Second, cfg.Pool.ConnectionMaxLifetime)
	assert.Equal(t, 60*time.Second, cfg.Pool.ConnectionMaxIdleLifetime)
	require.NoError(t, cfg.Validate())
}

// TestParseToleratesOmittedOptionalKeys is the T1.3 headline: a hand-built config
// map that omits `log`, `prefer_simple_protocol` or `properties` used to panic on
// an unchecked type assertion. It must now simply parse.
func TestParseToleratesOmittedOptionalKeys(t *testing.T) {
	minimal := map[string]any{
		"driver": "postgres_gorm",
		"host":   "localhost",
		"port":   5432,
		"db":     "gorgany_test",
	}

	require.NotPanics(t, func() {
		cfg, err := config.Parse(minimal)
		require.NoError(t, err)
		assert.False(t, cfg.Log)
		assert.False(t, cfg.PreferSimpleProtocol)
		assert.Zero(t, cfg.Pool.MaxOpenConnections)
		require.NoError(t, cfg.Validate())
	})
}

// TestParseNamesTheOffendingKeyAndType is the other half of T1.3: a mistyped key
// must produce a descriptive error, not a panic.
func TestParseNamesTheOffendingKeyAndType(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(map[string]any)
		wantInError []string
	}{
		{
			name:        "log is not a boolean",
			mutate:      func(m map[string]any) { m["log"] = 7 },
			wantInError: []string{"'log'", "boolean", "int"},
		},
		{
			name:        "port is not an integer",
			mutate:      func(m map[string]any) { m["port"] = "not-a-port" },
			wantInError: []string{"'port'", "integer", "not-a-port"},
		},
		{
			name:        "host is not a string",
			mutate:      func(m map[string]any) { m["host"] = []string{"a"} },
			wantInError: []string{"'host'", "string"},
		},
		{
			name:        "prefer_simple_protocol is not a boolean",
			mutate:      func(m map[string]any) { m["prefer_simple_protocol"] = "yepp" },
			wantInError: []string{"'prefer_simple_protocol'", "boolean"},
		},
		{
			name:        "properties is not a map",
			mutate:      func(m map[string]any) { m["properties"] = 42 },
			wantInError: []string{"'properties'", "map", "int"},
		},
		{
			name: "maxOpenConnections is not an integer",
			mutate: func(m map[string]any) {
				m["properties"] = map[string]any{"maxOpenConnections": "many"}
			},
			wantInError: []string{"'properties.maxOpenConnections'", "integer"},
		},
		{
			name:        "unknown top-level key",
			mutate:      func(m map[string]any) { m["hostname"] = "localhost" },
			wantInError: []string{"unknown key", "'hostname'"},
		},
		{
			name: "unknown properties key",
			mutate: func(m map[string]any) {
				m["properties"] = map[string]any{"maxOpenConnection": 5}
			},
			wantInError: []string{"unknown key", "'maxOpenConnection'", "'properties'"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := validRaw()
			tt.mutate(raw)

			var err error
			require.NotPanics(t, func() { _, err = config.Parse(raw) })

			require.Error(t, err)
			for _, want := range tt.wantInError {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// TestParseAcceptsViperNumberAndStringForms covers the shapes Viper actually
// produces: YAML gives int, an environment variable gives string, and a decoded
// JSON map gives float64.
func TestParseAcceptsViperNumberAndStringForms(t *testing.T) {
	for _, port := range []any{5432, int64(5432), float64(5432), "5432"} {
		raw := validRaw()
		raw["port"] = port

		cfg, err := config.Parse(raw)
		require.NoErrorf(t, err, "port form %T", port)
		assert.Equal(t, 5432, cfg.Port)
	}

	for _, log := range []any{true, "true"} {
		raw := validRaw()
		raw["log"] = log

		cfg, err := config.Parse(raw)
		require.NoErrorf(t, err, "log form %T", log)
		assert.True(t, cfg.Log)
	}
}

func TestParseRejectsNilConfig(t *testing.T) {
	_, err := config.Parse(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestParseAcceptsMapAnyAnyProperties(t *testing.T) {
	raw := validRaw()
	raw["properties"] = map[any]any{"maxOpenConnections": 10}

	cfg, err := config.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, 10, cfg.Pool.MaxOpenConnections)
}

func TestValidateRejectsStructuralProblems(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.DataSource
		wantInError string
	}{
		{
			name:        "missing host",
			cfg:         config.DataSource{Database: "d"},
			wantInError: "'host' is required",
		},
		{
			name:        "missing db",
			cfg:         config.DataSource{Host: "h"},
			wantInError: "'db' is required",
		},
		{
			name:        "port out of range",
			cfg:         config.DataSource{Host: "h", Database: "d", Port: 70000},
			wantInError: "'port'",
		},
		{
			name: "negative pool size",
			cfg: config.DataSource{Host: "h", Database: "d",
				Pool: config.Pool{MaxOpenConnections: -1}},
			wantInError: "maxOpenConnections",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantInError)
		})
	}
}

func TestSortedOptionKeys(t *testing.T) {
	cfg := config.DataSource{Options: map[string]string{"zeta": "1", "alpha": "2", "mid": "3"}}
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, cfg.SortedOptionKeys())
}
