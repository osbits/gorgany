package i18n

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
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

type Manager struct {
	Configs map[string]core.Ii18nConfig
}

func (thiz Manager) GetConfig(locale string) core.Ii18nConfig {
	return thiz.Configs[locale]
}
