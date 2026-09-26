//go:build e2e

// Package e2e drives the built application over HTTP, the way a client does. It
// imports nothing from pkg/: what it knows about the app comes from the API.
package e2e

import (
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// reached counts the tests that got past live(). With E2E_REQUIRE_LIVE=1 a run in
// which nothing reached the stack is a failure, not a green build: it means a
// wrong -run pattern, a missing build tag, or an unset E2E_BASE_URL.
var reached atomic.Int32

func TestMain(m *testing.M) {
	code := m.Run()
	if os.Getenv("E2E_REQUIRE_LIVE") == "1" && reached.Load() == 0 {
		fmt.Fprintln(os.Stderr, "e2e: E2E_REQUIRE_LIVE=1 but no test reached the application")
		code = 1
	}
	os.Exit(code)
}

// live returns the base URL once the app answers /readyz. Without E2E_BASE_URL the
// test is skipped, unless E2E_REQUIRE_LIVE=1 turns that skip into a failure.
func live(t *testing.T) string {
	t.Helper()
	base := os.Getenv("E2E_BASE_URL")
	if base == "" {
		if os.Getenv("E2E_REQUIRE_LIVE") == "1" {
			t.Fatal("E2E_BASE_URL is unset")
		}
		t.Skip("E2E_BASE_URL is unset; run test/e2e/run.sh")
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		resp, err := http.Get(base + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s/readyz did not answer 200 within 45s (last error: %v)", base, err)
		}
		time.Sleep(time.Second)
	}
	reached.Add(1)
	return base
}
