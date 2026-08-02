package auth

import (
	"fmt"
	"sync"
	"time"

	grgErr "github.com/osbits/gorgany/v2/err"
)

// SessionTombstoneRetention is how long a revoked session id is remembered.
//
// A tombstone only has to outlive the object it protects against: once the revoked
// session's own expiry has passed, nothing would accept it anyway. One session lifetime
// plus a margin is therefore the natural bound, and it makes the tombstone set at most
// "revocations per lifetime" entries rather than unbounded. A variable so an app with an
// unusually long lifetime can raise it.
var SessionTombstoneRetention = 25 * time.Hour

// tombstoneSweepInterval bounds how often the tombstone set is walked. The set is small
// and the pass is O(n), so doing it on every revocation would be wasteful without being
// any more correct.
var tombstoneSweepInterval = time.Minute

// DbSessionMediator acts as an intermediary between session entities and the database
// It ensures that all session changes are automatically persisted
type DbSessionMediator struct {
	sessionRepo ISessionRepository

	// mu guards cache, tombstones and lastTombstoneSweep. It is never held across a
	// database call; ids serialises those.
	mu         sync.RWMutex
	cache      map[string]*DbSessionEntity
	tombstones map[string]time.Time

	lastTombstoneSweep time.Time

	// ids serialises everything done for one session id — the database call and the
	// cache mutation together. Without it, a save and a delete for the same id
	// interleave: UpdateSession used to call the repository *before* taking the lock, so
	// a request already inside its round trip when a logout deleted the row would put the
	// session pointer straight back into the cache afterwards, and the re-cached entry
	// authenticated. It locks per id, so requests for different sessions still run
	// concurrently and only the two requests actually contending for one session wait.
	ids keyedMutex
}

func NewDbSessionMediator(sessionRepo ISessionRepository) *DbSessionMediator {
	return &DbSessionMediator{
		sessionRepo: sessionRepo,
		cache:       make(map[string]*DbSessionEntity),
		tombstones:  make(map[string]time.Time),
	}
}

// GetSession returns the session for this id, or nil when there is none.
//
// It checks the row every time, and that is the point. The cache used to answer from
// memory and never look again, which made revocation a per-process affair: the cache is
// invalidated only by this process's own delete, ClearCache had no callers at all, and so
// a logout handled by one replica left the session authenticating on every other replica
// for as long as its expiry kept sliding forward — no race and no error needed, just two
// processes, which is the deployment database-backed sessions exist for. The scheduled
// sweep and `session:gc` had the same hole: rows one instance deletes stay live in another
// instance's cache.
//
// So the cache no longer decides whether a session exists; the row does. What the cache
// still provides is identity — one *DbSessionEntity per id per process — which the request
// pipeline relies on, because SessionMiddleware and the request's session scope each
// resolve the session separately and have to see each other's writes. Expiry is adopted
// from the row when the row's is later, because expiry is the one field another replica
// moves forward on every request and a stale local copy would expire a session that is
// alive elsewhere; taking the later of the two can only extend a live session, never
// resurrect a revoked one, because a revoked one has no row to read.
func (m *DbSessionMediator) GetSession(id string) (*DbSessionEntity, error) {
	if id == "" {
		return nil, nil
	}

	unlock := m.ids.lock(id)
	defer unlock()

	if m.isRevoked(id) {
		return nil, nil
	}

	row, err := m.sessionRepo.FindById(id)
	if err != nil {
		return nil, err
	}

	if row == nil {
		m.mu.Lock()
		delete(m.cache, id)
		m.mu.Unlock()
		return nil, nil
	}

	m.mu.Lock()
	cached, exists := m.cache[id]
	if !exists {
		m.cache[id] = row
	}
	m.mu.Unlock()

	if !exists {
		return row, nil
	}

	if rowExpiry := row.GetExpiry(); rowExpiry.After(cached.GetExpiry()) {
		cached.SetExpiry(rowExpiry)
	}

	return cached, nil
}

// CreateSession creates a new session and persists it
func (m *DbSessionMediator) CreateSession(session *DbSessionEntity) (*DbSessionEntity, error) {
	id := session.GetId()

	unlock := m.ids.lock(id)
	defer unlock()

	if m.isRevoked(id) {
		return nil, fmt.Errorf("session %s has been revoked and will not be recreated", id)
	}

	if err := m.persist(session); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.cache[id] = session
	m.mu.Unlock()

	return session, nil
}

// UpdateSession persists session changes to database
func (m *DbSessionMediator) UpdateSession(session *DbSessionEntity) error {
	id := session.GetId()

	unlock := m.ids.lock(id)
	defer unlock()

	if m.isRevoked(id) {
		return fmt.Errorf("session %s has been revoked and cannot be updated", id)
	}

	if err := m.persist(session); err != nil {
		return err
	}

	m.mu.Lock()
	m.cache[id] = session
	m.mu.Unlock()

	return nil
}

// persist hands the repository a detached copy and copies the outcome back.
//
// The repository — and through it the ORM and the driver — must never see the live entity:
// see DbSessionEntity.Snapshot for why, and AttributesMap.Value for what happens when it
// does.
func (m *DbSessionMediator) persist(session *DbSessionEntity) error {
	snapshot := session.Snapshot()
	if err := m.sessionRepo.Save(snapshot); err != nil {
		return err
	}
	session.AdoptPersistedMeta(snapshot)
	return nil
}

// DeleteSession revokes a session and reports whether the store held it.
//
// Two things changed here. The cache is purged unconditionally, before the error is
// returned: it used to return early on a repository failure, so a delete that failed —
// including the "domain cannot be nil" the repository produced for an already-absent row —
// left the very entry it was asked to revoke alive in the cache. And the id is
// tombstoned, so a concurrent request that is inside its own database round trip cannot
// re-cache or re-insert the session when it finishes. The tombstone is what a mutex alone
// does not give: serialising update against delete decides who goes first, not what the
// loser is then allowed to do.
//
// Nothing is remembered when the statement cleanly reported that there was no such row.
// Revoking is idempotent, so that answer is normal, and there is then no session for a
// tombstone to protect — while the entry itself would live for SessionTombstoneRetention
// under a key the caller chose. The id revoked comes off the session cookie, so on an app
// whose logout route is not behind the CSRF middleware an unauthenticated visitor picks
// both the key and its length, which turns one request into a day of retained memory. A
// revocation that *failed* is remembered, though: the row is still there and this process
// was told to get rid of it, so refusing to serve it is the fail-closed direction.
func (m *DbSessionMediator) DeleteSession(id string) (bool, error) {
	unlock := m.ids.lock(id)
	defer unlock()

	deleted, err := m.sessionRepo.DeleteById(id)

	now := time.Now()
	m.mu.Lock()
	delete(m.cache, id)
	if deleted || err != nil {
		m.tombstones[id] = now.Add(SessionTombstoneRetention)
	}
	m.sweepTombstonesLocked(now)
	m.mu.Unlock()

	if err != nil {
		return false, err
	}
	return deleted, nil
}

// ClearExpired removes expired sessions from database and cache.
//
// The cache pass runs whether or not the delete succeeded: an entry this process believes
// is expired must stop being served regardless, and the error is still returned so the job
// or the command can report the sweep as failed.
func (m *DbSessionMediator) ClearExpired() error {
	err := m.sessionRepo.DeleteExpired()

	now := time.Now()
	m.mu.Lock()
	for id, session := range m.cache {
		if session.IsExpired() {
			delete(m.cache, id)
		}
	}
	m.sweepTombstonesLocked(now)
	m.mu.Unlock()

	return err
}

// ClearCache removes all sessions from cache (useful for testing or memory management)
func (m *DbSessionMediator) ClearCache() {
	m.mu.Lock()
	m.cache = make(map[string]*DbSessionEntity)
	m.mu.Unlock()
}

// isRevoked reports whether this process revoked this id recently.
//
// It is only ever an extra refusal on top of the row check in GetSession, never a
// substitute for it: a revocation performed by another process leaves no tombstone here,
// which is exactly why the row is consulted as well.
func (m *DbSessionMediator) isRevoked(id string) bool {
	m.mu.RLock()
	until, tombstoned := m.tombstones[id]
	m.mu.RUnlock()

	return tombstoned && until.After(time.Now())
}

// sweepTombstonesLocked drops tombstones that have outlived anything they could protect.
// The caller holds m.mu.
func (m *DbSessionMediator) sweepTombstonesLocked(now time.Time) {
	if now.Sub(m.lastTombstoneSweep) < tombstoneSweepInterval {
		return
	}
	m.lastTombstoneSweep = now

	for id, until := range m.tombstones {
		if !until.After(now) {
			delete(m.tombstones, id)
		}
	}
}

// keyedMutex hands out one mutex per key, and keeps a mutex alive only while somebody
// holds or waits for it.
//
// A plain map[string]*sync.Mutex would grow one entry per session id ever seen, which on a
// busy server is a leak with the same shape as the one the session sweep exists to close.
// Reference counting keeps it bounded by the number of requests in flight.
type keyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu       sync.Mutex
	refCount int
}

// lock takes the mutex for key and returns the function that releases it.
func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLock)
	}
	entry, exists := k.locks[key]
	if !exists {
		entry = &keyedLock{}
		k.locks[key] = entry
	}
	entry.refCount++
	k.mu.Unlock()

	entry.mu.Lock()

	return func() {
		entry.mu.Unlock()

		k.mu.Lock()
		entry.refCount--
		if entry.refCount == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// DbSessionEntityWithMediator wraps DbSessionEntity with automatic persistence
type DbSessionEntityWithMediator struct {
	*DbSessionEntity
	mediator *DbSessionMediator
}

func NewDbSessionEntityWithMediator(entity *DbSessionEntity, mediator *DbSessionMediator) *DbSessionEntityWithMediator {
	return &DbSessionEntityWithMediator{
		DbSessionEntity: entity,
		mediator:        mediator,
	}
}

// Override methods to ensure automatic persistence.
//
// Each of these mutates the entity and then writes it back, and each of them used to call
// UpdateSession as a bare statement — six dropped errors, so a session whose store was
// unreachable behaved exactly like one that had been saved. ISession's setters are void
// and stay void (see core.ISession), so they have nowhere to return the failure; it is
// reported here instead, and the enclosing operation that *can* report one does so on its
// own terms. Login is the case that matters: it sets the user id through SetUserId and
// then asks the storage to persist the session, so a login whose write never landed is
// reported as a failed login rather than as a session nothing holds.
func (s *DbSessionEntityWithMediator) persist() {
	if err := s.mediator.UpdateSession(s.DbSessionEntity); err != nil {
		grgErr.HandleError(fmt.Errorf("could not persist session %s: %w", s.GetId(), err))
	}
}

func (s *DbSessionEntityWithMediator) SetUserId(id string) {
	s.DbSessionEntity.SetUserId(id)
	s.persist()
}

func (s *DbSessionEntityWithMediator) SetLastActivity(t time.Time) {
	s.DbSessionEntity.SetLastActivity(t)
	s.persist()
}

func (s *DbSessionEntityWithMediator) SetItem(key string, value string) {
	s.DbSessionEntity.SetItem(key, value)
	s.persist()
}

func (s *DbSessionEntityWithMediator) ClearItem(attribute string) {
	s.DbSessionEntity.ClearItem(attribute)
	s.persist()
}

func (s *DbSessionEntityWithMediator) ClearItems() {
	s.DbSessionEntity.ClearItems()
	s.persist()
}

func (s *DbSessionEntityWithMediator) SetExpiry(t time.Time) {
	s.DbSessionEntity.SetExpiry(t)
	s.persist()
}
