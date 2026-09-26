//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSeededReferenceDataIsServed(t *testing.T) {
	base := live(t)

	r := call(t, http.MethodGet, base+"/api/v1/notes/welcome", nil, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET the seeded note = %d", r.status)
	}
	var note struct{ Title string }
	if err := json.Unmarshal(decode(t, r.body).Body, &note); err != nil || note.Title != "Welcome" {
		t.Fatalf("seeded note = %+v (%v)", note, err)
	}
}

func TestMissingNoteIsA404Envelope(t *testing.T) {
	base := live(t)

	r := call(t, http.MethodGet, base+"/api/v1/notes/no-such-note", nil, nil)
	if r.status != http.StatusNotFound || decode(t, r.body).StatusCode != "NOT_FOUND" {
		t.Fatalf("GET a missing note = %d %s", r.status, r.body)
	}
}
