package builtin_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/sql/driver"
	_ "github.com/osbits/gorgany/v2/db/sql/driver/builtin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F7: provider.DbProvider used to blank-import builtin, which registers both engines — so
// every app on the standard bootstrap linked gorm.io/driver/mysql, go-sql-driver/mysql and
// filippo.io/edwards25519 whether or not it would ever speak MySQL. DbProvider now imports
// nothing and the app chooses.

// TestBuiltinStillRegistersBothEngines: it is the convenience import, and an app that had
// it must keep working unchanged.
func TestBuiltinStillRegistersBothEngines(t *testing.T) {
	names := driver.Names()

	assert.Contains(t, names, "postgres_gorm")
	assert.Contains(t, names, "mysql_gorm")
}

// TestBothEnginesResolveThroughBuiltin — registered *and* constructible, since a
// registration that cannot build anything would satisfy the test above.
func TestBothEnginesResolveThroughBuiltin(t *testing.T) {
	for _, name := range []string{"postgres_gorm", "mysql_gorm"} {
		ctor, ok := driver.Lookup(name)
		require.Truef(t, ok, "%s must be registered", name)
		require.NotNilf(t, ctor, "%s must have a constructor", name)
	}
}

// TestASingleEngineImportDoesNotLinkTheOther is the dependency-hygiene claim, and it has to
// be checked with the build tool rather than at runtime: once this test binary imports
// builtin, both engines are linked into *it* by construction.
//
// `go list -deps` on the single-engine package is the assertion. It is skipped when the
// toolchain is unavailable rather than failing, since that is an environment problem and not
// a defect in the split.
func TestASingleEngineImportDoesNotLinkTheOther(t *testing.T) {
	deps := func(pkg string) string {
		t.Helper()

		out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
		if err != nil {
			t.Skipf("go list unavailable: %v", err)
		}
		return string(out)
	}

	postgresOnly := deps("github.com/osbits/gorgany/v2/db/sql/driver/postgres")
	assert.Contains(t, postgresOnly, "gorm.io/driver/postgres")
	assert.NotContains(t, postgresOnly, "gorm.io/driver/mysql",
		"importing driver/postgres must not link the MySQL driver")
	assert.NotContains(t, postgresOnly, "github.com/go-sql-driver/mysql")

	mysqlOnly := deps("github.com/osbits/gorgany/v2/db/sql/driver/mysql")
	assert.Contains(t, mysqlOnly, "gorm.io/driver/mysql")
	assert.NotContains(t, mysqlOnly, "gorm.io/driver/postgres",
		"and the reverse holds too")

	for name, linked := range map[string]string{"driver/postgres": postgresOnly, "driver/mysql": mysqlOnly} {
		for _, engine := range sqlServerPackages {
			assert.NotContainsf(t, linked, engine, "importing %s must not link %s", name, engine)
		}
	}
}

// sqlServerPackages are what the SQL Server engine links, and no package that does not speak
// SQL Server may.
var sqlServerPackages = []string{"github.com/microsoft/go-mssqldb", "gorm.io/driver/sqlserver"}

// azureSDKModules are what the Entra ID sign-in links, and only driver/sqlserver/azuread may.
var azureSDKModules = []string{
	"github.com/Azure/azure-sdk-for-go",
	"github.com/AzureAD/microsoft-authentication-library-for-go",
	"github.com/pkg/browser",
}

// linkedPackages returns the packages pkg links, one per entry, or skips the test when the
// toolchain is unavailable.
func linkedPackages(t *testing.T, pkg string) map[string]bool {
	t.Helper()

	out, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	linked := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		linked[strings.TrimSpace(line)] = true
	}
	return linked
}

// linksUnder reports the first package in linked that is module or inside it, or "".
func linksUnder(linked map[string]bool, module string) string {
	for pkg := range linked {
		if pkg == module || strings.HasPrefix(pkg, module+"/") {
			return pkg
		}
	}
	return ""
}

// requireNoSQLServer fails unless none of pkgs links the SQL Server engine, or the Azure SDK its
// Entra ID sign-in brings.
//
// The SQL Server driver stays out of builtin, and the framework packages every app links pick
// SQL Server behaviour by name only — gorm's Dialector.Name() or the dialect's Name() — so
// that an app that never speaks SQL Server never links go-mssqldb, and one that never signs in
// with Entra ID never links azidentity or MSAL.
func requireNoSQLServer(t *testing.T, pkgs ...string) {
	t.Helper()
	for _, pkg := range pkgs {
		linked := linkedPackages(t, pkg)
		for _, module := range append(append([]string{}, sqlServerPackages...), azureSDKModules...) {
			assert.Emptyf(t, linksUnder(linked, module), "%s must not link %s", pkg, module)
		}
	}
}

// TestBuiltinDoesNotRegisterSQLServer: builtin is Postgres and MySQL, and stays that way; this
// test binary imports builtin and nothing else.
func TestBuiltinDoesNotRegisterSQLServer(t *testing.T) {
	_, ok := driver.Lookup("sqlserver_gorm")
	assert.False(t, ok, "driver/builtin must not register SQL Server")
}

func TestBuiltinLinksNoSQLServer(t *testing.T) {
	requireNoSQLServer(t, "github.com/osbits/gorgany/v2/db/sql/driver/builtin")
}

// TestTestsupportLinksNoSQLServer: every test binary of an app links testsupport.
func TestTestsupportLinksNoSQLServer(t *testing.T) {
	requireNoSQLServer(t, "github.com/osbits/gorgany/v2/testsupport")
}

func TestProviderAuthAndCommandsLinkNoSQLServer(t *testing.T) {
	requireNoSQLServer(t,
		"github.com/osbits/gorgany/v2/provider",
		"github.com/osbits/gorgany/v2/auth",
		"github.com/osbits/gorgany/v2/command/db",
		"github.com/osbits/gorgany/v2/db/migration",
	)
}

func TestORMLinksNoSQLServer(t *testing.T) {
	requireNoSQLServer(t, "github.com/osbits/gorgany/v2/db/orm")
}

// TestTheSQLServerDriverDoesNotLinkAzureAD: a SQL login needs go-mssqldb and nothing of Azure.
// The Entra ID methods live in driver/sqlserver/azuread, since they link the identity SDK, MSAL
// and a browser opener, which an app that signs in with a SQL login must not link. Nor may the
// SQL Server driver link another engine's.
func TestTheSQLServerDriverDoesNotLinkAzureAD(t *testing.T) {
	linked := linkedPackages(t, "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver")

	assert.True(t, linked["github.com/microsoft/go-mssqldb"], "the driver is go-mssqldb")
	assert.True(t, linked["gorm.io/driver/sqlserver"])
	for _, module := range []string{
		"github.com/Azure/azure-sdk-for-go",
		"github.com/AzureAD/microsoft-authentication-library-for-go",
		"github.com/microsoft/go-mssqldb/azuread",
		"github.com/microsoft/go-mssqldb/integratedauth/krb5",
		"github.com/pkg/browser",
		"github.com/jackc/pgx",
		"github.com/go-sql-driver/mysql",
		"gorm.io/driver/postgres",
		"gorm.io/driver/mysql",
	} {
		assert.Emptyf(t, linksUnder(linked, module), "driver/sqlserver must not link %s", module)
	}
}

// TestAzureADLinksTheAzureSDKButNotTheKeychain: the Entra ID package is where the Azure SDK
// comes in, together with the driver it registers, and it stops short of three things. The
// persistent token cache links the operating system's keychain, cgo on macOS, and belongs to an
// opt-in package of its own. go-mssqldb's own azuread package builds a credential for every
// connection, so an interactive sign-in would prompt for each; this package builds one per
// datasource instead. Kerberos is a sign-in the engine refuses.
func TestAzureADLinksTheAzureSDKButNotTheKeychain(t *testing.T) {
	linked := linkedPackages(t, "github.com/osbits/gorgany/v2/db/sql/driver/sqlserver/azuread")

	for _, pkg := range []string{
		"github.com/Azure/azure-sdk-for-go/sdk/azidentity",
		"github.com/Azure/azure-sdk-for-go/sdk/azcore",
		"github.com/osbits/gorgany/v2/db/sql/driver/sqlserver",
		"github.com/microsoft/go-mssqldb",
	} {
		assert.Truef(t, linked[pkg], "driver/sqlserver/azuread links %s", pkg)
	}
	assert.NotEmpty(t, linksUnder(linked, "github.com/AzureAD/microsoft-authentication-library-for-go"),
		"azidentity signs in through MSAL")

	for _, module := range []string{
		"github.com/Azure/azure-sdk-for-go/sdk/azidentity/cache",
		"github.com/AzureAD/microsoft-authentication-extensions-for-go",
		"github.com/keybase/go-keychain",
		"github.com/microsoft/go-mssqldb/azuread",
		"github.com/microsoft/go-mssqldb/integratedauth/krb5",
		"github.com/jcmturner/gokrb5",
		"github.com/jackc/pgx",
		"github.com/go-sql-driver/mysql",
	} {
		assert.Emptyf(t, linksUnder(linked, module), "driver/sqlserver/azuread must not link %s", module)
	}
}

// TestDbProviderLinksNeitherEngine is the other half: the provider itself must be
// engine-agnostic, or the split achieves nothing for an app that uses the standard
// bootstrap.
func TestDbProviderLinksNeitherEngine(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/osbits/gorgany/v2/provider").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	deps := string(out)
	for _, engine := range []string{
		"gorm.io/driver/mysql",
		"gorm.io/driver/postgres",
		"github.com/go-sql-driver/mysql",
	} {
		assert.NotContainsf(t, deps, engine,
			"provider must not link %s — the app chooses its engine", engine)
	}
}

// TestThePolicyGuardLinksNoEngine: db/sql/gorm/guard is installed by every engine's
// datasource, so it must link none of them. It reads an engine's SQL through the lexicon the
// engine hands it, and if it imported a driver to learn anything else, every engine would link
// that one — the SQL Server engine the Postgres driver, say. The SQL Server names are checked
// before that engine exists so that it cannot be the first to break the rule.
func TestThePolicyGuardLinksNoEngine(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/osbits/gorgany/v2/db/sql/gorm/guard").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	deps := string(out)
	assert.Contains(t, deps, "gorm.io/gorm", "the guard is a set of gorm callbacks")
	for _, engine := range []string{
		"github.com/jackc/pgx",
		"github.com/go-sql-driver/mysql",
		"gorm.io/driver/postgres",
		"gorm.io/driver/mysql",
		"gorm.io/driver/sqlserver",
		"github.com/microsoft/go-mssqldb",
	} {
		assert.NotContainsf(t, deps, engine, "the guard must not link %s", engine)
	}
}

// TestTheDriverNameConstantsAgree, since apps referenced builtin's before the split and the
// single-engine packages expose their own.
func TestTheDriverNameConstantsAgree(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.Name}}",
		"github.com/osbits/gorgany/v2/db/sql/driver/postgres").CombinedOutput()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	assert.Equal(t, "postgres", strings.TrimSpace(string(out)))

	out, err = exec.Command("go", "list", "-f", "{{.Name}}",
		"github.com/osbits/gorgany/v2/db/sql/driver/sqlserver").CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, "sqlserver", strings.TrimSpace(string(out)))
	assert.Equal(t, "sqlserver_gorm", string(core.GormSQLServer),
		"the SQL Server name is checked against core here, since this binary must not link that driver")
}
