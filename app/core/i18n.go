package core

type Ii18nManager interface {
	GetConfig(locale string) Ii18nConfig
}

type Ii18nConfig interface {
	GetString(key string) string
}
