package provider

import (
	"fmt"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/i18n"
	"github.com/osbits/gorgany/log"
	"github.com/spf13/viper"
)

type I18nProvider struct{}

func NewI18nProvider() *I18nProvider {
	return &I18nProvider{}
}

func (p *I18nProvider) Register(c core.IContainer) {
	c.SingletonLazy(func() core.Ii18nManager {
		available := viper.GetStringSlice("i18n.lang.available")
		defaultLang := viper.GetString("i18n.lang.default")

		foundDefault := false
		for _, lang := range available {
			if lang == defaultLang {
				foundDefault = true
				break
			}
		}
		if !foundDefault {
			available = append(available, defaultLang)
		}

		// A missing resource/i18n/<lang>.yaml used to panic here, taking down boot
		// for the whole app because one translation file was absent. It now degrades
		// to the fallback locale with a warning: a missing translation is a content
		// problem, and refusing to start is a worse answer than serving the default
		// language.
		//
		// The one case that is still fatal is the *default* locale being unreadable,
		// because then there is nothing to fall back to.
		configs := make(map[string]core.Ii18nConfig, len(available))
		missing := make([]string, 0)

		for _, lang := range available {
			cfg := viper.New()
			cfg.AddConfigPath("resource/i18n")
			cfg.SetConfigName(lang)
			if err := cfg.ReadInConfig(); err != nil {
				if lang == defaultLang {
					panic(fmt.Errorf(
						"i18n: cannot read the default locale '%s' from resource/i18n, "+
							"so there is nothing to fall back to: %w", lang, err))
				}
				missing = append(missing, lang)
				log.Log().Warnf(
					"i18n: no resource/i18n/%s.* found; requests for '%s' will fall back to '%s'. (%v)",
					lang, lang, defaultLang, err)
				continue
			}
			configs[lang] = cfg
		}

		if len(missing) > 0 {
			log.Log().Warnf("i18n: %d configured language(s) have no translation file: %v",
				len(missing), missing)
		}

		mgr := i18n.Manager{
			Configs:     configs,
			FallbackTag: defaultLang,
		}

		return mgr
	})
}

func (p *I18nProvider) Boot(c core.IContainer) {
	c.Invoke(func(m core.Ii18nManager) {
		i18n.SetManager(m)
	})
}
