package migration

import (
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"gorm.io/gorm"
)

// SessionsMigration creates the sessions table
type SessionsMigration struct{}

func NewSessionsMigration() *SessionsMigration {
	return &SessionsMigration{}
}

func (m *SessionsMigration) Name() string {
	return "create_sessions_table"
}

func (m *SessionsMigration) Up() core.MigrationClosure {
	return func(db *gorm.DB) error {
		return db.Exec(`
			CREATE TABLE IF NOT EXISTS sessions (
				id VARCHAR(255) PRIMARY KEY,
				user_id VARCHAR(255),
				expiry TIMESTAMP NOT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT NOW(),
				last_activity TIMESTAMP NOT NULL DEFAULT NOW(),
				attributes JSONB DEFAULT '{}'::jsonb
			);
			
			-- Create index on expiry for faster cleanup
			CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expiry);
			
			-- Create index on user_id for faster user session lookups
			CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
		`).Error
	}
}

func (m *SessionsMigration) Down() core.MigrationClosure {
	return func(db *gorm.DB) error {
		return db.Exec(`
			DROP TABLE IF EXISTS sessions CASCADE;
		`).Error
	}
}
