package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RevokeSession is part of core.ISessionStorage rather than an optional interface asserted
// for at the call site. These tests fence the property that made it mandatory: rotation must
// never carry an identity off a session the store could not confirm was live.
//
// Before, a storage without the optional interface got StandardAuthStrategy.revoke's fallback
// — DeleteSessionById, then (true, nil). Deleting an absent session is deliberately not an
// error, so that fallback reported "there was a live session here" for a delete that removed
// nothing, which is precisely the answer RotateSession's guard reads as permission to
// proceed. The compile error a third-party storage now gets is the point: there is no correct
// value for the framework to guess on its behalf.

// answeringStorage is a compliant core.ISessionStorage whose revoker answer the test chooses.
// Everything else delegates to a real memory store, so rotation exercises real code.
type answeringStorage struct {
	core.ISessionStorage

	wasLive   bool
	revokeErr error
	revoked   []string
}

func newAnsweringStorage(wasLive bool, revokeErr error) *answeringStorage {
	return &answeringStorage{
		ISessionStorage: NewMemorySession(time.Hour),
		wasLive:         wasLive,
		revokeErr:       revokeErr,
	}
}

func (s *answeringStorage) RevokeSession(id string) (bool, error) {
	s.revoked = append(s.revoked, id)
	// The row is removed either way. What varies is what the storage is willing to say
	// about whether it was there — which is the only thing rotation depends on.
	_ = s.ISessionStorage.DeleteSessionById(id)
	return s.wasLive, s.revokeErr
}

func strategyOver(storage core.ISessionStorage) *StandardAuthStrategy {
	return &StandardAuthStrategy{
		sessionManager: storage,
		csrfService:    &CsrfService{},
		sessionFactory: NewMemorySessionFactory(),
		userService:    &stubUserService{},
	}
}

// TestRotationDoesNotAssumeADeleteRemovedSomething is the headline. A storage that says "there
// was nothing to revoke" must stop the rotation, not be second-guessed into a success.
func TestRotationDoesNotAssumeADeleteRemovedSomething(t *testing.T) {
	storage := newAnsweringStorage(false, nil)
	strategy := strategyOver(storage)

	ctx := contextWith(&stubMessageContext{cookies: newStubCookieManager()})
	old := NewSession("planted", time.Now().Add(time.Hour))
	old.SetUserId("victim-42")
	require.NoError(t, storage.AddSession(old))

	rotated, err := strategy.RotateSession(ctx, old)

	require.Error(t, err, "a session the store could not confirm was live must not be rotated")
	assert.Nil(t, rotated, "no replacement session may be handed back")
	assert.Contains(t, storage.revoked, "planted", "the revocation was still attempted")
}

// TestLoginRefusesWhenTheStoreCannotConfirmTheSessionWasLive — the same refusal reached
// through the operation that matters, since Login rotates whatever the client presented.
func TestLoginRefusesWhenTheStoreCannotConfirmTheSessionWasLive(t *testing.T) {
	storage := newAnsweringStorage(false, nil)
	strategy := strategyOver(storage)

	msgCtx, cookies := requestCarrying("planted")
	old := NewSession("planted", time.Now().Add(time.Hour))
	require.NoError(t, storage.AddSession(old))

	session, err := strategy.Login(&stubUser{id: "victim-42"}, contextWith(msgCtx))

	require.Error(t, err)
	assert.Nil(t, session)
	assert.Empty(t, cookiesNamed(cookies, SessionCookieName()),
		"a login that failed closed must not leave the client a cookie")
}

// TestRotationProceedsWhenTheStoreConfirmsTheSessionWasLive is the over-blocking fence: the
// refusal must be about the store's answer, not about rotation itself.
func TestRotationProceedsWhenTheStoreConfirmsTheSessionWasLive(t *testing.T) {
	storage := newAnsweringStorage(true, nil)
	strategy := strategyOver(storage)

	ctx := contextWith(&stubMessageContext{cookies: newStubCookieManager()})
	old := NewSession("live", time.Now().Add(time.Hour))
	old.SetUserId("user-7")
	require.NoError(t, storage.AddSession(old))

	rotated, err := strategy.RotateSession(ctx, old)

	require.NoError(t, err)
	require.NotNil(t, rotated)
	assert.NotEqual(t, "live", rotated.GetId())
	assert.Equal(t, "user-7", rotated.GetUserId())
}

// TestRotationReportsAStorageThatCannotAnswer. A storage that genuinely cannot tell must
// return an error, and that error has to reach the caller rather than being flattened into
// "not live" — the two need different operator responses.
func TestRotationReportsAStorageThatCannotAnswer(t *testing.T) {
	cannotTell := errors.New("storage cannot report liveness")
	storage := newAnsweringStorage(false, cannotTell)
	strategy := strategyOver(storage)

	ctx := contextWith(&stubMessageContext{cookies: newStubCookieManager()})
	old := NewSession("unknown", time.Now().Add(time.Hour))
	require.NoError(t, storage.AddSession(old))

	_, err := strategy.RotateSession(ctx, old)

	require.Error(t, err)
	assert.ErrorIs(t, err, cannotTell, "the storage's own reason must survive to the caller")
}

// TestBothShippedStoragesReportRevocationLiveness. The compile-time assertions in db_session.go
// and memory_session.go already say this, but they said it while the interface was optional
// too — and the interface being satisfied is not the same as the answer being right.
func TestBothShippedStoragesReportRevocationLiveness(t *testing.T) {
	for _, backend := range sessionBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			session := NewSession("sid", time.Now().Add(time.Hour))
			if backend.name == "database" {
				session = nil
			}

			var id string
			if session != nil {
				require.NoError(t, backend.storage.AddSession(session))
				id = session.GetId()
			} else {
				created, err := strategyFor(backend).NewSessionWithoutUser(
					contextWith(&stubMessageContext{cookies: newStubCookieManager()}))
				require.NoError(t, err)
				id = created.GetId()
			}

			wasLive, err := backend.storage.RevokeSession(id)
			require.NoError(t, err)
			assert.True(t, wasLive, "the store held this session")

			wasLive, err = backend.storage.RevokeSession(id)
			require.NoError(t, err, "revoking twice is idempotent, not an error")
			assert.False(t, wasLive, "the second revocation removed nothing and must say so")
		})
	}
}
