package provider

import (
	"fmt"
	"github.com/go-chi/chi"
	gohttp "net/http"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	grgerr "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/http"
	"git.qix.sx/gorgany/gorgany.git/http/router"
)

type RouteProvider struct {
	routerCtor  func() core.Router
	controllers []core.IController
	middleware  []core.IMiddleware
	notFound    core.HandlerFunc
}

func NewRouteProvider() *RouteProvider {
	return &RouteProvider{}
}

func (p *RouteProvider) AddController(ctrl core.IController) {
	p.controllers = append(p.controllers, ctrl)
}

func (p *RouteProvider) AddMiddleware(mw core.IMiddleware) {
	p.middleware = append(p.middleware, mw)
}

func (p *RouteProvider) SetNotFoundHandler(h core.HandlerFunc) {
	p.notFound = h
}

func (p *RouteProvider) Register(c core.IContainer) {
	c.SingletonLazy(func() core.Router {
		return router.NewGorganyRouter()
	})

	c.SingletonLazy(func(r core.Router) core.IWebContext {
		wc := &http.WebContext{}
		wc.SetRouter(r)
		return wc
	})
}

func (p *RouteProvider) Boot(c core.IContainer) {
	err := c.Invoke(func(wc core.IWebContext) {
		for _, ctrl := range p.controllers {
			if err := c.Make(ctrl); err != nil {
				panic(fmt.Errorf("route Boot: inject controller %T: %w", ctrl, err))
			}
			wc.AddController(ctrl)
		}
		for _, mw := range p.middleware {
			if err := c.Make(mw); err != nil {
				panic(fmt.Errorf("route Boot: inject middleware %T: %w", mw, err))
			}
			wc.AddMiddleware(mw)
		}
		wc.SetNotFound(p.notFound)

		wc.SetNewMessage(func(w gohttp.ResponseWriter, r *gohttp.Request) (core.HttpMessage, error) {
			msg := &http.Message{}
			if err := c.Make(msg, map[string]interface{}{
				"writer":  w,
				"request": r,
			}); err != nil {
				return nil, fmt.Errorf("dispatch: cannot make HTTPMessage: %w", err)
			}
			msg.SetSession()
			return msg, nil
		})

		engine := wc.GetRouter().Engine().(chi.Router)
		for _, ctrl := range wc.GetControllers() {
			for _, rc := range ctrl.GetRoutes() {
				cfg := rc.(*router.RouteConfig)
				method, pattern, handler := cfg.Method, cfg.Pattern(), cfg.Handler
				engine.MethodFunc(string(method), pattern, func(w gohttp.ResponseWriter, r *gohttp.Request) {
					http.Dispatch(wc, w, r, handler)
				})
			}
		}
		if wc.GetNotFound() != nil {
			engine.NotFound(func(w gohttp.ResponseWriter, r *gohttp.Request) {
				http.Dispatch(wc, w, r, wc.GetNotFound())
			})
		}
	})

	if err != nil {
		grgerr.HandleError(err)
	}
}
