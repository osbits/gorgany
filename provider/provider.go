package provider

type IProvider interface {
	InitProvider()
}

type IProviders []IProvider

func (thiz IProviders) AddProvider(provider IProvider) {
	thiz = append(thiz, provider)
}
