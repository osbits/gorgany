package domain

import (
	"go/format"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The registrar is committed to applications and checked by their format gates, so it
// has to come out gofmt-clean and as an ordinary, non-executable source file.
func TestGenerateRegistrarWritesAGofmtCleanFile(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll(filepath.Join("pkg", "provider"), 0o755))

	err := RegisterDomainsCommand{}.generateRegistrar(
		[]string{"example.com/app/pkg/domain"},
		[]string{
			`"example.com/app/pkg/domain.Note": domain.Note{},`,
			`"example.com/app/pkg/domain.LongerName": domain.LongerName{},`,
		},
	)
	require.NoError(t, err)

	path := filepath.Join("pkg", "provider", "domains.go")
	written, err := os.ReadFile(path)
	require.NoError(t, err)

	formatted, err := format.Source(written)
	require.NoError(t, err)
	assert.Equal(t, string(formatted), string(written), "domains.go is not gofmt-clean")
	assert.Contains(t, string(written), "go run ./cmd/cli domains:register")

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}
