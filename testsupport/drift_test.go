package testsupport

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTheSQLServerTestSettingsAgreeEverywhere pins the SQL Server container's port, user, sa
// password and image across every place that repeats them: CI's service, the compose file, the
// e2e suite's defaults, the harness's defaults and docs/TESTING.md, both its defaults table and
// its multi-engine Configure example.
//
// Each copy is written by hand, and a copy that drifts fails far from its cause. A password
// changed in CI alone leaves the container refusing every login, which the suites report as an
// engine that never came up; a port changed in the docs alone sends a developer's container
// where no default looks; and an image bumped in one file alone runs the suites against two
// versions of SQL Server while each claims to test the one the others name. The compose file
// publishes no host port, so it is checked for the password and the image only.
//
// The harness's own defaults, defaultPortFor, defaultUserFor and defaultPasswordFor, are the
// reference the others are compared with, and testsupport's live suite reads them rather than
// repeating them.
func TestTheSQLServerTestSettingsAgreeEverywhere(t *testing.T) {
	port := strconv.Itoa(defaultPortFor(DriverSQLServer))
	user := defaultUserFor(DriverSQLServer)
	password := defaultPasswordFor(DriverSQLServer)

	ci := readRepoFile(t, ".github/workflows/ci.yml")
	compose := readRepoFile(t, "e2e/docker-compose.yml")
	e2e := readRepoFile(t, "e2e/tests/live_sqlserver_test.go")
	docs := readRepoFile(t, "docs/TESTING.md")

	// The SQL Server entry of TESTING.md's Configure example, which spells out every setting.
	example := regexp.MustCompile(`\{Name: "sqlserver", Driver: testsupport\.DriverSQLServer,[^}]*\}`).FindString(docs)
	require.NotEmpty(t, example, "TESTING.md's Configure example has no SQL Server entry")

	t.Run("image", func(t *testing.T) {
		image := regexp.MustCompile(`mcr\.microsoft\.com/mssql/server:([A-Za-z0-9._-]+)`)
		var tags []string
		for name, content := range map[string]string{
			"ci.yml": ci, "docker-compose.yml": compose, "live_sqlserver_test.go": e2e, "TESTING.md": docs,
		} {
			found := allSubmatches(image, content)
			require.NotEmpty(t, found, "%s names no SQL Server image", name)
			tags = append(tags, found...)
		}
		for _, tag := range tags {
			assert.Equal(t, tags[0], tag, "every file must name the same SQL Server image")
			assert.NotContains(t, tag, "latest", "the image is pinned to a cumulative update, so a new "+
				"one reaches the suites only in a commit that says so")
		}
	})

	t.Run("password", func(t *testing.T) {
		saPassword := regexp.MustCompile(`MSSQL_SA_PASSWORD[=:]\s*"?([^"\s]+)"?`)
		for name, content := range map[string]string{
			"ci.yml": ci, "docker-compose.yml": compose, "live_sqlserver_test.go": e2e, "TESTING.md": docs,
		} {
			found := allSubmatches(saPassword, content)
			require.NotEmpty(t, found, "%s starts no SQL Server container with a password", name)
			for _, value := range found {
				assert.Equal(t, password, value, "the sa password in %s", name)
			}
		}

		healthCheck := allSubmatches(regexp.MustCompile(`sqlcmd [^"\n]*-P (\S+)`), ci)
		require.NotEmpty(t, healthCheck, "ci.yml's SQL Server health check signs in with no password")
		for _, value := range healthCheck {
			assert.Equal(t, password, value, "the password ci.yml's health check signs in with")
		}
		assert.Equal(t, []string{password},
			allSubmatches(regexp.MustCompile(`mssqlPassword\s*=\s*"([^"]+)"`), e2e),
			"the password the e2e suite signs in with")

		assert.Contains(t, defaultsRow(t, docs, EnvPassword), "`"+password+"` for SQL Server",
			"the defaults table in TESTING.md")
		assert.Equal(t, []string{password}, allSubmatches(regexp.MustCompile(`Password: "([^"]+)"`), example),
			"the password in TESTING.md's Configure example")
	})

	t.Run("user", func(t *testing.T) {
		for name, content := range map[string]string{"ci.yml": ci, "docker-compose.yml": compose} {
			found := allSubmatches(regexp.MustCompile(`sqlcmd [^"\n]*-U (\S+)`), content)
			require.NotEmpty(t, found, "%s's SQL Server health check signs in as nobody", name)
			for _, value := range found {
				assert.Equal(t, user, value, "the user %s's health check signs in as", name)
			}
		}
		assert.Equal(t, []string{user}, allSubmatches(regexp.MustCompile(`"username":\s*"([^"]+)"`), e2e),
			"the user the e2e suite signs in as")
		assert.Contains(t, defaultsRow(t, docs, EnvUser), "`"+user+"` for SQL Server",
			"the defaults table in TESTING.md")
		assert.Equal(t, []string{user}, allSubmatches(regexp.MustCompile(`User: "([^"]+)"`), example),
			"the user in TESTING.md's Configure example")
	})

	t.Run("port", func(t *testing.T) {
		published := regexp.MustCompile(`-p (?:[0-9.]+:)?(\d+):1433\b`)
		for name, content := range map[string]string{"live_sqlserver_test.go": e2e, "TESTING.md": docs} {
			found := allSubmatches(published, content)
			require.NotEmpty(t, found, "%s publishes no SQL Server port", name)
			for _, value := range found {
				assert.Equal(t, port, value, "the port %s publishes", name)
			}
		}

		assert.Equal(t, []string{port}, allSubmatches(regexp.MustCompile(`'(\d+):1433'`), ci),
			"the port CI's service publishes")
		assert.Equal(t, []string{port}, allSubmatches(regexp.MustCompile(`E2E_MSSQL_PORT: '(\d+)'`), ci),
			"the port CI points the e2e suite at")
		assert.Equal(t, []string{port},
			allSubmatches(regexp.MustCompile(`liveEnginePort\("E2E_MSSQL_PORT", (\d+)\)`), e2e),
			"the e2e suite's default port")
		assert.Contains(t, defaultsRow(t, docs, EnvPort), "`"+port+"` for SQL Server",
			"the defaults table in TESTING.md")
		assert.Equal(t, []string{port}, allSubmatches(regexp.MustCompile(`Port: (\d+)`), example),
			"the port in TESTING.md's Configure example")
	})
}

// defaultsRow returns the row of TESTING.md's defaults table for the variable name.
func defaultsRow(t *testing.T, docs, name string) string {
	t.Helper()

	row := regexp.MustCompile("(?m)^\\| `" + regexp.QuoteMeta(name) + "` \\|.*$").FindString(docs)
	require.NotEmpty(t, row, "TESTING.md's defaults table has no %s row", name)
	return row
}

// readRepoFile reads a file by its path from the module root, which is this package's parent.
func readRepoFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(path)))
	require.NoError(t, err, "cannot read %s", path)
	return string(content)
}

// allSubmatches returns the first group of every match of pattern in content.
func allSubmatches(pattern *regexp.Regexp, content string) []string {
	var out []string
	for _, match := range pattern.FindAllStringSubmatch(content, -1) {
		out = append(out, match[1])
	}
	return out
}
