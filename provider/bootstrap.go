package provider

import (
	"github.com/osbits/gorgany/v2/app/core"
)

type Bootstrapper struct {
	providers []core.IProvider
}

func NewGorganyBootstrapper() *Bootstrapper {
	return &Bootstrapper{
		providers: make([]core.IProvider, 0),
	}
}

func (thiz *Bootstrapper) Bootstrap(container core.IContainer) {
	for _, provider := range thiz.providers {
		provider.Register(container)
	}

	for _, provider := range thiz.providers {
		provider.Boot(container)
	}

}

func (thiz *Bootstrapper) AddProvider(provider core.IProvider) {
	thiz.providers = append(thiz.providers, provider)
}
