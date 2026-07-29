package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/service"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// I18nProvider reads resource/i18n/<lang>.yaml relative to the working directory,
// so these tests chdir into a temp dir laid out that way.
func withLocaleFiles(t *testing.T, files map[string]string) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "resource", "i18n"), 0o755))

	for lang, body := range files {
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "resource", "i18n", lang+".yaml"), []byte(body), 0o644))
	}

	previousWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(previousWd) })
}

func withI18nConfig(t *testing.T, defaultLang string, available []string) {
	t.Helper()

	prevDefault := viper.Get("i18n.lang.default")
	prevAvailable := viper.Get("i18n.lang.available")

	viper.Set("i18n.lang.default", defaultLang)
	viper.Set("i18n.lang.available", available)

	t.Cleanup(func() {
		viper.Set("i18n.lang.default", prevDefault)
		viper.Set("i18n.lang.available", prevAvailable)
	})
}

// resolveManager runs the provider's Register and resolves the manager it bound.
// The binding is lazy, so the file reads happen at resolution time.
func resolveManager(t *testing.T) (core.Ii18nManager, error) {
	t.Helper()

	c := service.NewContainer()
	NewI18nProvider().Register(c)

	var mgr core.Ii18nManager
	err := c.Make(&mgr)
	return mgr, err
}

// TestMissingLocaleFileNoLongerPanics is the T4.2 regression. The provider did:
//
//	if err := cfg.ReadInConfig(); err != nil {
//	    panic(fmt.Errorf("i18n: failed to read config for '%s': %w", lang, err))
//	}
//
// so one absent translation file took down boot for the whole app. This test is the
// assertion that was missing: the earlier work only tested i18n.Manager, never the
// provider whose panic was the actual defect.
func TestMissingLocaleFileNoLongerPanics(t *testing.T) {
	withLocaleFiles(t, map[string]string{
		"en": "greeting: Hello\n",
		// `de` is deliberately absent.
	})
	withI18nConfig(t, "en", []string{"en", "de"})

	var mgr core.Ii18nManager
	var err error
	require.NotPanics(t, func() { mgr, err = resolveManager(t) },
		"a missing non-default locale file must not panic")
	require.NoError(t, err)
	require.NotNil(t, mgr)

	// The present locale works.
	assert.Equal(t, "Hello", mgr.GetConfig("en").GetString("greeting"))

	// And the absent one degrades to the default rather than nil-dereferencing.
	assert.Equal(t, "Hello", mgr.GetConfig("de").GetString("greeting"),
		"the missing locale must fall back to the default")
}

// TestAllLocalesPresentStillWorks is the unchanged happy path.
func TestAllLocalesPresentStillWorks(t *testing.T) {
	withLocaleFiles(t, map[string]string{
		"en": "greeting: Hello\n",
		"uk": "greeting: Vitayu\n",
	})
	withI18nConfig(t, "en", []string{"en", "uk"})

	mgr, err := resolveManager(t)
	require.NoError(t, err)

	assert.Equal(t, "Hello", mgr.GetConfig("en").GetString("greeting"))
	assert.Equal(t, "Vitayu", mgr.GetConfig("uk").GetString("greeting"))
}

// TestMissingDefaultLocaleIsStillFatal is the deliberate exception. Degrading is
// right for a missing translation, but if the *default* locale is unreadable there
// is nothing to fall back to, so refusing to start is the honest outcome.
func TestMissingDefaultLocaleIsStillFatal(t *testing.T) {
	withLocaleFiles(t, map[string]string{
		"uk": "greeting: Vitayu\n",
		// `en` — the default — is absent.
	})
	withI18nConfig(t, "en", []string{"en", "uk"})

	assert.Panics(t, func() { _, _ = resolveManager(t) },
		"an unreadable default locale leaves nothing to fall back to")
}

// TestDefaultLangIsAddedToAvailable pins the pre-existing behaviour that the default
// is always loaded even when it is not listed under available.
func TestDefaultLangIsAddedToAvailable(t *testing.T) {
	withLocaleFiles(t, map[string]string{"en": "greeting: Hello\n"})
	withI18nConfig(t, "en", []string{}) // `en` not listed

	mgr, err := resolveManager(t)
	require.NoError(t, err)
	assert.Equal(t, "Hello", mgr.GetConfig("en").GetString("greeting"))
}

// TestUnknownLocaleResolvesToTheFallback covers a locale nobody configured at all,
// which Translation would previously have nil-dereferenced.
func TestUnknownLocaleResolvesToTheFallback(t *testing.T) {
	withLocaleFiles(t, map[string]string{"en": "greeting: Hello\n"})
	withI18nConfig(t, "en", []string{"en"})

	mgr, err := resolveManager(t)
	require.NoError(t, err)

	cfg := mgr.GetConfig("fr")
	require.NotNil(t, cfg, "an unconfigured locale must never yield nil")
	assert.Equal(t, "Hello", cfg.GetString("greeting"))
}
