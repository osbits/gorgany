package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/osbits/gorgany/db/sql/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests read config through Viper rather than hand-building a map, because
// that is where the real bug lived and why the hand-built tests missed it.
//
// Viper lowercases every key it reads. A YAML `maxOpenConnections` therefore
// arrives as `maxopenconnections`, so any code comparing against the camelCase
// literal never matches. That had two consequences:
//
//   - All four pool settings were silently ignored on every version, including
//     v1.5.1, whose datasource did `props["maxOpenConnections"]` against the same
//     lowercased map.
//   - The strict unknown-key check added in v2 briefly turned that silence into a
//     hard boot failure: "unknown key(s) 'connectionmaxidlelifetime',
//     'connectionmaxlifetime', 'maxidleconnections', 'maxopenconnections' under
//     'properties'".
//
// Every test here goes through the same path a real app does.

// parseFromYAML reads yaml through Viper and parses `databases.default`, exactly as
// DbProvider does at boot.
func parseFromYAML(t *testing.T, yaml string) (config.DataSource, error) {
	t.Helper()

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(yaml)))

	databases := v.GetStringMap("databases")
	require.Contains(t, databases, "default")

	raw, ok := databases["default"].(map[string]any)
	require.True(t, ok)

	return config.Parse(raw)
}

const fixtureShapedYAML = `
databases:
  default:
    driver: postgres_gorm
    host: localhost
    port: 5432
    username: gorgany
    password: gorgany
    db: gorgany_e2e
    ssl: disable
    prefer_simple_protocol: true
    log: false
    properties:
      maxOpenConnections: 5
      maxIdleConnections: 4
      connectionMaxLifetime: 300
      connectionMaxIdleLifetime: 200
`

// TestCamelCasePoolKeysFromYAMLAreParsed is the regression. This exact config shape
// is what e2e/fixture-app ships, and it failed to boot until the fold was added.
func TestCamelCasePoolKeysFromYAMLAreParsed(t *testing.T) {
	cfg, err := parseFromYAML(t, fixtureShapedYAML)

	require.NoError(t, err, "a camelCase `properties` block from YAML must parse")

	assert.Equal(t, 5, cfg.Pool.MaxOpenConnections,
		"pool settings from YAML were silently dropped before this fix")
	assert.Equal(t, 4, cfg.Pool.MaxIdleConnections)
	assert.Equal(t, 300*time.Second, cfg.Pool.ConnectionMaxLifetime)
	assert.Equal(t, 200*time.Second, cfg.Pool.ConnectionMaxIdleLifetime)

	// And the rest of the entry still parses.
	assert.Equal(t, "postgres_gorm", cfg.Driver)
	assert.Equal(t, "gorgany_e2e", cfg.Database)
	assert.True(t, cfg.PreferSimpleProtocol)
	assert.False(t, cfg.Log)
	require.NoError(t, cfg.Validate())
}

// TestPoolKeysAreCaseInsensitive covers every spelling a user might write.
func TestPoolKeysAreCaseInsensitive(t *testing.T) {
	for _, spelling := range []string{
		"maxOpenConnections", "maxopenconnections", "MaxOpenConnections", "MAXOPENCONNECTIONS",
	} {
		t.Run(spelling, func(t *testing.T) {
			cfg, err := parseFromYAML(t, `
databases:
  default:
    driver: postgres_gorm
    host: localhost
    db: d
    properties:
      `+spelling+`: 7
`)
			require.NoError(t, err)
			assert.Equal(t, 7, cfg.Pool.MaxOpenConnections)
		})
	}
}

// TestTopLevelKeysAreCaseInsensitive — the same fold applies above `properties`.
func TestTopLevelKeysAreCaseInsensitive(t *testing.T) {
	cfg, err := parseFromYAML(t, `
databases:
  default:
    Driver: postgres_gorm
    HOST: localhost
    Db: mydb
    Search_Path: tenant_a
`)
	require.NoError(t, err)
	assert.Equal(t, "postgres_gorm", cfg.Driver)
	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, "mydb", cfg.Database)
	assert.Equal(t, "tenant_a", cfg.SearchPath)
}

// TestAGenuineTypoIsStillReported: folding case must not weaken the typo check that
// the fold was introduced to stop misfiring.
func TestAGenuineTypoIsStillReported(t *testing.T) {
	_, err := parseFromYAML(t, `
databases:
  default:
    driver: postgres_gorm
    host: localhost
    db: d
    properties:
      maxOpenConnection: 5
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key")
	// Viper lowercased it on the way in, so that is the spelling available to report.
	assert.Contains(t, err.Error(), "maxopenconnection")
}

func TestATopLevelTypoIsStillReported(t *testing.T) {
	_, err := parseFromYAML(t, `
databases:
  default:
    driver: postgres_gorm
    hostname: localhost
    db: d
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key")
	assert.Contains(t, err.Error(), "hostname")
}

// TestMistypedPoolValueNamesTheCamelCaseSpelling: the user wrote camelCase, so the
// error must say camelCase even though matching happens in lowercase.
func TestMistypedPoolValueNamesTheCamelCaseSpelling(t *testing.T) {
	_, err := parseFromYAML(t, `
databases:
  default:
    driver: postgres_gorm
    host: localhost
    db: d
    properties:
      maxOpenConnections: "lots"
`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "properties.maxOpenConnections",
		"the error must use the spelling the user wrote, not the folded one")
	assert.Contains(t, err.Error(), "integer")
}

// TestOptionsKeysArePreservedVerbatim — option keys are handed to the driver, so
// Parse must not fold them. Viper will already have lowercased anything from YAML;
// the per-driver DSN builder restores canonical spellings where it matters.
func TestOptionsKeysArePreservedVerbatim(t *testing.T) {
	cfg, err := config.Parse(map[string]any{
		"driver":  "mysql_gorm",
		"host":    "localhost",
		"db":      "d",
		"options": map[string]any{"readTimeout": "10s", "application_name": "app"},
	})
	require.NoError(t, err)

	assert.Equal(t, "10s", cfg.Options["readTimeout"],
		"a hand-built option key must reach the driver unchanged")
	assert.Equal(t, "app", cfg.Options["application_name"])
}

// TestDuplicateKeysInDifferentCasesAreDeterministic guards the fold against
// nondeterminism: two spellings of one key must resolve the same way every time.
func TestDuplicateKeysInDifferentCasesAreDeterministic(t *testing.T) {
	raw := map[string]any{
		"driver": "postgres_gorm",
		"host":   "localhost",
		"db":     "d",
		"properties": map[string]any{
			"maxOpenConnections": 1,
			"MaxOpenConnections": 2,
		},
	}

	first, err := config.Parse(raw)
	require.NoError(t, err)

	for i := 0; i < 25; i++ {
		again, err := config.Parse(raw)
		require.NoError(t, err)
		assert.Equal(t, first.Pool.MaxOpenConnections, again.Pool.MaxOpenConnections,
			"colliding key cases must resolve deterministically")
	}
}
