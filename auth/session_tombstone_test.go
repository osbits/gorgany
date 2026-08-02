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

// TestARevocationThatFailedIsRetriedNotJustRemembered.
//
// This replaces TestARevocationThatFailedIsStillRemembered, which asserted the three
// properties below and stopped there — and stopping there was the defect. Remembering makes
// this process refuse the identifier, which is correct and fail-closed; but the memory was
// kept as a *tombstone*, indistinguishable from "the row is gone". So the next request
// resolved nothing, minted a replacement session, and wrote its cookie over the client's only
// copy of the id that still needed revoking. The user pressed "try again", revoked the
// replacement, and the original row stayed live — on this replica and, with no local record at
// all, on every other one.
//
// The memory has to carry the obligation as well as the refusal, and something has to
// discharge it.
func TestARevocationThatFailedIsRetriedNotJustRemembered(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	mediator := NewDbSessionMediator(repo)

	session, err := mediator.GetSession("sid")
	require.NoError(t, err)
	require.NotNil(t, session)

	repo.deleteErr = assert.AnError
	_, err = mediator.DeleteSession("sid")
	require.Error(t, err)

	// The three properties the previous test asserted, unchanged.
	require.True(t, repo.has("sid"), "the row is still there — the delete failed")
	assert.Error(t, mediator.UpdateSession(session),
		"a session this process was told to revoke must not keep being written back")
	resolved, err := mediator.GetSession("sid")
	require.NoError(t, err)
	assert.Nil(t, resolved, "nor keep resolving")

	// And the ones that make the refusal recoverable rather than terminal.
	assert.True(t, mediator.RevocationPending("sid"),
		"the obligation has to be recorded as still owed, not as already done")

	mediator.mu.RLock()
	_, tombstoned := mediator.tombstones["sid"]
	mediator.mu.RUnlock()
	assert.False(t, tombstoned,
		"recording it as a tombstone is what claimed the row was gone while it was live")

	// The store comes back and the client presents the same identifier again.
	repo.deleteErr = nil
	withoutRetryBackoff(t)
	_, err = mediator.GetSession("sid")
	require.NoError(t, err)

	assert.False(t, repo.has("sid"),
		"the retry has to actually delete the row the failed logout left behind")
	assert.False(t, mediator.RevocationPending("sid"), "and the obligation is discharged")

	mediator.mu.RLock()
	_, tombstoned = mediator.tombstones["sid"]
	mediator.mu.RUnlock()
	assert.True(t, tombstoned, "now it is genuinely a tombstone")
}

// withoutRetryBackoff removes the per-id retry backoff for the rest of the test.
//
// The backoff exists so a client hammering an unreachable database does not turn one failed
// logout into a retry storm. A test that is about the retry itself would otherwise have to
// sleep a second to see it.
func withoutRetryBackoff(t *testing.T) {
	t.Helper()

	previous := pendingRevocationRetry
	pendingRevocationRetry = 0
	t.Cleanup(func() { pendingRevocationRetry = previous })
}

// TestAClientWhoseRevocationFailedKeepsTheIdentifierItMustPresentAgain is the other half, and
// the one that closes the loop: withholding the replacement is what keeps the client able to
// name the session that still has to go.
func TestAClientWhoseRevocationFailedKeepsTheIdentifierItMustPresentAgain(t *testing.T) {
	repo := newFakeSessionRepo()
	storage := newDbStorage(repo)
	repo.put(liveRow("stuck", "user-1"))

	require.Error(t, func() error {
		repo.deleteErr = assert.AnError
		return storage.DeleteSessionById("stuck")
	}())

	strategy := &StandardAuthStrategy{
		sessionManager: storage,
		csrfService:    &CsrfService{},
		sessionFactory: func() ISessionFactory {
			factory := NewDbSessionFactory()
			factory.SetMediator(storage.mediator)
			return factory
		}(),
		userService: &stubUserService{},
	}

	msgCtx, cookies := requestCarrying("stuck")
	session, err := strategy.NewSessionWithoutUser(contextWith(msgCtx))

	require.ErrorIs(t, err, ErrRevocationPending)
	assert.Nil(t, session)
	assert.Empty(t, cookiesNamed(cookies, SessionCookieName()),
		"issuing a replacement cookie here destroys the only handle on the live session")
}

// TestAVisitorWithNoPendingRevocationStillGetsASessionWhileAnotherIdIsStuck is the
// not-an-outage fence. The refusal is keyed on the presented identifier, so an unreachable
// database for one logout must not stop everybody else getting a session.
func TestAVisitorWithNoPendingRevocationStillGetsASessionWhileAnotherIdIsStuck(t *testing.T) {
	repo := newFakeSessionRepo()
	storage := newDbStorage(repo)
	repo.put(liveRow("stuck", "user-1"))

	repo.deleteErr = assert.AnError
	require.Error(t, storage.DeleteSessionById("stuck"))
	repo.deleteErr = nil

	factory := NewDbSessionFactory()
	factory.SetMediator(storage.mediator)
	strategy := &StandardAuthStrategy{
		sessionManager: storage,
		csrfService:    &CsrfService{},
		sessionFactory: factory,
		userService:    &stubUserService{},
	}

	// A different visitor, presenting nothing.
	session, err := strategy.NewSessionWithoutUser(
		contextWith(&stubMessageContext{cookies: newStubCookieManager()}))

	require.NoError(t, err, "one stuck revocation must not stop every other visitor")
	assert.NotNil(t, session)
}

// TestAPendingRevocationIsRetriedByTheScheduledSweep covers the identifier nobody presents
// again — a client that threw its cookie away, or never came back.
func TestAPendingRevocationIsRetriedByTheScheduledSweep(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("abandoned", "user-1"))
	mediator := NewDbSessionMediator(repo)

	repo.deleteErr = assert.AnError
	_, err := mediator.DeleteSession("abandoned")
	require.Error(t, err)
	require.True(t, repo.has("abandoned"))

	// The sweep runs while the store is still unreachable: it must report the work as
	// outstanding rather than exiting clean.
	assert.Error(t, mediator.ClearExpired(),
		"a sweep that left a revocation owed must not report success")

	repo.deleteErr = nil
	withoutRetryBackoff(t)
	require.NoError(t, mediator.ClearExpired())

	assert.False(t, repo.has("abandoned"), "the sweep has to finish the revocation")
	assert.False(t, mediator.RevocationPending("abandoned"))
}

// TestAnOverlongSessionIdNeverBecomesAMapKey. The identifier is whatever is in the cookie, and
// both stores use it as a map key; the tombstone comment has always acknowledged the hazard
// and closed it only for the clean-zero-rows case.
func TestAnOverlongSessionIdNeverBecomesAMapKey(t *testing.T) {
	oversized := strings.Repeat("x", SessionIdMaxLength+1)

	repo := newFakeSessionRepo()
	mediator := NewDbSessionMediator(repo)
	repo.deleteErr = assert.AnError

	deleted, err := mediator.DeleteSession(oversized)
	assert.False(t, deleted)
	assert.NoError(t, err, "an identifier this long is not a session, so there is nothing to fail")

	mediator.mu.RLock()
	pending := len(mediator.pending)
	tombstones := len(mediator.tombstones)
	mediator.mu.RUnlock()

	assert.Zero(t, pending, "an over-long identifier must not be retained as owed work")
	assert.Zero(t, tombstones, "nor as a tombstone")
}

// TestPendingRevocationsAreBounded. Time alone is not a bound when the key is attacker-chosen.
func TestPendingRevocationsAreBounded(t *testing.T) {
	previous := MaxPendingRevocations
	MaxPendingRevocations = 8
	defer func() { MaxPendingRevocations = previous }()

	repo := newFakeSessionRepo()
	mediator := NewDbSessionMediator(repo)
	repo.deleteErr = assert.AnError

	for i := 0; i < 100; i++ {
		row := liveRow(fmt.Sprintf("sid-%d", i), "user-1")
		repo.put(row)
		_, _ = mediator.GetSession(row.ID)
		_, _ = mediator.DeleteSession(row.ID)
	}

	mediator.mu.RLock()
	held := len(mediator.pending)
	mediator.mu.RUnlock()

	assert.LessOrEqual(t, held, MaxPendingRevocations,
		"the pending set has to be bounded by cardinality, not only by expiry")
}

// TestATombstoneOutlivesTheSessionItProtectsAndNoLonger. A fixed 25 hours was both too long
// for a short session and too short for an app that configured a longer lifetime — the only
// case where the memory actually has to hold.
func TestATombstoneOutlivesTheSessionItProtectsAndNoLonger(t *testing.T) {
	repo := newFakeSessionRepo()
	mediator := NewDbSessionMediator(repo)

	longLived := liveRow("long", "user-1")
	longLived.Expiry = time.Now().Add(72 * time.Hour)
	repo.put(longLived)

	_, err := mediator.GetSession("long")
	require.NoError(t, err)
	_, err = mediator.DeleteSession("long")
	require.NoError(t, err)

	mediator.mu.RLock()
	until := mediator.tombstones["long"]
	mediator.mu.RUnlock()

	assert.True(t, until.After(time.Now().Add(72*time.Hour)),
		"the memory of a revocation has to outlive the session it protects against; a fixed "+
			"%s would have expired %s before the session did",
		SessionTombstoneRetention, 72*time.Hour-SessionTombstoneRetention)
}
