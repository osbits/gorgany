// Command app is the HTTP server.
//
// Start it with the project root as the working directory: the framework reads
// .env, config/config.* and resource/ relative to it. In the image that is
// WORKDIR /app.
//
//	app              serve HTTP on app.server.port
//	app healthcheck  ask the running server whether it is ready; exit 0 or 1
package main

import (
	"os"

	// The IANA zone database, compiled in: TZ and time.LoadLocation work the same in
	// a scratch image as on a laptop.
	_ "time/tzdata"

	"github.com/osbits/gorgany/v2/app"

	"myapp/pkg/health"
	"myapp/pkg/provider"
)

func main() {
	// The container probe must not boot the application: a boot opens the database
	// pools and runs every provider, and a HEALTHCHECK does that every few seconds.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(health.Probe())
	}

	app.NewServerApp(provider.NewServerBootstrapper()).Run()
}
