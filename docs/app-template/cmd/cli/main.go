// Command cli runs one console command and exits.
//
//	cli db:migrate up [--datasource=name]
//	cli db:migrate down [--datasource=name] [--steps=n]
//	cli db:seed
//	cli version                         the version the build stamped
//	cli <area>:<verb> [--flag=value]    the app's own commands, in pkg/command
//
// Same working-directory rule as cmd/app. db:migrate and db:seed exit non-zero on
// failure. db:diff and session:gc print the failure and exit 0, so a job that runs
// them checks their output as well as the exit status.
package main

import (
	_ "time/tzdata"

	"github.com/osbits/gorgany/v2/app"

	"myapp/pkg/provider"
)

func main() {
	app.NewConsoleApp(provider.NewConsoleBootstrapper()).Run()
}
