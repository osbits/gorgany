package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two DbSessionMediators over one fakeSessionRepo *are* two replicas. The double models the
// table rather than the repository, so what the two share is exactly what two processes share
// in production — the row — and what they do not share is exactly what they do not share
// there: the per-process cache of entity objects.
//
// The defect these fence: a cache hit used to keep the local user id, attributes and activity
// stamp and take the row's expiry only when it was *later*, while every write re-sent the
// whole struct. So a replica that had not looked recently could put back an attribute, an
// identity or a long expiry that another replica had just removed, and it did so through the
// most ordinary path there is — SessionMiddleware touches expiry, the flash marker and last
// activity on literally every request.

// replicaPair is two independent mediators over one table.
type replicaPair struct {
	repo *fakeSessionRepo
	a    *DbSessionMediator
	b    *DbSessionMediator
}

func newReplicaPair(t *testing.T, id, userId string) *replicaPair {
	t.Helper()

	repo := newFakeSessionRepo()
	repo.put(liveRow(id, userId))

	return &replicaPair{
		repo: repo,
		a:    NewDbSessionMediator(repo),
		b:    NewDbSessionMediator(repo),
	}
}

// sessionOn resolves the session through one replica, which also caches it there.
func sessionOn(t *testing.T, mediator *DbSessionMediator, id string) *DbSessionEntityWithMediator {
	t.Helper()

	entity, err := mediator.GetSession(id)
	require.NoError(t, err)
	require.NotNil(t, entity)
	return NewDbSessionEntityWithMediator(entity, mediator)
}

func storedRow(t *testing.T, repo *fakeSessionRepo, id string) *DbSessionEntity {
	t.Helper()

	row, err := repo.FindById(id)
	require.NoError(t, err)
	require.NotNil(t, row)
	return row
}

// TestASecondReplicaHeartbeatDoesNotRestoreAnAttributeThisOneCleared is the headline for
// SEC-H01. Replica B has the session cached with the attribute set; A clears it; B then does
// nothing more remarkable than serve another request.
func TestASecondReplicaHeartbeatDoesNotRestoreAnAttributeThisOneCleared(t *testing.T) {
	pair := newReplicaPair(t, "sid", "user-1")

	// Both replicas have served this session, so both hold it in their caches.
	onA := sessionOn(t, pair.a, "sid")
	onA.SetItem("mfa_verified", "yes")
	onB := sessionOn(t, pair.b, "sid")
	require.Equal(t, "yes", onB.GetItem("mfa_verified"))

	// A revokes the step-up.
	onA.ClearItem("mfa_verified")
	require.Empty(t, storedRow(t, pair.repo, "sid").Attributes["mfa_verified"],
		"the row no longer carries the flag")

	// B serves an ordinary request: SessionMiddleware's heartbeat, nothing else.
	onB.SetExpiry(time.Now().Add(time.Hour))
	onB.SetLastActivity(time.Now())

	assert.Empty(t, storedRow(t, pair.repo, "sid").Attributes["mfa_verified"],
		"a heartbeat on a stale replica put back an authorization attribute another replica cleared")

	refreshed, err := pair.b.GetSession("sid")
	require.NoError(t, err)
	assert.Empty(t, refreshed.GetItem("mfa_verified"),
		"and the stale replica must not keep serving the cleared flag from its cache")
}

// TestASecondReplicaHeartbeatDoesNotRestoreTheUserIdALogoutRemoved — the same defect on the
// field authorization is actually derived from.
func TestASecondReplicaHeartbeatDoesNotRestoreTheUserIdALogoutRemoved(t *testing.T) {
	pair := newReplicaPair(t, "sid", "user-1")

	onA := sessionOn(t, pair.a, "sid")
	onB := sessionOn(t, pair.b, "sid")
	require.Equal(t, "user-1", onB.GetUserId())

	// A de-authenticates the session in place.
	onA.SetUserId("")
	require.Empty(t, storedRow(t, pair.repo, "sid").UserID)

	onB.SetLastActivity(time.Now())

	assert.Empty(t, storedRow(t, pair.repo, "sid").UserID,
		"a heartbeat on a stale replica restored the identity another replica removed")
}

// TestAHeartbeatDoesNotWriteColumnsItDidNotChange is the mechanism underneath both of the
// above, asserted directly: the statement must name only the columns that moved.
func TestAHeartbeatDoesNotWriteColumnsItDidNotChange(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	mediator := NewDbSessionMediator(repo)

	var written map[string]bool
	repo.beforeSave = func(string) {}
	entity, err := mediator.GetSession("sid")
	require.NoError(t, err)

	session := NewDbSessionEntityWithMediator(entity, mediator)
	repo.recordDirty(&written)

	session.SetLastActivity(time.Now())

	require.NotNil(t, written, "the save recorded no column set at all")
	assert.True(t, written[SessionColumnLastActivity], "the column that changed must be written")
	assert.False(t, written[SessionColumnUserID],
		"the user id was not touched and must not be re-sent")
	assert.False(t, written[SessionColumnAttributes],
		"the attribute bag was not touched and must not be re-sent")
	assert.False(t, written[SessionColumnVersion],
		"a heartbeat must not bump the version, or two parallel requests from one browser "+
			"would conflict on every page load")
}

// TestAStateWriteIsGuardedOnTheVersion — the other half of the write-shape rule.
func TestAStateWriteIsGuardedOnTheVersion(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	mediator := NewDbSessionMediator(repo)

	entity, err := mediator.GetSession("sid")
	require.NoError(t, err)
	session := NewDbSessionEntityWithMediator(entity, mediator)

	var written map[string]bool
	repo.recordDirty(&written)

	session.SetItem("cart", "42")

	assert.True(t, written[SessionColumnAttributes])
	assert.True(t, written[SessionColumnVersion], "a state write carries the next version")
	assert.Equal(t, int64(1), storedRow(t, repo, "sid").Version)
}

// TestACachedSessionAdoptsTheRowsExpiryEvenWhenTheRowIsShorter pins the deletion of the
// max-expiry merge. Taking the later of the two could only ever extend a session, so an
// expiry another replica shortened was unreachable by construction.
func TestACachedSessionAdoptsTheRowsExpiryEvenWhenTheRowIsShorter(t *testing.T) {
	pair := newReplicaPair(t, "sid", "user-1")

	onA := sessionOn(t, pair.a, "sid")
	onB := sessionOn(t, pair.b, "sid")
	long := time.Now().Add(10 * time.Hour).Truncate(time.Second)
	onB.SetExpiry(long)
	require.Equal(t, long, storedRow(t, pair.repo, "sid").Expiry.Truncate(time.Second))

	// A shortens it — a step-down, a suspicious-activity response, an admin action.
	short := time.Now().Add(time.Minute).Truncate(time.Second)
	onA.SetExpiry(short)

	refreshed, err := pair.b.GetSession("sid")
	require.NoError(t, err)
	assert.Equal(t, short, refreshed.GetExpiry().Truncate(time.Second),
		"the row is authoritative for expiry in both directions, not only upward")
}

// TestACachedSessionAdoptsEveryColumnFromTheRow states the rule plainly.
func TestACachedSessionAdoptsEveryColumnFromTheRow(t *testing.T) {
	pair := newReplicaPair(t, "sid", "user-1")

	sessionOn(t, pair.b, "sid") // B caches the session as it is now.
	onA := sessionOn(t, pair.a, "sid")
	onA.SetUserId("user-2")
	onA.SetItem("theme", "dark")

	refreshed, err := pair.b.GetSession("sid")
	require.NoError(t, err)
	assert.Equal(t, "user-2", refreshed.GetUserId())
	assert.Equal(t, "dark", refreshed.GetItem("theme"))
}

// TestAConcurrentAttributeWriteIsDetectedNotSilentlyLost. Both replicas write the attribute
// column from copies of the same version; the second must not simply overwrite the first.
func TestAConcurrentAttributeWriteIsDetectedNotSilentlyLost(t *testing.T) {
	pair := newReplicaPair(t, "sid", "user-1")

	onA := sessionOn(t, pair.a, "sid")
	onB := sessionOn(t, pair.b, "sid")

	onA.SetItem("from_a", "1")
	// B is still holding the version it read before A's write.
	onB.SetItem("from_b", "1")

	row := storedRow(t, pair.repo, "sid")
	assert.Equal(t, "1", row.Attributes["from_a"],
		"B's write must not have erased the key A had already stored")
	assert.Equal(t, "1", row.Attributes["from_b"])
}

// TestAConflictReplaysThePendingAttributeOpsRatherThanTheWholeMap is the reason mutations are
// recorded as operations. Replaying B's *map* would put back the key A deleted; replaying B's
// *operations* keeps both decisions.
func TestAConflictReplaysThePendingAttributeOpsRatherThanTheWholeMap(t *testing.T) {
	pair := newReplicaPair(t, "sid", "user-1")

	onA := sessionOn(t, pair.a, "sid")
	onA.SetItem("mfa_verified", "yes")

	// B loads the session while the flag is set, so its local map contains it.
	onB := sessionOn(t, pair.b, "sid")
	require.Equal(t, "yes", onB.GetItem("mfa_verified"))

	// A clears the flag. B, still on the old version, writes an unrelated key.
	onA.ClearItem("mfa_verified")
	onB.SetItem("cart", "42")

	row := storedRow(t, pair.repo, "sid")
	assert.Empty(t, row.Attributes["mfa_verified"],
		"replaying B's whole map would resurrect the flag A cleared")
	assert.Equal(t, "42", row.Attributes["cart"], "and B's own change must survive")
}

// TestAFullWriteStillCarriesEveryColumn — AddSession means "persist the state I am holding",
// and the repair path H02 depends on needs it to stay that way.
func TestAFullWriteStillCarriesEveryColumn(t *testing.T) {
	repo := newFakeSessionRepo()
	storage := newDbStorage(repo)
	repo.put(liveRow("sid", "user-1"))

	session := sessionById(t, storage, "sid")
	require.NotNil(t, session)

	var written map[string]bool
	repo.recordDirty(&written)

	require.NoError(t, storage.AddSession(session))

	for _, column := range []string{
		SessionColumnUserID, SessionColumnExpiry, SessionColumnCreatedAt,
		SessionColumnLastActivity, SessionColumnAttributes,
	} {
		assert.True(t, written[column], "AddSession must write %s", column)
	}
	assert.True(t, written[SessionColumnVersion],
		"a full write includes the user id, so it is guarded like any other state write")
}
