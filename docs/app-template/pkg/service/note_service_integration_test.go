//go:build integration

package service

import (
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db"
	"github.com/osbits/gorgany/v2/testsupport"

	"myapp/db/migration"
)

// TestMain lives in the tagged file only, so the untagged unit run is unaffected.
func TestMain(m *testing.M) {
	testsupport.AddMigration(migration.All()...)
	testsupport.Main(m)
}

func newNoteService(t *testing.T) *NoteService {
	t.Helper()
	// MustDatabase, not RequireDatabase: the integration tag is the opt-in, so a
	// missing engine here is a broken setup, not a reason to skip.
	database := testsupport.MustDatabase(t)

	databases := &db.DBContext{}
	databases.RegisterDataSource(core.DefaultKeyInRegistrar, database.DataSource())
	return &NoteService{DBContext: databases}
}

func TestNoteService_CreateThenFind(t *testing.T) {
	notes := newNoteService(t)

	created, err := notes.Create("groceries")
	if err != nil {
		t.Fatal(err)
	}
	found, err := notes.Find(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Title != "groceries" {
		t.Errorf("Find returned %q", found.Title)
	}
}

func TestNoteService_FindMissingIsNotFound(t *testing.T) {
	notes := newNoteService(t)

	if _, err := notes.Find("absent"); err == nil {
		t.Fatal("Find of a missing note returned no error")
	}
}
