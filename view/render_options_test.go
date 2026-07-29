package view

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The renderer reads an "fn" entry out of the caller-assembled opts map. Both
// registerFunctions and NativeEngine.Render used to assert its type unchecked, so a
// controller passing anything other than a map[string]any under that key crashed the
// render rather than getting its template back.

// TestRegisterFunctionsToleratesABadFnEntry covers view.go:64.
func TestRegisterFunctionsToleratesABadFnEntry(t *testing.T) {
	badShapes := map[string]any{
		"a string":                    "not a func map",
		"a map with the wrong value":  map[string]string{"x": "y"},
		"a slice":                     []string{"x"},
		"an explicit nil":             nil,
		"a func rather than a facade": func() {},
	}

	for name, bad := range badShapes {
		t.Run(name, func(t *testing.T) {
			er := &EngineRenderer{}
			er.Init()
			lrc := newLocalRenderingContext(context.Background(), er)

			var opts map[string]any
			require.NotPanics(t, func() {
				opts = er.registerFunctions(lrc, map[string]any{"fn": bad})
			})

			funcs, ok := opts["fn"].(map[string]any)
			require.True(t, ok, "the renderer always leaves a usable func map behind")
			assert.Contains(t, funcs, "UrlByName", "the built-ins are still registered")
		})
	}
}

// TestRegisterFunctionsKeepsCallerSuppliedFunctions is the happy path: a well-formed
// "fn" entry is merged, not discarded.
func TestRegisterFunctionsKeepsCallerSuppliedFunctions(t *testing.T) {
	er := &EngineRenderer{}
	er.Init()
	er.RegisterGlobalFunction("global", func() string { return "g" })
	lrc := newLocalRenderingContext(context.Background(), er)

	opts := er.registerFunctions(lrc, map[string]any{
		"fn": map[string]any{"caller": func() string { return "c" }},
	})

	funcs := opts["fn"].(map[string]any)
	assert.Contains(t, funcs, "caller")
	assert.Contains(t, funcs, "global")
	assert.Contains(t, funcs, "SafeHtml")
}

// TestNativeEngineToleratesABadFnEntry covers native.go:55, the same defect one layer
// down — reached when a caller invokes the engine directly instead of through
// EngineRenderer.
func TestNativeEngineToleratesABadFnEntry(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "page.gohtml"), []byte(`hello {{.Name}}`), 0o600))

	engine := NewNativeEngine(dir, "gohtml")

	var out strings.Builder
	var err error
	require.NotPanics(t, func() {
		err = engine.Render(&out, "page", map[string]any{
			"fn":   "not a func map",
			"Name": "world",
		})
	})

	require.NoError(t, err)
	assert.Equal(t, "hello world", out.String())
}

// TestNativeEngineUsesCallerSuppliedFunctions is the happy path for the same site.
func TestNativeEngineUsesCallerSuppliedFunctions(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "page.gohtml"), []byte(`{{shout "hi"}}`), 0o600))

	engine := NewNativeEngine(dir, "gohtml")

	var out strings.Builder
	err := engine.Render(&out, "page", map[string]any{
		"fn": map[string]any{"shout": strings.ToUpper},
	})

	require.NoError(t, err)
	assert.Equal(t, "HI", out.String())
}
