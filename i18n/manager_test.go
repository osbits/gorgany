package i18n

import (
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubConfig struct {
	values map[string]string
}

func (c stubConfig) GetString(key string) string { return c.values[key] }

// TestGetConfigReturnsTheRequestedLocale is the baseline.
func TestGetConfigReturnsTheRequestedLocale(t *testing.T) {
	mgr := Manager{
		Configs: map[string]core.Ii18nConfig{
			"en": stubConfig{values: map[string]string{"greeting": "Hello"}},
			"uk": stubConfig{values: map[string]string{"greeting": "Вітаю"}},
		},
		FallbackTag: "en",
	}

	assert.Equal(t, "Вітаю", mgr.GetConfig("uk").GetString("greeting"))
	assert.Equal(t, "Hello", mgr.GetConfig("en").GetString("greeting"))
}

// TestGetConfigFallsBackForAnUnconfiguredLocale is what makes T4.2 safe. GetConfig
// used to return the raw map lookup, so an unconfigured locale yielded a nil
// Ii18nConfig and the very next call — cfg.GetString(code) in Translation —
// dereferenced it. That only stayed hidden because I18nProvider panicked at boot
// for any missing locale file; now that a missing file degrades instead, GetConfig
// has to hold up the other end.
func TestGetConfigFallsBackForAnUnconfiguredLocale(t *testing.T) {
	mgr := Manager{
		Configs: map[string]core.Ii18nConfig{
			"en": stubConfig{values: map[string]string{"greeting": "Hello"}},
		},
		FallbackTag: "en",
	}

	cfg := mgr.GetConfig("de")

	require.NotNil(t, cfg, "an unconfigured locale must never yield nil")
	assert.Equal(t, "Hello", cfg.GetString("greeting"),
		"it must fall back to the default locale")
}

// TestGetConfigNeverReturnsNil covers every degenerate shape.
func TestGetConfigNeverReturnsNil(t *testing.T) {
	tests := map[string]Manager{
		"no configs at all":     {},
		"nil map":               {Configs: nil, FallbackTag: "en"},
		"fallback also missing": {Configs: map[string]core.Ii18nConfig{}, FallbackTag: "en"},
		"nil config stored": {
			Configs:     map[string]core.Ii18nConfig{"en": nil},
			FallbackTag: "en",
		},
		"nil config for requested locale, good fallback": {
			Configs: map[string]core.Ii18nConfig{
				"de": nil,
				"en": stubConfig{values: map[string]string{"greeting": "Hello"}},
			},
			FallbackTag: "en",
		},
	}

	for name, mgr := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := mgr.GetConfig("de")
			require.NotNil(t, cfg)
			require.NotPanics(t, func() { _ = cfg.GetString("anything") })
		})
	}
}

// TestGetConfigWithoutFallbackTagStillDoesNotPanic covers a Manager built by hand
// before FallbackTag existed.
func TestGetConfigWithoutFallbackTagStillDoesNotPanic(t *testing.T) {
	mgr := Manager{Configs: map[string]core.Ii18nConfig{
		"en": stubConfig{values: map[string]string{"greeting": "Hello"}},
	}}

	cfg := mgr.GetConfig("de")
	require.NotNil(t, cfg)
	assert.Equal(t, "", cfg.GetString("greeting"),
		"with no fallback declared, an unknown locale resolves to empty strings")
}

func TestEmptyConfigResolvesEverythingToEmptyString(t *testing.T) {
	var cfg core.Ii18nConfig = emptyConfig{}
	assert.Equal(t, "", cfg.GetString("anything"))
}

// TestManagerSatisfiesTheInterface
func TestManagerSatisfiesTheInterface(t *testing.T) {
	var _ core.Ii18nManager = Manager{}
}
