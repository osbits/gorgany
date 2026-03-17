package fixtureseeder

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/util"

	fixturedomain "git.qix.sx/gorgany/gorgany.git/e2e/fixture-app/pkg/domain"
)

const (
	AdminPassword  = "password123"
	EditorPassword = "password123"
)

type FixtureSeeder struct{}

func NewFixtureSeeder() *FixtureSeeder {
	return &FixtureSeeder{}
}

func (s *FixtureSeeder) Name() string {
	return "seed_fixture_data"
}

func (s *FixtureSeeder) CollectInsertModels() []any {
	return []any{
		&fixturedomain.User{
			ID:           "user-admin",
			Username:     "admin",
			PasswordHash: mustHash(AdminPassword),
			Role:         core.UserRole("admin"),
		},
		&fixturedomain.User{
			ID:           "user-editor",
			Username:     "editor",
			PasswordHash: mustHash(EditorPassword),
			Role:         core.UserRole("editor"),
		},
		&fixturedomain.Tag{
			ID:   "tag-red",
			Name: "red",
		},
		&fixturedomain.Tag{
			ID:   "tag-blue",
			Name: "blue",
		},
		&fixturedomain.Tag{
			ID:   "tag-green",
			Name: "green",
		},
		&fixturedomain.Widget{
			ID:          "widget-seeded",
			Name:        "Seeded widget",
			Description: "Created by the fixture seeder",
			CreatedBy:   "user-admin",
		},
	}
}

func mustHash(raw string) string {
	hashed, err := util.HashWithSalt(raw)
	if err != nil {
		panic(err)
	}
	return hashed
}
