package provider

import (
	grgprovider "github.com/osbits/gorgany/v2/provider"

	"myapp/pkg/command"
)

// newCommandProvider is the framework's built-in commands (db:migrate, db:seed,
// db:diff, domains:register, session:gc) plus the app's own. Console only. A command
// registered here under a built-in's name replaces the built-in, as `version` does.
func newCommandProvider() *grgprovider.CommandProvider {
	p := grgprovider.NewCommandProvider()
	p.AddCommand(&command.VersionCommand{})
	return p
}
