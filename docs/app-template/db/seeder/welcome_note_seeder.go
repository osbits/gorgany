package seeder

import "myapp/pkg/domain"

type WelcomeNoteSeeder struct{}

func (WelcomeNoteSeeder) Name() string { return "welcome_note" }

func (WelcomeNoteSeeder) CollectInsertModels() []any {
	return []any{&domain.Note{ID: "welcome", Title: "Welcome"}}
}
