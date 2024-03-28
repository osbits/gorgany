package provider

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/service"
	"git.qix.sx/gorgany/gorgany.git/view"
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

	err.HandleErrorWithStacktrace(thiz.AppRegistrar.GetContainer().BindLazy(func() core.IViewEngine {
		return thiz.AppRegistrar.GetViewEngine()
	}))

	err.HandleErrorWithStacktrace(thiz.AppRegistrar.GetContainer().SingletonLazy(func() *view.EngineRenderer {
		return &view.EngineRenderer{}
	}))

	err.HandleErrorWithStacktrace(thiz.AppRegistrar.GetContainer().SingletonLazy(func() core.ISessionStorage {
		return thiz.AppRegistrar.GetSessionStorage()
	}))

	err.HandleErrorWithStacktrace(thiz.AppRegistrar.GetContainer().SingletonLazy(func() core.IAuthStrategy {
		return thiz.AppRegistrar.GetAuthStrategy()
	}))
}

func (thiz *AppProvider) RegisterProvider(provider core.IProvider) {
	rtProvider := reflect.TypeOf(provider)
	if rtProvider.Kind() == reflect.Ptr {
		rtProvider = rtProvider.Elem()
	}

	log.Log("").Infof("Provider \u001B[0;32m`%s`\u001B[0m is registering", rtProvider.Name())
	provider.InitProvider(thiz)
	log.Log("").Infof("Provider \u001B[0;32m`%s`\u001B[0m registered\n\n", rtProvider.Name())
}

func (thiz *AppProvider) GetRegistrar() core.IRegistrar {
	return internal.GetFrameworkRegistrar()
}
