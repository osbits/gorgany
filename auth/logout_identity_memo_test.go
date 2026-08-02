package auth

import (
	"errors"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memoisingMessageContext is a stubMessageContext that also carries a request identity memo,
// which is what the real *messageContext does.
//
// Revoking a session does not disturb the copy of it hanging off the message context, and the
// memo key is built from the session id and user id — so the key is unchanged across a logout
// and the memo cannot notice the revocation on its own. Without an explicit invalidation it
// keeps answering with the identity of the user who has just logged out, and nothing consults
// the store again for the rest of the request.
type memoisingMessageContext struct {
	stubMessageContext

	key         string
	identity    any
	stored      bool
	invalidated int
}

var _ core.IRequestIdentityMemo = (*memoisingMessageContext)(nil)

func (c *memoisingMessageContext) LoadIdentity(key string) (any, bool) {
	if !c.stored || c.key != key {
		return nil, false
	}
	return c.identity, true
}

func (c *memoisingMessageContext) StoreIdentity(key string, identity any) {
	c.key = key
	c.identity = identity
	c.stored = true
}

func (c *memoisingMessageContext) InvalidateIdentity() {
	c.stored = false
	c.identity = nil
	c.key = ""
	c.invalidated++
}

const memoTestKey = "session:sid\x00user-1"

func memoLogoutFixture(t *testing.T, deleteErr error) (*StandardAuthStrategy, *memoisingMessageContext) {
	t.Helper()

	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	repo.deleteErr = deleteErr

	strategy := &StandardAuthStrategy{sessionManager: newDbStorage(repo), csrfService: &CsrfService{}}

	cookies := newStubCookieManager()
	cookies.present[SessionCookieName()] = "sid"

	messageContext := &memoisingMessageContext{stubMessageContext: stubMessageContext{cookies: cookies}}
	messageContext.StoreIdentity(memoTestKey, "resolved-as-user-1")

	return strategy, messageContext
}

// A handler that renders anything access-controlled after logging the caller out would
// otherwise filter it for the user who just left.
func TestLogoutDropsTheIdentityMemoisedEarlierInTheRequest(t *testing.T) {
	strategy, messageContext := memoLogoutFixture(t, nil)

	_, found := messageContext.LoadIdentity(memoTestKey)
	require.True(t, found, "the memo has to be populated for this test to mean anything")

	require.NoError(t, strategy.Logout(contextWith(messageContext)))

	assert.Equal(t, 1, messageContext.invalidated, "logout must drop the memoised identity")

	_, found = messageContext.LoadIdentity(memoTestKey)
	assert.False(t, found,
		"the identity resolved before the logout must not still be readable afterwards")
}

// A logout that could not revoke the session leaves the caller authenticated on purpose — the
// cookie is deliberately left in place — so the memo is still correct and must survive.
// Dropping it would cost only a re-resolution, but it would report a state change that did not
// happen.
func TestAFailedLogoutLeavesTheMemoAlone(t *testing.T) {
	strategy, messageContext := memoLogoutFixture(t, errors.New("sessions table is unavailable"))

	require.Error(t, strategy.Logout(contextWith(messageContext)),
		"a revocation that fails has to be reported")

	assert.Zero(t, messageContext.invalidated,
		"a logout that revoked nothing must not claim the identity changed")

	_, found := messageContext.LoadIdentity(memoTestKey)
	assert.True(t, found, "the caller is still authenticated, so the memo is still correct")
}
