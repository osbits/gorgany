package controller

import (
	"sync"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The token endpoint is registered by default at core.DefaultCSRFTokenPath and is
// reachable without authentication, so whatever it does to session storage is
// something any visitor can ask for. It ends by upserting the session it resolved,
// which turns "hand me a token" into "put this session back".

// fakeSessionTable stands in for the `sessions` table. Save on a row that is gone
// matches nothing and reports success, and deleting an absent row is a no-op — SQL,
// not the repository's own behaviour.
type fakeSessionTable struct {
	mu   sync.Mutex
	rows map[string]*auth.DbSessionEntity
}

func newFakeSessionTable() *fakeSessionTable {
	return &fakeSessionTable{rows: map[string]*auth.DbSessionEntity{}}
}

func (r *fakeSessionTable) clone(s *auth.DbSessionEntity) *auth.DbSessionEntity {
	row := &auth.DbSessionEntity{
		ID:           s.ID,
		UserID:       s.UserID,
		Expiry:       s.Expiry,
		CreatedAt:    s.CreatedAt,
		LastActivity: s.LastActivity,
	}
	row.Meta = *s.GetMeta()
	row.Meta.IsLoaded = true
	if s.Attributes != nil {
		attrs := make(auth.AttributesMap, len(s.Attributes))
		for k, v := range s.Attributes {
			attrs[k] = v
		}
		row.Attributes = attrs
	}
	return row
}

func (r *fakeSessionTable) insert(s *auth.DbSessionEntity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[s.ID] = r.clone(s)
}

func (r *fakeSessionTable) FindById(id string) (*auth.DbSessionEntity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[id]
	if !ok {
		return nil, nil
	}
	return r.clone(row), nil
}

func (r *fakeSessionTable) Save(session *auth.DbSessionEntity) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rows[session.GetId()]; !exists && session.GetMeta().IsLoaded {
		return nil
	}
	r.rows[session.GetId()] = r.clone(session)
	session.GetMeta().IsLoaded = true
	return nil
}

func (r *fakeSessionTable) Delete(session *auth.DbSessionEntity) error {
	_, err := r.DeleteById(session.GetId())
	return err
}

func (r *fakeSessionTable) DeleteById(id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, existed := r.rows[id]
	delete(r.rows, id)
	return existed, nil
}

func (r *fakeSessionTable) DeleteExpired() error { return nil }

// TestTokenDoesNotResurrectARevokedSession, for both backends.
//
// The revoked session object is still in the caller's hand — the request that carries
// the stolen cookie resolved it before the victim logged out, or a concurrent request
// still holds it. Asking for a CSRF token must not put it back.
func TestTokenDoesNotResurrectARevokedSession(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		storage := auth.NewMemorySession(time.Hour)
		session := auth.NewSession("session-1", time.Now().Add(time.Hour))
		session.SetUserId("user-9")
		require.NoError(t, storage.AddSession(session))

		require.NoError(t, storage.DeleteSessionById("session-1"))
		require.Nil(t, sessionById(t, storage, "session-1"), "the session is revoked")

		controller := csrfControllerOver(storage, session)
		controller.Token(newStubMessage())

		assert.Nil(t, sessionById(t, storage, "session-1"),
			"a revoked session must not come back through the token endpoint")
	})

	t.Run("database", func(t *testing.T) {
		table := newFakeSessionTable()
		now := time.Now()
		row := &auth.DbSessionEntity{
			ID:           "session-1",
			UserID:       "user-9",
			Expiry:       now.Add(time.Hour),
			CreatedAt:    now,
			LastActivity: now,
			Attributes:   auth.AttributesMap{},
		}
		table.insert(row)

		storage := auth.NewDbSessionStorageWithRepository(time.Hour, table)
		session := sessionById(t, storage, "session-1")
		require.NotNil(t, session, "the request resolved the session before the logout")

		require.NoError(t, storage.DeleteSessionById("session-1"))
		require.Nil(t, sessionById(t, storage, "session-1"), "the session is revoked")

		controller := csrfControllerOver(storage, session)
		controller.Token(newStubMessage())

		assert.Nil(t, sessionById(t, storage, "session-1"),
			"a revoked session must not come back through the token endpoint")
	})
}

// sessionById is the two-value lookup with the error asserted away.
func sessionById(t *testing.T, storage core.ISessionStorage, id string) core.ISession {
	t.Helper()

	session, err := storage.GetSessionById(id)
	require.NoError(t, err)
	return session
}

func csrfControllerOver(storage core.ISessionStorage, session core.ISession) *CsrfController {
	c := NewCsrfController()
	c.AuthContext = &stubAuthContext{strategy: &stubStrategy{session: session}}
	c.CsrfService = &auth.CsrfService{}
	c.SessionStorage = storage
	return c
}
