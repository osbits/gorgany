package provider

import (
	"github.com/osbits/gorgany/v2/app/core"
)

// domainProvider registers every entity in pkg/domain with the framework, which
// db:diff and domains:register read. The list is generatedDomains() in domains.go,
// which `go run ./cmd/cli domains:register` rewrites when it finds a new entity.
// Remove a deleted entity's line by hand first (the console does not compile while
// the file names a missing type); change nothing else in it.
type domainProvider struct{}

func newDomainProvider() *domainProvider { return &domainProvider{} }

func (*domainProvider) Register(core.IContainer) {}

func (*domainProvider) Boot(c core.IContainer) {
	must(c.Invoke(func(domains core.IDomainContext) {
		for key, entity := range generatedDomains() {
			domains.RegisterDomain(key, entity)
		}
	}))
}
