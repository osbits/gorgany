//go:build e2e

package e2e

import (
	"net/http"
	"testing"
)

// These checks hold for every application, whatever its features.

func TestHealth(t *testing.T) {
	base := live(t)

	r := call(t, http.MethodGet, base+"/healthz", nil, nil)
	if r.status != http.StatusOK || decode(t, r.body).StatusCode != "SUCCESS" {
		t.Fatalf("GET /healthz = %d", r.status)
	}
}

func TestUnknownRouteIsA404Envelope(t *testing.T) {
	base := live(t)

	r := call(t, http.MethodGet, base+"/api/v1/no-such-route", nil, nil)
	if r.status != http.StatusNotFound || decode(t, r.body).StatusCode != "NOT_FOUND" {
		t.Fatalf("GET an unknown route = %d %s", r.status, r.body)
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	base := live(t)

	r := call(t, http.MethodGet, base+"/healthz", nil, nil)
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options"} {
		if r.header.Get(h) == "" {
			t.Errorf("response has no %s header", h)
		}
	}
}
