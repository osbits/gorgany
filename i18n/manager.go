package i18n

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/internal"
)

func GetManager() core.Ii18nManager {
	return internal.GetApplicationContext().GetI18nManager()
}

type Manager struct {
	Configs map[string]core.Ii18nConfig
}

func (thiz Manager) GetConfig(locale string) core.Ii18nConfig {
	return thiz.Configs[locale]
}
