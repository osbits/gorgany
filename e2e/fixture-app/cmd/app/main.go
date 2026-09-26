package main

import (
	"github.com/osbits/gorgany/v2/app"

	fixtureprovider "github.com/osbits/gorgany/v2/e2e/fixture-app/pkg/provider"
)

func main() {
	app.NewServerApp(fixtureprovider.NewBootstrapper()).Run()
}
