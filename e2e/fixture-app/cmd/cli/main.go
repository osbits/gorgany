package main

import (
	"git.qix.sx/gorgany/gorgany.git/app"

	fixtureprovider "git.qix.sx/gorgany/gorgany.git/e2e/fixture-app/pkg/provider"
)

func main() {
	app.NewConsoleApp(fixtureprovider.NewBootstrapper()).Run()
}
