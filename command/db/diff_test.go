package db

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// diffStatements covers what db:diff records: PostgreSQL DDL with double-quoted
// identifiers (one of them a reserved word), MySQL DDL with backquoted ones, and a
// statement a raw string cannot hold.
var diffStatements = []string{
	`CREATE TABLE "orders" ("id" bigserial,"order" bigint,"Label" text,PRIMARY KEY ("id"))`,
	"ALTER TABLE `notes` ADD `title` longtext",
	"CREATE INDEX idx_notes_title ON notes (title)\nWHERE title <> 'a\\b'",
}

var diffGeneratedAt = time.Date(2026, 9, 26, 14, 30, 5, 0, time.UTC)

func renderDiffMigration(t *testing.T) (string, []byte, *ast.File) {
	t.Helper()

	fileName, source, err := renderMigration(diffStatements, "reports", diffGeneratedAt)
	require.NoError(t, err)

	file, err := parser.ParseFile(token.NewFileSet(), fileName, source, 0)
	require.NoError(t, err, "the generated migration must parse:\n%s", source)
	return fileName, source, file
}

// method returns the body of the generated migration's method called name.
func method(t *testing.T, file *ast.File, name string) *ast.BlockStmt {
	t.Helper()

	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil && fn.Name.Name == name {
			return fn.Body
		}
	}
	require.Failf(t, "method not generated", "no %s method", name)
	return nil
}

// stringLiterals returns the values of the string literals under node, in source order.
func stringLiterals(t *testing.T, node ast.Node) []string {
	t.Helper()

	var values []string
	ast.Inspect(node, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			value, err := strconv.Unquote(lit.Value)
			require.NoError(t, err)
			values = append(values, value)
		}
		return true
	})
	return values
}

// TestRenderedMigrationRunsOnTheTransaction is the regression for the draft db:diff
// wrote: its Up called dbGorm.DB(), which on the transaction db:migrate passes in returns
// the underlying pool, so the statements ran outside the transaction and a failure left
// the earlier ones applied and the migration unrecorded.
func TestRenderedMigrationRunsOnTheTransaction(t *testing.T) {
	_, source, file := renderDiffMigration(t)

	ast.Inspect(file, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				assert.NotEqual(t, "DB", sel.Sel.Name, "nothing may reach past the transaction to the pool")
			}
		}
		return true
	})
	assert.Contains(t, string(source), "dbGorm.Exec(statement).Error")
}

// TestRenderedMigrationKeepsEveryStatementVerbatim: the generator used to strip every
// `"` so a statement could be pasted between quotes, which broke quoted identifiers
// such as the reserved word "order" and the mixed-case "Label".
func TestRenderedMigrationKeepsEveryStatementVerbatim(t *testing.T) {
	_, _, file := renderDiffMigration(t)

	var statements *ast.CompositeLit
	ast.Inspect(method(t, file, "Up"), func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok && statements == nil {
			statements = lit
		}
		return true
	})
	require.NotNil(t, statements, "Up must hold its statements in a slice literal")

	assert.Equal(t, diffStatements, stringLiterals(t, statements))
}

// TestRenderedMigrationRefusesToRollBack: the generated Down returned nil, so
// `db:migrate down` recorded a rollback that changed nothing.
func TestRenderedMigrationRefusesToRollBack(t *testing.T) {
	_, _, file := renderDiffMigration(t)

	assert.Equal(t,
		[]string{"migration 20260926_143005.000 is not reversible: db:diff does not generate Down()"},
		stringLiterals(t, method(t, file, "Down")))
}

// TestRenderedMigrationTargetsTheDiffedDatasource: a draft from
// `db:diff --datasource=reports` that declared nothing would be applied to `default`.
func TestRenderedMigrationTargetsTheDiffedDatasource(t *testing.T) {
	fileName, _, file := renderDiffMigration(t)

	assert.Equal(t, "20260926143005_migration.go", fileName)
	assert.Equal(t, []string{"20260926_143005.000"}, stringLiterals(t, method(t, file, "Name")))
	assert.Equal(t, []string{"reports"}, stringLiterals(t, method(t, file, "DataSourceName")))
}

func TestRenderedMigrationIsGofmtClean(t *testing.T) {
	_, source, _ := renderDiffMigration(t)

	formatted, err := format.Source(source)
	require.NoError(t, err)
	assert.Equal(t, string(formatted), string(source))
}

// TestRenderedMigrationCompiles builds the draft against the framework, which parsing
// alone cannot prove: no test compiled what the old template produced.
func TestRenderedMigrationCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a package with the go command")
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Skip("the go command is not on PATH")
	}

	fileName, source, _ := renderDiffMigration(t)

	// Inside the module, so its imports resolve against this checkout; the leading
	// underscore keeps it out of `./...`.
	dir, err := os.MkdirTemp(".", "_diffmigration-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	require.NoError(t, os.WriteFile(filepath.Join(dir, fileName), source, 0o644))

	build := exec.Command(goBinary, "vet", "./"+filepath.ToSlash(dir))
	output, err := build.CombinedOutput()
	require.NoError(t, err, "the generated migration must compile and vet clean:\n%s\n%s", output, source)
}

// TestGenerateMigrationWritesAnOrdinarySourceFile: the file was written with
// os.ModePerm, and db/migration was created only if db/ already existed.
func TestGenerateMigrationWritesAnOrdinarySourceFile(t *testing.T) {
	t.Chdir(t.TempDir())

	DiffCommand{}.generateMigration([]string{`CREATE TABLE "widgets" ("id" int)`}, "default")

	written, err := filepath.Glob(filepath.Join(MigrationDir, "*_migration.go"))
	require.NoError(t, err)
	require.Len(t, written, 1)

	info, err := os.Stat(written[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())

	source, err := os.ReadFile(written[0])
	require.NoError(t, err)
	formatted, err := format.Source(source)
	require.NoError(t, err)
	assert.Equal(t, string(formatted), string(source), "the written file must be gofmt-clean")
}
