package main

import (
	"github.com/osbits/gorgany/app"

	fixtureprovider "github.com/osbits/gorgany/e2e/fixture-app/pkg/provider"
)

func main() {
	app.NewServerApp(fixtureprovider.NewBootstrapper()).Run()
}
