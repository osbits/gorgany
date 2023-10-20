package provider

import (
	"github.com/spf13/viper"
	"gorgany/app/core"
	"gorgany/i18n"
	"gorgany/internal"
)

type I18nProvider struct{}

func NewI18nProvider() *I18nProvider {
	return &I18nProvider{}
}

func (thiz *I18nProvider) InitProvider() {
	availableLangs := viper.GetStringSlice("i18n.lang.available")
	defaultLang := viper.GetString("i18n.lang.default")
	availableLangs = append(availableLangs, defaultLang)

	i18nConfigs := make(map[string]core.Ii18nConfig)
	for _, lang := range availableLangs {
		v := viper.New()
		v.AddConfigPath("resource/i18n")
		v.SetConfigName(lang)
		err := v.ReadInConfig()
		if err != nil {
			panic(err)
		}
		i18nConfigs[lang] = v
	}

	manager := &i18n.Manager{
		Configs: i18nConfigs,
	}

	thiz.SetManager(manager)
}

func (thiz I18nProvider) SetManager(manager core.Ii18nManager) {
	internal.GetFrameworkRegistrar().SetI18nManager(manager)
}
