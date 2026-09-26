package provider

import (
	"context"
	"io"

	"github.com/osbits/gorgany/v2/app/core"

	"myapp/pkg/service"
)

// AppProvider binds what the framework expects the application to supply, and the
// application's own services. It is the last shared provider, so its bindings win.
type AppProvider struct{}

func (AppProvider) Register(c core.IContainer) {
	// Both built-in auth strategies inject core.IUserService.
	must(c.SingletonLazy(func() core.IUserService { return &service.UserService{} }))

	// http.Message injects a renderer. An API-only app binds a no-op; an app that
	// renders templates adds grgprovider.NewViewProvider() to sharedProviders instead.
	must(c.SingletonLazy(func() core.IEngineRenderer { return noopRenderer{} }))

	must(c.SingletonLazy(func() *service.NoteService { return &service.NoteService{} }))
}

func (AppProvider) Boot(core.IContainer) {}

type noopRenderer struct{}

func (noopRenderer) DoRender(context.Context, io.Writer, string, map[string]any) error { return nil }
func (noopRenderer) RegisterGlobalFunction(string, any)                                {}
func (noopRenderer) RegisterGlobalVariable(string, any)                                {}

// must fails the boot. A binding that did not register is a wiring bug, and a
// process that starts without it fails later, on a request, far from the cause.
func must(err error) {
	if err != nil {
		panic(err)
	}
}
