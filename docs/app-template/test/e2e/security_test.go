//go:build e2e

package e2e

import (
	"net/http"
	"testing"
)

func TestMutatingWithoutACsrfTokenIsForbidden(t *testing.T) {
	base := live(t)

	r := call(t, http.MethodPost, base+"/api/v1/notes", map[string]string{"title": "forged"}, nil)
	if r.status != http.StatusForbidden {
		t.Fatalf("POST without a CSRF token = %d, want 403", r.status)
	}
}

func TestCrossOriginMutationIsForbidden(t *testing.T) {
	base := live(t)
	b := newBrowser(t, base)

	header := b.header()
	header.Set("Origin", "https://attacker.example")
	r := call(t, http.MethodPost, base+"/api/v1/notes", map[string]string{"title": "forged"}, header)
	if r.status != http.StatusForbidden {
		t.Fatalf("cross-origin POST = %d, want 403", r.status)
	}
}

func TestCreatingANoteRequiresSignIn(t *testing.T) {
	base := live(t)
	b := newBrowser(t, base)

	// A valid session and token, but nobody signed in: the CSRF check passes and the
	// route's AuthMiddleware refuses.
	r := call(t, http.MethodPost, base+"/api/v1/notes", map[string]string{"title": "anonymous"}, b.header())
	if r.status != http.StatusUnauthorized {
		t.Fatalf("signed-out POST = %d, want 401", r.status)
	}
}
