package provider

import (
	"gorgany/internal"
	"gorgany/proxy"
)

type AppProvider struct {
	AppRegistrar proxy.IRegistrar
}

func NewAppProvider() *AppProvider {
	return &AppProvider{}
}

func (thiz *AppProvider) InitProvider() {
	thiz.AppRegistrar = internal.GetFrameworkRegistrar()
}

func (thiz *AppProvider) RegisterProvider(provider proxy.IProvider) {
	provider.InitProvider()
}
