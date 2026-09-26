package provider

import (
	"github.com/osbits/gorgany/v2/app/core"

	v1 "myapp/pkg/controller/api/v1"
	"myapp/pkg/health"
)

// controllers is every controller the app serves. It is a plain function, not a
// container lookup, so the route-contract tests can walk it without booting
// anything. A generated app starts from GeneratedControllers() and appends its
// hand-written controllers here.
func controllers() []core.IController {
	return []core.IController{
		health.NewController(),
		v1.NewNoteController(),
	}
}
