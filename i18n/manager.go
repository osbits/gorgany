package i18n

import (
	"github.com/osbits/gorgany/v2/app/core"
)

var (
	mgr     core.Ii18nManager
	setOnce = false
)

func SetManager(m core.Ii18nManager) {
	if setOnce {
		panic("i18n: manager already set")
	}
	mgr = m
	setOnce = true
}

func GetManager() core.Ii18nManager {
	if mgr == nil {
		panic("i18n: manager not initialized")
	}
	return mgr
}

// HasManager reports whether a manager has been installed.
//
// It exists so a caller that can proceed without translations can ask instead of
// recovering from GetManager's panic. The validator's message catalog is the case that
// needs it: a CLI app validates its command DTOs without ever booting i18n, and
// panicking there would turn a bad flag into a crash.
func HasManager() bool {
	return mgr != nil
}

type Manager struct {
	Configs map[string]core.Ii18nConfig

	// FallbackTag names the locale to use when the requested one has no config.
	// I18nProvider sets it to i18n.lang.default.
	FallbackTag string
}

// GetConfig returns the config for locale, falling back to FallbackTag when the
// requested locale has none.
//
// It used to return the raw map lookup, so an unconfigured locale yielded a nil
// core.Ii18nConfig and the very next call — cfg.GetString(code) in Translation —
// dereferenced it. That was survivable only because I18nProvider panicked at boot
// if any configured locale's file was missing; now that a missing file degrades
// instead of taking the app down, GetConfig has to hold up the other end.
func (thiz Manager) GetConfig(locale string) core.Ii18nConfig {
	if cfg, ok := thiz.Configs[locale]; ok && cfg != nil {
		return cfg
	}

	if thiz.FallbackTag != "" && thiz.FallbackTag != locale {
		if cfg, ok := thiz.Configs[thiz.FallbackTag]; ok && cfg != nil {
			return cfg
		}
	}

	// Nothing configured at all. An empty config resolves every code to "" rather
	// than nil-dereferencing inside Translation.
	return emptyConfig{}
}

// emptyConfig is the null object GetConfig returns when no locale resolves.
type emptyConfig struct{}

func (emptyConfig) GetString(string) string { return "" }

var _ core.Ii18nConfig = emptyConfig{}
