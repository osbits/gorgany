package provider

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/service"
	"git.qix.sx/gorgany/gorgany.git/validator"
	"git.qix.sx/gorgany/gorgany.git/view"
	"reflect"
)

type AppProvider struct {
	applicationContext core.IApplicationContext
}

func NewAppProvider() *AppProvider {
	return &AppProvider{}
}

func (thiz *AppProvider) InitProvider(applicationContext core.IApplicationContext) {
	thiz.applicationContext = applicationContext
	applicationContext.RegisterContainer(service.NewContainer())

	err.HandleErrorWithStacktrace(applicationContext.GetContainer().BindLazy(func() core.IViewEngine {
		return applicationContext.GetViewEngine()
	}))

	err.HandleErrorWithStacktrace(applicationContext.GetContainer().SingletonLazy(func() *view.EngineRenderer {
		return &view.EngineRenderer{}
	}))

	err.HandleErrorWithStacktrace(applicationContext.GetContainer().SingletonLazy(func() core.ISessionStorage {
		return applicationContext.GetSessionStorage()
	}))

	err.HandleErrorWithStacktrace(applicationContext.GetContainer().SingletonLazy(func() core.IAuthStrategy {
		return applicationContext.GetAuthStrategy()
	}))

	applicationContext.RegisterValidator(validator.New())
}

func (thiz *AppProvider) RegisterProvider(provider core.IProvider) {
	rtProvider := reflect.TypeOf(provider)
	if rtProvider.Kind() == reflect.Ptr {
		rtProvider = rtProvider.Elem()
	}

	log.Log("").Infof("Provider \u001B[0;32m`%s`\u001B[0m is registering", rtProvider.Name())
	provider.InitProvider(thiz.applicationContext)
	log.Log("").Infof("Provider \u001B[0;32m`%s`\u001B[0m registered\n\n", rtProvider.Name())
}
