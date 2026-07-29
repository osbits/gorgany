package v2

import (
	"testing"

	dsconfig "github.com/osbits/gorgany/db/sql/config"
	dbCore "github.com/osbits/gorgany/db/sql/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mysqlConfig() dsconfig.DataSource {
	return dsconfig.DataSource{
		Driver:   "mysql_gorm",
		Host:     "localhost",
		Port:     3307,
		Username: "root",
		Password: "test",
		Database: "gorgany_test",
	}
}

func TestBuildDSNDefaultsToUtf8mb4AndParseTime(t *testing.T) {
	dsn, err := BuildDSN(mysqlConfig())
	require.NoError(t, err)

	// parseTime is mandatory: without it the driver hands DATETIME back as []byte
	// and every time.Time field in every model fails to scan.
	assert.Equal(t,
		"root:test@tcp(localhost:3307)/gorgany_test?"+
			"charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=true",
		dsn)
}

func TestBuildDSNAppliesDefaultPort(t *testing.T) {
	cfg := mysqlConfig()
	cfg.Port = 0

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Contains(t, dsn, "tcp(localhost:3306)")
}

func TestBuildDSNOmitsAuthWhenNoUsername(t *testing.T) {
	cfg := mysqlConfig()
	cfg.Username = ""
	cfg.Password = ""

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Equal(t,
		"tcp(localhost:3307)/gorgany_test?charset=utf8mb4&collation=utf8mb4_unicode_ci&parseTime=true",
		dsn)
}

func TestBuildDSNMapsSSLToTLS(t *testing.T) {
	cfg := mysqlConfig()
	cfg.SSL = "skip-verify"

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Contains(t, dsn, "tls=skip-verify")
}

func TestBuildDSNOptionsOverrideDefaultsAndAreSorted(t *testing.T) {
	cfg := mysqlConfig()
	cfg.Options = map[string]string{
		"collation":      "utf8mb4_bin",
		"readTimeout":    "10s",
		"connectTimeout": "5s",
	}

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Equal(t,
		"root:test@tcp(localhost:3307)/gorgany_test?"+
			"charset=utf8mb4&collation=utf8mb4_bin&connectTimeout=5s&parseTime=true&readTimeout=10s",
		dsn)
}

// TestBuildDSNEscapesParameterValues proves a value containing '&' or '=' cannot
// smuggle in a second DSN parameter.
func TestBuildDSNEscapesParameterValues(t *testing.T) {
	cfg := mysqlConfig()
	cfg.Options = map[string]string{"loc": "Local&tls=false"}

	dsn, err := BuildDSN(cfg)
	require.NoError(t, err)
	assert.Contains(t, dsn, "loc=Local%26tls%3Dfalse")
	assert.NotContains(t, dsn, "&tls=false")
}

func TestBuildDSNRejectsMalformedOptionKey(t *testing.T) {
	for _, key := range []string{"", "bad key", "a&b", "a=b"} {
		cfg := mysqlConfig()
		cfg.Options = map[string]string{key: "v"}

		_, err := BuildDSN(cfg)
		require.Errorf(t, err, "key %q must be rejected", key)
	}
}

// TestBuildDSNRejectsSearchPath: MySQL has no schema search path — a schema *is*
// a database — so honouring it is impossible and ignoring it would silently
// connect to the wrong place.
func TestBuildDSNRejectsSearchPath(t *testing.T) {
	cfg := mysqlConfig()
	cfg.SearchPath = "tenant_a"

	dsn, err := BuildDSN(cfg)

	require.Error(t, err)
	assert.True(t, dbCore.IsUnsupported(err))
	assert.Contains(t, err.Error(), "search_path")
	assert.Contains(t, err.Error(), "'db'")
	assert.Empty(t, dsn)
}

// TestNewDataSourceReturnsErrorInsteadOfPanicking mirrors the Postgres guarantee:
// a hand-built config map never takes the process down.
func TestNewDataSourceReturnsErrorInsteadOfPanicking(t *testing.T) {
	tests := []struct {
		name        string
		config      map[string]any
		wantInError string
	}{
		{
			name:        "missing host",
			config:      map[string]any{"driver": "mysql_gorm", "db": "d"},
			wantInError: "'host' is required",
		},
		{
			name:        "log mistyped",
			config:      map[string]any{"driver": "mysql_gorm", "host": "h", "db": "d", "log": 3},
			wantInError: "'log'",
		},
		{
			name: "search_path rejected",
			config: map[string]any{
				"driver": "mysql_gorm", "host": "h", "db": "d", "search_path": "s",
			},
			wantInError: "search_path",
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

func TestDataSourceExposesMySQLDialect(t *testing.T) {
	ds := &gormMySQLDataSource{}
	assert.Equal(t, "mysql", ds.Dialect().Name())
}

// TestSessionQueryReturnsFreshMySQLBuilder pins T2.3 for the MySQL session too.
func TestSessionQueryReturnsFreshMySQLBuilder(t *testing.T) {
	s := &sessionImpl{dialect: &MySQLDialect{}}

	first := s.Query()
	second := s.Query()
	assert.NotSame(t, first, second)
	assert.Equal(t, "mysql", first.Dialect().Name())

	_, _, err := first.Select("id").From("a").Eq("x", 1).ToSQL()
	require.NoError(t, err)

	sql, args, err := second.Select("id").From("b").ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT id FROM b", sql)
	assert.Empty(t, args)
}

func TestTransactionQueryReturnsFreshMySQLBuilder(t *testing.T) {
	tx := &transactionImpl{Builder: NewBuilder(), dialect: &MySQLDialect{}}

	assert.NotSame(t, tx.Query(), tx.Query())
	assert.Equal(t, "mysql", tx.Query().Dialect().Name())
}
