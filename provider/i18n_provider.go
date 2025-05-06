package provider

import (
	"fmt"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/i18n"
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

		configs := make(map[string]core.Ii18nConfig, len(available))
		for _, lang := range available {
			cfg := viper.New()
			cfg.AddConfigPath("resource/i18n")
			cfg.SetConfigName(lang)
			if err := cfg.ReadInConfig(); err != nil {
				panic(fmt.Errorf("i18n: failed to read config for '%s': %w", lang, err))
			}
			configs[lang] = cfg
		}

		mgr := i18n.Manager{
			Configs: configs,
		}

		return mgr
	})
}

func (p *I18nProvider) Boot(c core.IContainer) {
	c.Invoke(func(m core.Ii18nManager) {
		i18n.SetManager(m)
	})
}
