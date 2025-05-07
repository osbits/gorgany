// provider/route_provider.go
package provider

import (
	"fmt"
	gohttp "net/http"
	"reflect"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/http"
	"git.qix.sx/gorgany/gorgany.git/http/router"
	"github.com/go-chi/chi"
)

type RouteProvider struct {
	routerCtor  func() core.Router
	controllers []core.IController
	middlewares []core.IMiddlewareConfig
	notFound    core.HandlerFunc
}

func NewRouteProvider() *RouteProvider {
	return &RouteProvider{}
}

func (p *RouteProvider) AddController(ctrl core.IController) {
	p.controllers = append(p.controllers, ctrl)
}

func (p *RouteProvider) AddMiddleware(mw core.IMiddlewareConfig) {
	p.middlewares = append(p.middlewares, mw)
}

func (p *RouteProvider) SetNotFoundHandler(h core.HandlerFunc) {
	p.notFound = h
}

func (p *RouteProvider) Register(c core.IContainer) {
	c.SingletonLazy(func() gohttp.Handler {
		return &http.Dispatcher{}
	})
	c.SingletonLazy(func() core.Router {
		return router.NewGorganyRouter()
	})

	c.SingletonLazy(func(r core.Router) core.IWebContext {
		wc := &http.WebContext{}
		wc.SetRouter(r)
		return wc
	})

	c.TransientLazy(func() *http.Message {
		return &http.Message{}
	})
}

func (p *RouteProvider) Boot(c core.IContainer) {
	_ = c.Invoke(func(wc core.IWebContext) {

		for _, mw := range p.middlewares {
			middleware := mw.GetMiddleware()
			err := c.Make(middleware)
			if err != nil {
				err2.HandleError(err)
			}

			configWithInjectedMiddleware := http.NewMiddlewareConfigBuilder().
				WithPattern(mw.GetPattern()).
				WithApplyOn404(mw.GetApplyOn404()).
				WithMiddleware(middleware).
				WithExcludePattern(mw.GetExcludePattern()).
				Build()

			wc.AddMiddleware(configWithInjectedMiddleware)
		}

		wc.SetNotFound(p.notFound)

		wc.SetNewMessage(func(w gohttp.ResponseWriter, r *gohttp.Request) (core.HttpMessage, error) {
			msg := &http.Message{}
			if err := c.Make(msg, map[string]interface{}{"writer": w, "request": r}); err != nil {
				return nil, fmt.Errorf("cannot make Message: %w", err)
			}
			msg.SetSession()
			return msg, nil
		})
		wc.SetNewInputResolver(func(h core.HandlerFunc, m core.HttpMessage) (interface{}, error) {
			res := &http.InputResolver{ReflectedHandler: reflect.ValueOf(h), Message: m}
			_ = c.Make(res)
			return res, nil
		})

		engine := wc.GetRouter().Engine().(chi.Router)
		for _, ctrl := range p.controllers {
			err := c.Make(ctrl)
			if err != nil {
				err2.HandleError(err)
			}
			wc.AddController(ctrl)
			for _, rc := range ctrl.GetRoutes() {
				cfg := rc.(*router.RouteConfig)
				engine.MethodFunc(string(cfg.Method), cfg.Pattern(), func(w gohttp.ResponseWriter, r *gohttp.Request) {})
				engine.Options(cfg.Pattern(), func(w gohttp.ResponseWriter, r *gohttp.Request) {})
			}
		}
		wc.CacheRoutes()

		var dispatcher gohttp.Handler
		c.Resolve(&dispatcher)
		wc.GetRouter().(*router.GorganyRouter).SetEntryPoint(dispatcher)
	})
}
