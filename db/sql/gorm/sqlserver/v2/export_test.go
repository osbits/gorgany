package v2

import "testing"

// resetAuthenticators empties the authenticator registry for the rest of t, and restores what
// was registered when t ends, so a test can register fakes without leaking them into another.
func resetAuthenticators(t *testing.T) {
	t.Helper()

	authMu.Lock()
	saved := authenticators
	authenticators = map[string]Authenticator{}
	authMu.Unlock()

	t.Cleanup(func() {
		authMu.Lock()
		authenticators = saved
		authMu.Unlock()
	})
}
