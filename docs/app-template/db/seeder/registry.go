// Package seeder holds reference data: rows every environment needs, production
// included. Development and e2e fixtures do not belong in All().
package seeder

import "github.com/osbits/gorgany/v2/app/core"

// All returns the seeders `cli db:seed` runs. Each runs once per Name().
func All() []core.ISeeder {
	return []core.ISeeder{
		WelcomeNoteSeeder{},
	}
}
