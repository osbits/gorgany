package auth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSessionRow stands in for the `sessions` table rather than for
// DbSessionRepository: FindById hands back a fresh struct every time, a Save whose
// UPDATE matches no row reports success (that is what SQL does), and deleting an
// absent row is a no-op. Modelling the database and not the repository is deliberate
// — a double that reproduced the repository's own bugs would make the fix look tested
// when only the double had changed.
type fakeSessionRepo struct {
	mu   sync.Mutex
	rows map[string]*DbSessionEntity

	saveErr   error
	deleteErr error

	// beforeSave and beforeDelete run before the statement touches the table, so a test can
	// hold a request inside its database round trip and drive an interleaving without
	// sleeping.
	beforeSave   func(id string)
	beforeDelete func(id string)
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{rows: map[string]*DbSessionEntity{}}
}

func cloneRow(s *DbSessionEntity) *DbSessionEntity {
	row := &DbSessionEntity{
		ID:           s.ID,
		UserID:       s.UserID,
		Expiry:       s.Expiry,
		CreatedAt:    s.CreatedAt,
		LastActivity: s.LastActivity,
	}
	row.Meta = *s.GetMeta()
	row.Meta.IsLoaded = true
	if s.Attributes != nil {
		attrs := make(AttributesMap, len(s.Attributes))
		for k, v := range s.Attributes {
			attrs[k] = v
		}
		row.Attributes = attrs
	}
	return row
}

func (r *fakeSessionRepo) put(s *DbSessionEntity) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[s.ID] = cloneRow(s)
}

func (r *fakeSessionRepo) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.rows[id]
	return ok
}

func (r *fakeSessionRepo) FindById(id string) (*DbSessionEntity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[id]
	if !ok {
		return nil, nil
	}
	return cloneRow(row), nil
}

func (r *fakeSessionRepo) Save(session *DbSessionEntity) error {
	if r.beforeSave != nil {
		r.beforeSave(session.GetId())
	}
	if r.saveErr != nil {
		return r.saveErr
	}

	// What the ORM does with whatever entity it is handed: extractFieldsForUpdate
	// copies the whole struct through reflect, and the driver then asks
	// AttributesMap.Value to marshal the map.
	_ = reflect.ValueOf(session).Elem().Interface()
	if _, err := session.Attributes.Value(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rows[session.GetId()]; !exists && session.GetMeta().IsLoaded {
		// An UPDATE whose WHERE matches nothing. No error, no row.
		return nil
	}
	r.rows[session.GetId()] = cloneRow(session)
	session.GetMeta().IsLoaded = true
	return nil
}

func (r *fakeSessionRepo) Delete(session *DbSessionEntity) error {
	_, err := r.DeleteById(session.GetId())
	return err
}

func (r *fakeSessionRepo) DeleteById(id string) (bool, error) {
	if r.beforeDelete != nil {
		r.beforeDelete(id)
	}
	if r.deleteErr != nil {
		return false, r.deleteErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, existed := r.rows[id]
	delete(r.rows, id)
	return existed, nil
}

func (r *fakeSessionRepo) DeleteExpired() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, row := range r.rows {
		if row.Expiry.Before(time.Now()) {
			delete(r.rows, id)
		}
	}
	return nil
}

func newDbStorage(repo ISessionRepository) *DbSessionStorage {
	storage := NewDbSessionStorageWithRepository(time.Hour, repo)
	storage.sessionRotationInterval = 24 * time.Hour
	storage.sessionActivityTimeout = 30 * time.Minute
	return storage
}

// sessionById is the two-value lookup with the error asserted away, for the tests whose
// subject is the session and not the failure mode.
func sessionById(t *testing.T, storage core.ISessionStorage, id string) core.ISession {
	t.Helper()

	session, err := storage.GetSessionById(id)
	require.NoError(t, err)
	return session
}

func liveRow(id, userId string) *DbSessionEntity {
	now := time.Now()
	row := &DbSessionEntity{
		ID:           id,
		UserID:       userId,
		Expiry:       now.Add(time.Hour),
		CreatedAt:    now,
		LastActivity: now,
		Attributes:   AttributesMap{},
	}
	row.Meta.IsLoaded = true
	return row
}

// ------------------------------------------------------------------ HTTP doubles

type stubCookieManager struct {
	mu      sync.Mutex
	set     []*http.Cookie
	present map[string]string
}

func newStubCookieManager() *stubCookieManager {
	return &stubCookieManager{present: map[string]string{}}
}

func (c *stubCookieManager) SetCookie(cookie *http.Cookie) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set = append(c.set, cookie)
}

func (c *stubCookieManager) GetCookie(key string) *http.Cookie {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.present[key]
	if !ok {
		return nil
	}
	return &http.Cookie{Name: key, Value: value}
}

func (c *stubCookieManager) GetCookies() []*http.Cookie { return nil }

// expiredCookies is the set of cookie names this manager was asked to delete.
func (c *stubCookieManager) expiredCookies() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var names []string
	for _, cookie := range c.set {
		if cookie.MaxAge < 0 {
			names = append(names, cookie.Name)
		}
	}
	return names
}

type stubMessageContext struct {
	cookies *stubCookieManager
	session core.ISession
}

func (c *stubMessageContext) GetURL() *url.URL                      { return &url.URL{} }
func (c *stubMessageContext) GetRequestURL() string                 { return "/" }
func (c *stubMessageContext) GetCookieManager() core.ICookieManager { return c.cookies }
func (c *stubMessageContext) GetHeader() http.Header                { return http.Header{} }
func (c *stubMessageContext) GetPathParam(string) string            { return "" }
func (c *stubMessageContext) GetSession() core.ISession             { return c.session }
func (c *stubMessageContext) SetSession(s core.ISession)            { c.session = s }
func (c *stubMessageContext) GetRequest() *http.Request             { return nil }
func (c *stubMessageContext) GetRequestId() string                  { return "req-1" }
func (c *stubMessageContext) GetIp() string                         { return "127.0.0.1" }
func (c *stubMessageContext) GetRequestContext() context.Context    { return context.Background() }

func contextWith(msgCtx core.IMessageContext) context.Context {
	return context.WithValue(context.Background(), core.MessageContextKey, msgCtx)
}

type stubUser struct {
	id string
}

func (u *stubUser) GetId() string          { return u.id }
func (u *stubUser) GetUsername() string    { return "user@example.com" }
func (u *stubUser) GetPassword() string    { return "" }
func (u *stubUser) GetRole() core.UserRole { return core.UserRole("user") }

// ---------------------------------------------------------------------- tests

// TestLogoutOfASessionTheStoreCannotDeleteFailsClosed.
//
// Deleting the row fails, so the server-side session is still live. Expiring the
// cookie in this browser while it is live tells the user they are logged out and
// leaves anyone holding a copy of the cookie authenticated.
func TestLogoutOfASessionTheStoreCannotDeleteFailsClosed(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	repo.deleteErr = errors.New("sessions table is unavailable")

	storage := newDbStorage(repo)
	strategy := &StandardAuthStrategy{sessionManager: storage, csrfService: &CsrfService{}}

	cookies := newStubCookieManager()
	cookies.present[SessionCookieName()] = "sid"
	ctx := contextWith(&stubMessageContext{cookies: cookies})

	err := strategy.Logout(ctx)

	require.Error(t, err, "the caller has to be able to see that the logout did not happen")
	assert.True(t, repo.has("sid"), "the row is still there — the delete failed")
	assert.Empty(t, cookies.expiredCookies(),
		"a logout that could not revoke the server-side session must not report success "+
			"to the browser by expiring its cookie")
}

// TestLogoutIsNotUndoneByAConcurrentRequest.
//
// Request B is inside its database round trip when the session is revoked, and then finishes
// and writes the session back. That is the interleaving that used to re-cache a revoked
// session and hand it to the next request; the entry could not even age out, because every
// request slid its expiry forward.
//
// The revocation is performed from inside B's own Save rather than from a second goroutine,
// which removes the last piece of ordering luck: there is nothing to wait for and nothing to
// sleep on. It is issued through a second storage over the same table because that is now the
// only way the row can vanish underneath B — within one process a save and a revocation for
// one session id are serialised, so the delete would simply queue behind B. The cross-process
// shape is also the harder one: the tombstone that catches a local revocation does not exist
// on the other instance, so nothing but the row itself can stop B.
func TestLogoutIsNotUndoneByAConcurrentRequest(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))

	storage := newDbStorage(repo)
	elsewhere := newDbStorage(repo)

	session := sessionById(t, storage, "sid")
	require.NotNil(t, session)

	revoked := false
	repo.beforeSave = func(id string) {
		if id != "sid" || revoked {
			return
		}
		revoked = true
		require.NoError(t, elsewhere.DeleteSessionById("sid"))
	}

	// Any request does this: SessionMiddleware slides the activity stamp forward.
	session.SetLastActivity(time.Now())

	assert.False(t, repo.has("sid"), "the row must stay deleted")
	assert.Nil(t, sessionById(t, storage, "sid"),
		"a session a concurrent request re-published after the logout must not authenticate")
}

// TestARevocationDoesNotInterleaveWithAnInFlightSave is the intra-process half: the store
// serialises a save and a revocation for one session id, so the interleaving above cannot
// happen locally at all. UpdateSession used to call the repository *before* taking the lock,
// which is what left the window open.
//
// The negative check waits a bounded time for a call that must not arrive. That direction is
// safe: a delete that had already reached the table fails the test, and a scheduler that has
// not yet started the goroutine can only make the check miss a regression, never invent one.
// The assertions after the release are unconditional.
func TestARevocationDoesNotInterleaveWithAnInFlightSave(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	storage := newDbStorage(repo)

	inSave := make(chan struct{}, 1)
	release := make(chan struct{})
	repo.beforeSave = func(id string) {
		select {
		case inSave <- struct{}{}:
			<-release
		default:
		}
	}

	deleteReachedTable := make(chan struct{})
	repo.beforeDelete = func(string) { close(deleteReachedTable) }

	session := sessionById(t, storage, "sid")
	require.NotNil(t, session)

	saveDone := make(chan struct{})
	go func() {
		defer close(saveDone)
		session.SetLastActivity(time.Now())
	}()
	<-inSave

	logoutDone := make(chan error, 1)
	go func() { logoutDone <- storage.DeleteSessionById("sid") }()

	select {
	case <-deleteReachedTable:
		t.Fatal("the revocation reached the table while a save for the same session was " +
			"still in flight; the two must be serialised")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	<-saveDone
	require.NoError(t, <-logoutDone)
	<-deleteReachedTable

	assert.False(t, repo.has("sid"))
	assert.Nil(t, sessionById(t, storage, "sid"))
}

// TestARevokedSessionDoesNotAuthenticateThroughASecondStorage is the cross-process
// case: two storages over one table, which is what a horizontally scaled deployment
// is. No race and no error is needed — the second instance simply never looks at the
// row again.
func TestARevokedSessionDoesNotAuthenticateThroughASecondStorage(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))

	replicaA := newDbStorage(repo)
	replicaB := newDbStorage(repo)

	require.NotNil(t, sessionById(t, replicaB, "sid"), "B serves a request on this cookie")

	require.NoError(t, replicaA.DeleteSessionById("sid"))

	assert.Nil(t, sessionById(t, replicaB, "sid"),
		"a session revoked on one replica must stop authenticating on every other one")
}

// TestDeleteSessionPurgesTheCacheEvenWhenTheDeleteFails: returning before the purge
// leaves the entry that the delete was supposed to revoke.
func TestDeleteSessionPurgesTheCacheEvenWhenTheDeleteFails(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", "user-1"))
	mediator := NewDbSessionMediator(repo)

	_, err := mediator.GetSession("sid")
	require.NoError(t, err)

	repo.deleteErr = errors.New("sessions table is unavailable")
	_, err = mediator.DeleteSession("sid")
	require.Error(t, err)

	mediator.mu.RLock()
	_, cached := mediator.cache["sid"]
	mediator.mu.RUnlock()

	assert.False(t, cached, "a failed revocation must not leave the entry cached")
}

// TestLoginWhosePersistenceFailsDoesNotReportSuccess. Login sets the user id through a
// void setter, so the write-through failure has nowhere to go; the caller is told a
// session was established that no store holds.
func TestLoginWhosePersistenceFailsDoesNotReportSuccess(t *testing.T) {
	repo := newFakeSessionRepo()
	repo.put(liveRow("sid", ""))
	storage := newDbStorage(repo)

	// A factory, because Login now rotates the identifier before it authenticates anybody:
	// it mints a session rather than writing the user id onto the one the client presented.
	factory := NewDbSessionFactory()
	factory.SetMediator(storage.mediator)
	strategy := &StandardAuthStrategy{
		sessionManager: storage,
		csrfService:    &CsrfService{},
		sessionFactory: factory,
	}

	cookies := newStubCookieManager()
	cookies.present[SessionCookieName()] = "sid"
	ctx := contextWith(&stubMessageContext{cookies: cookies})

	repo.saveErr = errors.New("sessions table is read-only")

	session, err := strategy.Login(&stubUser{id: "user-1"}, ctx)

	assert.Error(t, err, "a login whose persistence failed is not a login")
	assert.Nil(t, session)
}
