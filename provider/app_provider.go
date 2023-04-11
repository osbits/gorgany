package provider

type AppProvider struct{}

func NewAppProvider() *AppProvider {
	return &AppProvider{}
}

func (thiz *AppProvider) InitProvider() {
	for _, provider := range FrameworkRegistrar.GetProviders() {
		provider.InitProvider()
	}
}
