package provider

import (
	"github.com/gorganyio/gorgany/app/core"
	grgerr "github.com/gorganyio/gorgany/err"
	"github.com/gorganyio/gorgany/log"
	"github.com/gorganyio/gorgany/view"
)

type ViewProvider struct {
	dir, ext string
}

func NewViewProvider() *ViewProvider {
	return &ViewProvider{dir: "./resource/view", ext: "gohtml"}
}

func NewViewProviderWithConfig() *ViewProvider {
	return &ViewProvider{}
}

func (p *ViewProvider) SetDir(dir string) {
	p.dir = dir
}

func (p *ViewProvider) SetExt(ext string) {
	p.ext = ext
}

func (p *ViewProvider) Register(c core.IContainer) {
	c.SingletonLazy(func() core.IViewEngine {
		return view.NewNativeEngine(p.dir, p.ext)
	})

	c.SingletonLazy(func() core.IEngineRenderer {
		return &view.EngineRenderer{}
	})
}

func (p *ViewProvider) Boot(c core.IContainer) {
	err := c.Invoke(func(engine core.IViewEngine) {
		log.Log("").Infof("View engine initialized: %T", engine)
	})
	if err != nil {
		grgerr.HandleError(err)
	}
}
