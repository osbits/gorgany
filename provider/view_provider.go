package provider

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/view"
	"reflect"
)

func NewViewProvider() *ViewProvider {
	return &ViewProvider{}
}

type ViewProvider struct {
	applicationContext core.IApplicationContext
}

func (thiz *ViewProvider) InitProvider(applicationContext core.IApplicationContext) {
	thiz.applicationContext = applicationContext

	thiz.RegisterViewEngine(view.NewNativeEngine("./resource/view", "gohtml"))
}

func (thiz *ViewProvider) RegisterViewEngine(engine core.IViewEngine) {
	rtEngine := reflect.TypeOf(engine)
	if rtEngine.Kind() == reflect.Ptr {
		rtEngine = rtEngine.Elem()
	}

	thiz.applicationContext.RegisterViewEngine(engine)
	log.Log("").Infof("%s is set as view engine", rtEngine.Name())
}
