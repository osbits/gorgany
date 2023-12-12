package provider

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/service"
	"reflect"
)

type AppProvider struct {
	AppRegistrar core.IRegistrar
}

func NewAppProvider() *AppProvider {
	return &AppProvider{}
}

func (thiz *AppProvider) InitProvider() {
	thiz.AppRegistrar = internal.GetFrameworkRegistrar()
	thiz.AppRegistrar.RegisterContainer(service.NewContainer())

	err.HandleErrorWithStacktrace(thiz.AppRegistrar.GetContainer().Bind(func() core.IViewEngine {
		return thiz.AppRegistrar.GetViewEngine()
	}))
}

func (thiz *AppProvider) RegisterProvider(provider core.IProvider) {
	rtProvider := reflect.TypeOf(provider)
	if rtProvider.Kind() == reflect.Ptr {
		rtProvider = rtProvider.Elem()
	}

	log.Log("").Infof("Provider \u001B[0;32m`%s`\u001B[0m is registering", rtProvider.Name())
	provider.InitProvider()
	log.Log("").Infof("Provider \u001B[0;32m`%s`\u001B[0m registered\n\n", rtProvider.Name())
}
