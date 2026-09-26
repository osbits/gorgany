//go:build e2e

package e2e

import (
	"context"
	"testing"
)

// TestMigrationsAndSeedersRanOnce checks the bookkeeping `cli db:migrate up` and
// `cli db:seed` left behind. run.sh migrates twice, so a count above one means the
// migration was not recognised as applied.
func TestMigrationsAndSeedersRanOnce(t *testing.T) {
	live(t)
	db := database(t)

	for query, want := range map[string]int{
		`SELECT count(*) FROM migrations WHERE name = '20260926_120000.create_notes_and_users'`: 1,
		`SELECT count(*) FROM seeders WHERE name = 'welcome_note'`:                              1,
	} {
		var got int
		if err := db.QueryRow(context.Background(), query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
}
