package i18n

import "github.com/spf13/viper"

var manager *Manager

func SetManager(m *Manager) {
	manager = m
}

func GetManager() *Manager {
	return manager
}

type Manager struct {
	Configs map[string]*viper.Viper
}

func (thiz Manager) GetConfig(locale string) *viper.Viper {
	return thiz.Configs[locale]
}
