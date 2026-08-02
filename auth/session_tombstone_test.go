package auth

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both stores remember the identifiers they revoked, so a caller still holding the
// session object cannot write it back. Nothing pinned that, and the two tests that look
// as though they do pass without it: the database half is caught by the row check on the
// way in, and the memory half stopped being reachable when the CSRF endpoint's upsert was
// removed. So the memory of a revocation was carrying no test at all, and its cost — an
// entry per revocation held for a day — was being paid for nothing.

// TestARevokedSessionIsRefusedByTheStoreThatRevokedIt.
//
// The refusal has to come from this process's own memory of the revocation, because the
// repository cannot supply it: an UPDATE keyed on a row that is gone matches nothing and
// reports success, which is exactly how a request already inside its round trip used to
// put a revoked session back into the cache.
func TestARevokedSessionIsRefusedByTheStoreThatRevokedIt(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	mediator := NewDbSessionMediator(repo)

	session, err := mediator.GetSession("sid")
	require.NoError(t, err)
	require.NotNil(t, session)

	deleted, err := mediator.DeleteSession("sid")
	require.NoError(t, err)
	require.True(t, deleted, "the store held the session")

	assert.Error(t, mediator.UpdateSession(session),
		"a session this process revoked must not be persisted again")

	_, err = mediator.CreateSession(session)
	assert.Error(t, err, "and it must not be recreated either")

	mediator.mu.RLock()
	_, cached := mediator.cache["sid"]
	mediator.mu.RUnlock()
	assert.False(t, cached, "neither refusal may leave the session cached")
}

// TestARevokedMemorySessionCannotBeStoredAgain is the same property for the default
// backend, which has no row to fall back on: MemorySession.AddSession is a map insert, so
// the memory of the revocation is the only thing standing between a stale session object
// and a store that holds it again, user id intact.
func TestARevokedMemorySessionCannotBeStoredAgain(t *testing.T) {
	storage := NewMemorySession(time.Hour)

	session := NewSession("sid", time.Now().Add(time.Hour))
	session.SetUserId("user-9")
	require.NoError(t, storage.AddSession(session))
	require.NoError(t, storage.DeleteSessionById("sid"))

	assert.Error(t, storage.AddSession(session),
		"a session this store revoked must not be stored again")
	assert.Nil(t, sessionById(t, storage, "sid"),
		"and it must not resolve afterwards")
}

// TestRevokingASessionTheStoreNeverHeldRemembersNothing.
//
// Revoking is idempotent and answers "there was nothing there" without an error, and both
// stores used to write a tombstone for that case anyway — one entry, keyed by whatever
// identifier the caller passed, held for SessionTombstoneRetention. Nothing was revoked,
// so nothing can be resurrected and there is nothing for the entry to protect; what it
// does instead is turn a request into a day of retained memory. The identifier comes
// straight off the session cookie (StandardAuthStrategy.Logout revokes whatever
// ResolveSessionId returns), so on an app that has not put its logout route behind the
// CSRF middleware the map key, and its size, are chosen by an unauthenticated visitor.
// That is the same unbounded growth the session sweep and the rate-limit store were given
// bounds for in this same release.
func TestRevokingASessionTheStoreNeverHeldRemembersNothing(t *testing.T) {
	// Roughly what fits in a cookie, to make the point that the cost is per byte the
	// caller chose and not per session the app has.
	unknownIds := make([]string, 0, 500)
	padding := strings.Repeat("x", 3000)
	for i := 0; i < 500; i++ {
		unknownIds = append(unknownIds, fmt.Sprintf("%s-%d", padding, i))
	}

	t.Run("memory", func(t *testing.T) {
		storage := NewMemorySession(time.Hour)
		for _, id := range unknownIds {
			require.NoError(t, storage.DeleteSessionById(id))
		}

		storage.mu.Lock()
		remembered := len(storage.tombstones)
		storage.mu.Unlock()

		assert.Zero(t, remembered,
			"revoking sessions the store never held must not retain anything")
	})

	t.Run("database", func(t *testing.T) {
		repo := newFakeSessionRepo()
		storage := newDbStorage(repo)
		for _, id := range unknownIds {
			require.NoError(t, storage.DeleteSessionById(id))
		}

		storage.mediator.mu.RLock()
		remembered := len(storage.mediator.tombstones)
		storage.mediator.mu.RUnlock()

		assert.Zero(t, remembered,
			"revoking sessions the store never held must not retain anything")
	})
}

// TestARevocationThatFailedIsStillRemembered. A delete that could not be performed is
// not a delete that found nothing: the session is still in the store, this process was
// told to revoke it, and the caller is being told the logout failed. Refusing to serve it
// here is the fail-closed direction, so the intent has to be remembered even though the
// store still holds the row.
func TestARevocationThatFailedIsStillRemembered(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	mediator := NewDbSessionMediator(repo)

	session, err := mediator.GetSession("sid")
	require.NoError(t, err)
	require.NotNil(t, session)

	repo.deleteErr = assert.AnError
	_, err = mediator.DeleteSession("sid")
	require.Error(t, err)

	require.True(t, repo.has("sid"), "the row is still there — the delete failed")
	assert.Error(t, mediator.UpdateSession(session),
		"a session this process was told to revoke must not keep being written back")

	resolved, err := mediator.GetSession("sid")
	require.NoError(t, err)
	assert.Nil(t, resolved, "nor keep resolving")
}
