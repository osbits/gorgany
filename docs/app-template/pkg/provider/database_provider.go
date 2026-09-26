package provider

import (
	"github.com/osbits/gorgany/v2/app/core"

	"myapp/db/migration"
	"myapp/db/seeder"
)

// databaseProvider registers the migrations and seeders. The lists live in
// db/migration and db/seeder, not here, so the tests run the same list.
type databaseProvider struct{}

func newDatabaseProvider() *databaseProvider { return &databaseProvider{} }

func (*databaseProvider) Register(core.IContainer) {}

func (*databaseProvider) Boot(c core.IContainer) {
	must(c.Invoke(func(data core.IDataContext) {
		for _, m := range migration.All() {
			data.AddMigration(m)
		}
		for _, s := range seeder.All() {
			data.AddSeeder(s)
		}
	}))
}
