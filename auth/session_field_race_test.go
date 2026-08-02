package auth

import (
	"sync"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/require"
)

// The session object is shared between every concurrent request carrying the same
// cookie, so each of these drives two requests at one session the way the framework
// does. They assert nothing beyond "no race": the failure they exist to catch is
// undefined behaviour on an authorization field, which the detector reports and an
// assertion cannot.

func bothSessionTypes(t *testing.T, run func(t *testing.T, session core.ISession)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) {
		run(t, NewSession("sid", time.Now().Add(time.Hour)))
	})

	t.Run("database", func(t *testing.T) {
		run(t, liveRow("sid", ""))
	})
}

// TestSessionUserIdIsRaceFree. The reachable writer is the login handler, which
// overwrites the user id of a session another request is authorizing against.
func TestSessionUserIdIsRaceFree(t *testing.T) {
	bothSessionTypes(t, func(t *testing.T, session core.ISession) {
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				session.SetUserId("user-1")
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_ = session.GetUserId()
			}
		}()

		wg.Wait()
	})
}

// TestSessionLastActivityIsRaceFree. SessionMiddleware writes this field on every
// request and ShouldRotateSession reads it on every request, so a torn three-word
// time.Time drives the rotation decision.
func TestSessionLastActivityIsRaceFree(t *testing.T) {
	bothSessionTypes(t, func(t *testing.T, session core.ISession) {
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				session.SetLastActivity(time.Now())
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				_ = session.GetLastActivity()
			}
		}()

		wg.Wait()
	})
}

// TestSessionAttributesSurvivePersistenceOfAConcurrentRequest.
//
// The attribute map is handed to the driver for marshalling while another request
// writes it. The observed failure is `fatal error: concurrent map iteration and map
// write`, which aborts the process rather than the request — RecoveryMiddleware cannot
// catch it — so it cannot be asserted from inside a test. What is asserted here is the
// race that precedes it, which the detector reports; run this package with -race.
func TestSessionAttributesSurvivePersistenceOfAConcurrentRequest(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	storage := newDbStorage(repo)

	session := sessionById(t, storage, "sid")
	require.NotNil(t, session)

	var wg sync.WaitGroup
	wg.Add(2)

	// One request plants a flash value and clears it again, which is what
	// RedirectWithFlash and Message.Close do on any request carrying one.
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			session.SetItem(core.OneTimeSessionAttributeKey, `{"start":true}`)
			session.ClearItem(core.OneTimeSessionAttributeKey)
		}
	}()

	// The other slides the expiry window forward, which persists the whole session.
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			session.SetExpiry(time.Now().Add(time.Hour))
		}
	}()

	wg.Wait()
}
