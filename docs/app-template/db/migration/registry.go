// Package migration holds the schema history. Migrations are frozen: they import
// nothing from this module, so changing application code never rewrites history.
package migration

import "github.com/osbits/gorgany/v2/app/core"

// All returns every migration in the order `cli db:migrate up` applies them.
//
// Append only. The migrate command runs this list in order and records each Name()
// in the migrations table; it does not sort. A Name() must never change once
// released. File and struct names are free to change.
func All() []core.IMigration {
	return []core.IMigration{
		Migration20260926120000{},
	}
}
