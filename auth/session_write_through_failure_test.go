package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// core.ISession's setters are void, and the contract on that interface says what an
// implementation owes in exchange: "an implementation whose write-through can fail must
// remember the failure and surface it at the next operation that *can* report one". The
// database-backed session logged the error and forgot it, so the obligation was documented
// and not implemented.
//
// Two paths made that reachable with no error-bearing call afterwards. /csrf hands a token to
// an unauthenticated caller and is registered by default; SessionMiddleware's heartbeat runs
// on every request. Login and RotateSession happened to be safe by accident — they finish with
// AddSession, which re-sends everything — and that accident is fenced below too, because it is
// load-bearing and easy to remove by mistake.

// failingWriteSession is a database-backed session whose store is unreachable.
func failingWriteSession(t *testing.T, id string) (*DbSessionEntityWithMediator, *fakeSessionRepo) {
	t.Helper()

	repo := newFakeSessionRepo()
	repo.put(liveRow(id, "user-1"))
	mediator := NewDbSessionMediator(repo)

	entity, err := mediator.GetSession(id)
	require.NoError(t, err)
	require.NotNil(t, entity)

	return NewDbSessionEntityWithMediator(entity, mediator), repo
}

// TestACsrfTokenIsNotHandedOutWhenTheSessionCouldNotStoreIt is the headline. A token the
// store never received is worse than no token: the client believes it is protected and every
// mutating request it makes is refused.
func TestACsrfTokenIsNotHandedOutWhenTheSessionCouldNotStoreIt(t *testing.T) {
	session, repo := failingWriteSession(t, "sid")
	repo.saveErr = errors.New("connection refused")

	token, err := (&CsrfService{}).GenerateCSRFToken(t.Context(), session)

	require.Error(t, err, "a token whose write did not land must not be issued")
	assert.Empty(t, token, "and no usable token may be returned alongside the error")
}

// TestAStoredCsrfTokenIsStillHandedOut — the over-blocking fence.
func TestAStoredCsrfTokenIsStillHandedOut(t *testing.T) {
	session, _ := failingWriteSession(t, "sid")

	token, err := (&CsrfService{}).GenerateCSRFToken(t.Context(), session)

	require.NoError(t, err)
	assert.NotEmpty(t, token)
	assert.Equal(t, token, session.GetItem(core.CSRFSessionKey))
}

// TestAnAlreadyStoredTokenIsReturnedDespiteAnUnrelatedFailedWrite. GetCSRFToken's reuse branch
// writes nothing, and the token it hands back is one the session genuinely holds. Refusing it
// because some earlier write failed would take a working token away over an unrelated problem.
func TestAnAlreadyStoredTokenIsReturnedDespiteAnUnrelatedFailedWrite(t *testing.T) {
	session, repo := failingWriteSession(t, "sid")
	service := &CsrfService{}

	token, err := service.GetCSRFToken(t.Context(), session)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	// Something else fails afterwards.
	repo.saveErr = errors.New("connection refused")
	session.SetLastActivity(time.Now())
	require.Error(t, core.PendingWriteError(session), "the heartbeat failure is remembered")

	reused, err := service.GetCSRFToken(t.Context(), session)
	require.NoError(t, err, "the reuse branch performs no write and must not refuse")
	assert.Equal(t, token, reused)
}

// TestASuccessfulWriteCarriesTheColumnsAnEarlierFailureLeftBehind is what makes "clear the
// failure on any success" a safe rule rather than a sloppy one.
//
// A failed write clears nothing, so its columns stay dirty and the next write carries them
// again. So the later heartbeat is not merely *followed by* the attribute reaching the row —
// it is what puts it there. Were that not so, clearing on any success would let a heartbeat
// swallow a failure the caller never saw, and the bookkeeping would have to be per-column.
func TestASuccessfulWriteCarriesTheColumnsAnEarlierFailureLeftBehind(t *testing.T) {
	session, repo := failingWriteSession(t, "sid")

	repo.saveErr = errors.New("connection refused")
	session.SetItem("mfa_verified", "yes")
	require.Error(t, core.PendingWriteError(session))
	require.Empty(t, storedRow(t, repo, "sid").Attributes["mfa_verified"],
		"the write did not land, which is the premise")

	// The store comes back and an ordinary heartbeat runs.
	repo.saveErr = nil
	var written map[string]bool
	repo.recordDirty(&written)
	session.SetExpiry(time.Now().Add(time.Hour))

	assert.True(t, written[SessionColumnAttributes],
		"the column an earlier write failed on is still dirty, so this write carries it")
	assert.Equal(t, "yes", storedRow(t, repo, "sid").Attributes["mfa_verified"],
		"and the attribute therefore reaches the row")
	assert.NoError(t, core.PendingWriteError(session),
		"only now is the failure settled, because the state genuinely arrived")
}

// TestAWriteOfTheSameColumnsClearsTheFailure — the other half: the failure is not sticky
// forever, it is cleared by a write that actually covers it.
func TestAWriteOfTheSameColumnsClearsTheFailure(t *testing.T) {
	session, repo := failingWriteSession(t, "sid")

	repo.saveErr = errors.New("connection refused")
	session.SetItem("mfa_verified", "yes")
	require.Error(t, core.PendingWriteError(session))

	repo.saveErr = nil
	session.SetItem("mfa_verified", "yes")

	assert.NoError(t, core.PendingWriteError(session),
		"a successful write of the same column means the state did reach the store")
}

// TestAskingTwiceGivesTheSameAnswer pins report-not-consume. If the first caller cleared it,
// which caller sees a failure would depend on call order.
func TestAskingTwiceGivesTheSameAnswer(t *testing.T) {
	session, repo := failingWriteSession(t, "sid")
	repo.saveErr = errors.New("connection refused")
	session.SetItem("cart", "42")

	first := core.PendingWriteError(session)
	second := core.PendingWriteError(session)

	require.Error(t, first)
	assert.Equal(t, first, second)
}

// TestAFailedWriteIsReportedByTheNextOperationThatCanReportOne — the contract, end to end,
// through the storage call rather than through the entity.
func TestAFailedWriteIsReportedByTheNextOperationThatCanReportOne(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	storage := newDbStorage(repo)

	session := sessionById(t, storage, "sid")
	require.NotNil(t, session)

	repo.saveErr = errors.New("connection refused")
	session.SetUserId("user-2")

	assert.Error(t, storage.AddSession(session),
		"AddSession re-sends every column, so its own failure is the report")

	repo.saveErr = nil
	require.NoError(t, storage.AddSession(session))
	assert.NoError(t, core.PendingWriteError(session),
		"and a full write that succeeds settles it")
}

// TestAMemorySessionNeverReportsAPendingWrite. It cannot fail a write, so it does not
// implement the interface and core.PendingWriteError answers nil — which is what makes the
// helper safe to call unconditionally.
func TestAMemorySessionNeverReportsAPendingWrite(t *testing.T) {
	session := NewSession("sid", time.Now().Add(time.Hour))
	session.SetItem("cart", "42")

	_, isReporter := core.ISession(session).(core.ISessionWriteStatus)
	assert.False(t, isReporter, "the in-memory session has no write that can fail")
	assert.NoError(t, core.PendingWriteError(session))
}

// TestTheFailureIsLoggedWithTheSessionIdAndNoSecret. The acceptance criterion asks that the
// diagnostic name the session without naming what was in it.
func TestTheFailureIsLoggedWithTheSessionIdAndNoSecret(t *testing.T) {
	session, repo := failingWriteSession(t, "sid")
	repo.saveErr = errors.New("connection refused")

	session.SetItem(core.CSRFSessionKey, "super-secret-token-value")

	err := core.PendingWriteError(session)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sid", "the diagnostic has to identify the session")
	assert.NotContains(t, err.Error(), "super-secret-token-value",
		"and must not carry the attribute value into a log")
}
