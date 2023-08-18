package provider

import (
	"gorgany/internal"
	"gorgany/log"
	"gorgany/proxy"
	"reflect"
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
	rtProvider := reflect.TypeOf(provider)
	if rtProvider.Kind() == reflect.Ptr {
		rtProvider = rtProvider.Elem()
	}

	log.Log("").Infof("Provider \u001B[0;32m`%s`\u001B[0m is registering", rtProvider.Name())
	provider.InitProvider()
	log.Log("").Infof("Provider \u001B[0;32m`%s`\u001B[0m registered\n\n", rtProvider.Name())
}
