// provider/route_provider.go
package provider

import (
	"fmt"
	"github.com/osbits/gorgany/v2/app/core"
	err2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/http"
	"github.com/osbits/gorgany/v2/http/controller"
	"github.com/osbits/gorgany/v2/http/middleware"
	"github.com/osbits/gorgany/v2/http/router"
	gohttp "net/http"
	"reflect"
)

type RouteProvider struct {
	controllers  []core.IController
	middlewares  []core.IMiddlewareConfig
	notFound     core.HandlerFunc
	skipRecovery bool
	skipCsrf     bool
}

func NewRouteProvider() *RouteProvider {
	return &RouteProvider{}
}

// DisableRecoveryMiddleware stops this provider from registering
// middleware.RecoveryMiddleware as the first /** filter.
//
// Recovery is on by default because nothing in the pipeline recovered before v2: a
// panic dropped the connection, and a registered error handler could never fire
// for a middleware that reports failure by panicking (JwtMiddleware does exactly
// that). Only disable it if the app installs its own recovery filter first.
func (p *RouteProvider) DisableRecoveryMiddleware() {
	p.skipRecovery = true
}

// DisableCsrfController stops this provider from registering
// controller.CsrfController at core.DefaultCSRFTokenPath.
//
// The endpoint is on by default because before v2.0 a client had no way to obtain a
// CSRF token: the token header was published on the one response that created a
// session and nowhere else, so any client that missed it was permanently unable to
// make a mutating request. Disable this only if the app mounts its own token endpoint,
// or mounts CsrfController itself with a different Path.
func (p *RouteProvider) DisableCsrfController() {
	p.skipCsrf = true
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

// standardMiddlewares returns the app's middlewares with the framework defaults
// prepended, so the recovery filter is registered first and therefore wraps
// everything else.
func (p *RouteProvider) standardMiddlewares() []core.IMiddlewareConfig {
	if p.skipRecovery {
		return p.middlewares
	}

	recovery := http.NewMiddlewareConfigBuilder().
		WithPattern("/**").
		AsFilter().
		WithMiddleware(middleware.NewRecoveryMiddleware()).
		Build()

	return append([]core.IMiddlewareConfig{recovery}, p.middlewares...)
}

// standardControllers returns the app's controllers with the framework defaults
// appended, so an app that mounts its own path at core.DefaultCSRFTokenPath wins the
// duplicate-route check rather than losing to a framework default.
func (p *RouteProvider) standardControllers() []core.IController {
	if p.skipCsrf {
		return p.controllers
	}

	return append(append([]core.IController{}, p.controllers...), controller.NewCsrfController())
}

func (p *RouteProvider) Boot(c core.IContainer) {
	_ = c.Invoke(func(wc core.IWebContext, grgRouter core.Router) {
		for _, mw := range p.standardMiddlewares() {
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
		for _, ctrl := range p.standardControllers() {
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
