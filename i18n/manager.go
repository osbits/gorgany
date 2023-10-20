package i18n

import (
	"gorgany/app/core"
	"gorgany/internal"
)

func GetManager() core.Ii18nManager {
	return internal.GetFrameworkRegistrar().GetI18nManager()
}

type Manager struct {
	Configs map[string]core.Ii18nConfig
}

func (thiz Manager) GetConfig(locale string) core.Ii18nConfig {
	return thiz.Configs[locale]
}
