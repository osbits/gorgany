package fixturemigration

import (
	"github.com/osbits/gorgany/app/core"
	"gorm.io/gorm"

	fixturedomain "github.com/osbits/gorgany/e2e/fixture-app/pkg/domain"
)

type FixtureMigration struct{}

func NewFixtureMigration() *FixtureMigration {
	return &FixtureMigration{}
}

func (m *FixtureMigration) Name() string {
	return "create_fixture_tables"
}

func (m *FixtureMigration) Up() core.MigrationClosure {
	return func(db *gorm.DB) error {
		return db.AutoMigrate(&fixturedomain.User{}, &fixturedomain.Tag{}, &fixturedomain.Widget{})
	}
}

func (m *FixtureMigration) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		return db.Migrator().DropTable("fixture_widget_tags", &fixturedomain.Widget{}, &fixturedomain.Tag{}, &fixturedomain.User{})
	}
}
