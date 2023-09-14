package i18n

import (
	"gorgany/internal"
	"gorgany/proxy"
)

func GetManager() proxy.Ii18nManager {
	return internal.GetFrameworkRegistrar().GetI18nManager()
}

type Manager struct {
	Configs map[string]proxy.Ii18nConfig
}

func (thiz Manager) GetConfig(locale string) proxy.Ii18nConfig {
	return thiz.Configs[locale]
}
