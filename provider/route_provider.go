// provider/route_provider.go
package provider

import (
	"fmt"
	"github.com/gorganyio/gorgany/app/core"
	err2 "github.com/gorganyio/gorgany/err"
	"github.com/gorganyio/gorgany/http"
	"github.com/gorganyio/gorgany/http/router"
	gohttp "net/http"
	"reflect"
)

type RouteProvider struct {
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
	c.SingletonLazy(func() core.IWebContext {
		return &http.WebContext{}
	})

	c.SingletonLazy(func() core.Router {
		return &router.ChiRouterAdapter{}
	})

	c.SingletonLazy(func(r core.Router) core.RouteLinker {
		return r
	})
}

func (p *RouteProvider) Boot(c core.IContainer) {
	_ = c.Invoke(func(wc core.IWebContext, grgRouter core.Router) {
		for _, mw := range p.middlewares {
			m := mw.GetMiddleware()
			if err := c.Make(m); err != nil {
				err2.HandleError(err)
			}
			grgRouter.RegisterMiddleware(mw)
		}

		wc.SetNotFound(p.notFound)

		wc.SetNewMessage(func(w gohttp.ResponseWriter, r *gohttp.Request) (core.HttpMessage, error) {
			msg := &http.Message{}
			if err := c.Make(msg, map[string]interface{}{"writer": w, "request": r}); err != nil {
				return nil, fmt.Errorf("cannot make Message: %w", err)
			}
			return msg, nil
		})

		wc.SetNewInputResolver(func(h core.HandlerFunc, m core.HttpMessage) (interface{}, error) {
			res := &http.InputResolver{ReflectedHandler: reflect.ValueOf(h), Message: m}
			_ = c.Make(res)
			return res, nil
		})

		chiAdapter := grgRouter.(*router.ChiRouterAdapter)
		for _, ctrl := range p.controllers {
			if err := c.Make(ctrl); err != nil {
				err2.HandleError(err)
			}
			wc.AddController(ctrl)
			for _, rc := range ctrl.GetRoutes() {
				for i := range rc.GetMiddlewares() {
					m := rc.GetMiddlewares()[i]
					if err := c.Make(m); err != nil {
						err2.HandleError(err)
					}
				}

				chiAdapter.RegisterRoute(rc)
			}
		}
	})
}
