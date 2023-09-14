package provider

import (
	"gorgany/internal"
	"gorgany/log"
	"gorgany/proxy"
	"gorgany/view"
	"reflect"
)

func NewViewProvider() *ViewProvider {
	return &ViewProvider{}
}

type ViewProvider struct {
}

func (thiz *ViewProvider) InitProvider() {
	thiz.RegisterViewEngine(view.NewNativeEngine("./resource/view", "html"))
}

func (thiz *ViewProvider) RegisterViewEngine(engine proxy.IViewEngine) {
	rtEngine := reflect.TypeOf(engine)
	if rtEngine.Kind() == reflect.Ptr {
		rtEngine = rtEngine.Elem()
	}

	internal.GetFrameworkRegistrar().RegisterViewEngine(engine)
	log.Log("").Infof("%s is set as view engine", rtEngine.Name())
}
