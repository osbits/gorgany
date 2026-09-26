// Package provider is the composition root: the only package that knows every
// other package, and where configuration is read (health.Probe's SERVER_PORT aside).
package provider

import (
	"github.com/osbits/gorgany/v2/app/core"
	grgprovider "github.com/osbits/gorgany/v2/provider"

	// The SQL engine this app speaks, linked for its registration side effect.
	// DbProvider registers no driver itself; without this import the app builds and
	// then refuses to boot.
	_ "github.com/osbits/gorgany/v2/db/sql/driver/postgres"
)

// NewServerBootstrapper is what cmd/app boots: the shared providers plus the job
// scheduler, which only the long-running server may start.
func NewServerBootstrapper() core.Bootstrapper {
	return deferred(func() []core.IProvider {
		return append(sharedProviders(), newJobProvider())
	})
}

// NewConsoleBootstrapper is what cmd/cli boots: the shared providers plus the
// console commands. It leaves JobProvider out on purpose: JobProvider.Boot starts
// the scheduler unconditionally, so a console that included it would run cron
// jobs in the middle of `db:migrate up`.
func NewConsoleBootstrapper() core.Bootstrapper {
	return deferred(func() []core.IProvider {
		return append(sharedProviders(), newCommandProvider())
	})
}

// sharedProviders is the order both binaries boot in. Every Register runs in this
// order, then every Boot in the same order.
func sharedProviders() []core.IProvider {
	return []core.IProvider{
		grgprovider.NewLoggerProvider(),
		newErrorProvider(),
		grgprovider.AppProvider{}, // auth context, session storage, the "default" and "api" strategies
		grgprovider.NewDbProvider(),
		newDomainProvider(),   // generatedDomains(), for db:diff and domains:register
		newDatabaseProvider(), // migration.All(), seeder.All()
		newRouteProvider(),    // the console needs it too: it binds IWebContext and RouteLinker
		&AppProvider{},        // last: the app's bindings win over the framework's
	}
}

// deferred builds the provider list inside Bootstrap. The framework calls
// Bootstrap only after it has loaded .env and config/config.*, so a provider
// constructor that reads configuration sees real values. The same constructor
// called as an argument to app.NewServerApp would run first and see nothing.
type deferred func() []core.IProvider

func (build deferred) Bootstrap(container core.IContainer) {
	bootstrapper := grgprovider.NewGorganyBootstrapper()
	for _, p := range build() {
		bootstrapper.AddProvider(p)
	}
	bootstrapper.Bootstrap(container)
}
