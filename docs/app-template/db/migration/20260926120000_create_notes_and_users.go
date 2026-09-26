package migration

import (
	"github.com/osbits/gorgany/v2/app/core"
	"gorm.io/gorm"
)

// Migration20260926120000 creates the notes and users tables.
type Migration20260926120000 struct{}

func (Migration20260926120000) Name() string { return "20260926_120000.create_notes_and_users" }

func (Migration20260926120000) Up() core.MigrationClosure {
	return func(db *gorm.DB) error {
		return db.Exec(`
			CREATE TABLE IF NOT EXISTS notes (
				id         text PRIMARY KEY,
				title      text NOT NULL,
				created_at timestamptz NOT NULL DEFAULT now()
			);
			CREATE TABLE IF NOT EXISTS users (
				id            text PRIMARY KEY,
				username      text NOT NULL UNIQUE,
				password_hash text NOT NULL,
				role          text NOT NULL
			);`).Error
	}
}

func (Migration20260926120000) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		return db.Exec(`DROP TABLE IF EXISTS notes; DROP TABLE IF EXISTS users;`).Error
	}
}
