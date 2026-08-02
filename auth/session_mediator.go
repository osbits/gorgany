package auth

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/osbits/gorgany/v2/db/orm"
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

// SessionIdMaxLength caps how long an identifier may be before this package will look it up,
// store it, or use it as a map key.
//
// The ids this framework mints are 64 hex characters (see StandardAuthStrategy's token
// derivation), so the bound is generous. It is a length bound rather than a format check on
// purpose: ISessionFactory is a public extension point, an app may mint its own shape, and a
// v1 install may still be presenting something else — refusing those would be an outage, while
// refusing a megabyte of attacker-chosen cookie is not.
//
// What it protects is everything downstream that treats the value as a key: the tombstone and
// pending-revocation sets here, the memory store's maps, and the database predicate.
var SessionIdMaxLength = 128

// MaxSessionTombstones and MaxPendingRevocations bound the two sets by cardinality as well as
// by time.
//
// Time alone is not a bound when the key is attacker-chosen. Both sets are keyed on an
// identifier that arrives on a cookie, so on an app whose logout route is not behind the CSRF
// middleware an unauthenticated visitor picks the keys — and, before the length cap above,
// their size too. When a set is full the entry expiring soonest is evicted, which degrades
// that identifier to pre-2.2 behaviour rather than letting the process grow without limit.
var (
	MaxSessionTombstones   = 100_000
	MaxPendingRevocations  = 4_096
	pendingRevocationRetry = time.Second
)

// pendingRevocation is a revocation this process was asked to perform and could not.
type pendingRevocation struct {
	// until is when this record stops being useful — the revoked session's own expiry plus a
	// margin, because after that nothing would honour the session anyway.
	until    time.Time
	attempts int
	lastTry  time.Time
	lastErr  error
}

// validSessionId reports whether an identifier is short enough to be worth handling.
func validSessionId(id string) bool {
	return id != "" && len(id) <= SessionIdMaxLength
}

// DbSessionMediator acts as an intermediary between session entities and the database
// It ensures that all session changes are automatically persisted
type DbSessionMediator struct {
	sessionRepo ISessionRepository

	// mu guards cache, tombstones, pending and lastTombstoneSweep. It is never held across a
	// database call; ids serialises those.
	mu         sync.RWMutex
	cache      map[string]*DbSessionEntity
	tombstones map[string]time.Time

	// pending are revocations this process was asked to perform and could not, kept apart
	// from tombstones because the two mean opposite things about the row. A tombstone says
	// the row is gone and this process should stop serving the id; a pending revocation says
	// the row is *still there* and somebody has to try again. Conflating them is what made a
	// failed logout look handled: the id stopped resolving here, so the next request minted a
	// replacement session and overwrote the client's only copy of the identifier that still
	// needed revoking.
	pending map[string]*pendingRevocation

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
		pending:     make(map[string]*pendingRevocation),
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
// resolve the session separately and have to see each other's writes.
//
// The row is authoritative for the session's *contents* as well, and it did not used to be.
// A cache hit kept the local user id, attributes and activity stamp and took the row's expiry
// only when it was later — a merge that picks the least restrictive of the two by
// construction. Every security-relevant direction was the losing one: an attribute another
// replica cleared stayed set here, an identity it removed stayed present, an expiry it
// shortened stayed long. Adoption replaces that: whatever the row says, the cached object now
// says, and a change this process has made but not yet written survives as a pending write
// rather than as a value that outvotes the database.
func (m *DbSessionMediator) GetSession(id string) (*DbSessionEntity, error) {
	if !validSessionId(id) {
		return nil, nil
	}

	unlock := m.ids.lock(id)
	defer unlock()

	// A revocation this process could not complete is retried here, on the request that
	// presents the identifier again. That is deliberately the only trigger it needs: it is the
	// same request that would otherwise be handed a replacement session, so the retry happens
	// exactly when the client is still able to tell us which id to revoke.
	m.retryPendingRevocation(id)

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

	cached.AdoptPersistedState(row)

	return cached, nil
}

// CreateSession creates a new session and persists it
func (m *DbSessionMediator) CreateSession(session *DbSessionEntity) (*DbSessionEntity, error) {
	id := session.GetId()
	if !validSessionId(id) {
		return nil, fmt.Errorf(
			"session identifier is empty or longer than %d characters", SessionIdMaxLength)
	}

	unlock := m.ids.lock(id)
	defer unlock()

	if m.isRevoked(id) {
		err := fmt.Errorf("session %s has been revoked and will not be recreated", id)
		session.notePersistFailure(err)
		return nil, err
	}

	if err := m.persist(session); err != nil {
		session.notePersistFailure(err)
		return nil, err
	}
	session.notePersistSuccess()

	m.mu.Lock()
	m.cache[id] = session
	m.mu.Unlock()

	return session, nil
}

// sessionConflictRetries bounds how many times a write that lost a version race is
// reconciled and retried before it is reported as a failure.
//
// Contention on a single session is between the tabs of one browser, so the realistic count
// is one or two; a session that cannot converge in three rounds is being written by something
// that is not going to stop, and looping would turn that into a hung request.
var sessionConflictRetries = 3

// UpdateSession persists session changes to database
func (m *DbSessionMediator) UpdateSession(session *DbSessionEntity) error {
	id := session.GetId()
	if !validSessionId(id) {
		err := fmt.Errorf(
			"session identifier is empty or longer than %d characters", SessionIdMaxLength)
		session.notePersistFailure(err)
		return err
	}

	unlock := m.ids.lock(id)
	defer unlock()

	if m.isRevoked(id) {
		err := fmt.Errorf("session %s has been revoked and cannot be updated", id)
		session.notePersistFailure(err)
		return err
	}

	// Whether the write landed is recorded here rather than in the caller, because this is
	// the one place every write goes through. It used to be recorded in
	// DbSessionEntityWithMediator.persist, which the void setters reach — but AddSession
	// calls this method directly, so the operation most likely to be *asked* whether the
	// state is durable was the one that never recorded an answer.
	if err := m.persistReconciling(session); err != nil {
		session.notePersistFailure(err)
		return err
	}
	session.notePersistSuccess()

	m.mu.Lock()
	m.cache[id] = session
	m.mu.Unlock()

	return nil
}

// persistReconciling writes the session, re-reading and replaying if another writer got there
// first.
//
// A guarded write refuses when the row's version is not the one the entity read — which is
// the whole point, but on its own it would turn every concurrent write into a failed request.
// So a conflict is not the answer, it is a signal to go and look: re-read the authoritative
// row, adopt it, replay this session's own pending changes on top, and try again against the
// version that is actually there. What that converges on is both writers' changes, rather
// than whichever of them happened to run last.
//
// A row that has disappeared is not retried. It means the session was revoked while this
// write was in flight, and re-creating it is the revocation bypass AddSession already refuses.
func (m *DbSessionMediator) persistReconciling(session *DbSessionEntity) error {
	for attempt := 0; ; attempt++ {
		err := m.persist(session)
		if !errors.Is(err, orm.ErrRowConflict) {
			return err
		}

		if attempt >= sessionConflictRetries {
			return fmt.Errorf(
				"session %s was modified by another writer on each of %d attempts: %w",
				session.GetId(), sessionConflictRetries+1, err)
		}

		row, findErr := m.sessionRepo.FindById(session.GetId())
		if findErr != nil {
			return fmt.Errorf(
				"session %s conflicted and could not be re-read: %w", session.GetId(), findErr)
		}
		if row == nil {
			return fmt.Errorf("%w: session %s was revoked while it was being written",
				orm.ErrRowGone, session.GetId())
		}

		session.AdoptPersistedState(row)

		if !session.hasPendingWrite() {
			// Everything this writer wanted is already reflected in the row it just adopted.
			return nil
		}
	}
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
// both the key and its length; SessionIdMaxLength and MaxSessionTombstones bound that now.
//
// A revocation that *failed* is recorded too, but as a pending revocation and not as a
// tombstone, because the two say opposite things about the row. Recording it as a tombstone
// meant this process stopped resolving the id while the row was still live — so the next
// request found nothing, minted a replacement session, and wrote its cookie over the client's
// only copy of the identifier that still needed revoking. The user then pressed "try again"
// and revoked the replacement, the original row stayed authenticated, and a copy of the
// original cookie kept working on every other replica. Keeping the id in `pending` retains the
// refusal (isRevoked consults both) *and* the obligation.
func (m *DbSessionMediator) DeleteSession(id string) (bool, error) {
	if !validSessionId(id) {
		// Nothing to revoke and nothing worth remembering under a key this long.
		return false, nil
	}

	unlock := m.ids.lock(id)
	defer unlock()

	// The expiry has to be read before the row goes, so the tombstone can outlive exactly the
	// session it protects against rather than a fixed day.
	retainUntil := m.revocationRetentionFor(id)

	deleted, err := m.sessionRepo.DeleteById(id)

	now := time.Now()
	m.mu.Lock()
	delete(m.cache, id)
	switch {
	case err != nil:
		m.rememberPendingLocked(id, retainUntil, now, err)
	case deleted:
		m.rememberTombstoneLocked(id, retainUntil)
		delete(m.pending, id)
	}
	m.sweepTombstonesLocked(now)
	m.mu.Unlock()

	if err != nil {
		return false, err
	}
	return deleted, nil
}

// revocationRetentionFor is how long this process should remember what it did to an id.
//
// Long enough to outlive the session itself: once the row's own expiry has passed nothing
// would honour it anyway, so that plus a margin is the natural bound. A fixed 25 hours was
// both too long for a short-lived session and too short for an app configuring a longer
// lifetime — the case where the memory actually has to hold.
func (m *DbSessionMediator) revocationRetentionFor(id string) time.Time {
	fallback := time.Now().Add(SessionTombstoneRetention)

	m.mu.RLock()
	cached, ok := m.cache[id]
	m.mu.RUnlock()
	if !ok {
		return fallback
	}

	expiry := cached.GetExpiry().Add(tombstoneMargin)
	if expiry.After(fallback) {
		return expiry
	}
	return fallback
}

// tombstoneMargin is the slack a revocation memory keeps past the session's own expiry, to
// cover clock skew between replicas.
var tombstoneMargin = time.Hour

func (m *DbSessionMediator) rememberTombstoneLocked(id string, until time.Time) {
	evictOldestLocked(m.tombstones, MaxSessionTombstones)
	m.tombstones[id] = until
}

func (m *DbSessionMediator) rememberPendingLocked(id string, until time.Time, now time.Time, err error) {
	if existing, ok := m.pending[id]; ok {
		existing.attempts++
		existing.lastTry = now
		existing.lastErr = err
		return
	}

	if len(m.pending) >= MaxPendingRevocations {
		evictSoonestPendingLocked(m.pending)
	}
	m.pending[id] = &pendingRevocation{until: until, attempts: 1, lastTry: now, lastErr: err}
}

// evictOldestLocked drops the entry expiring soonest when the set is at its cap.
func evictOldestLocked(entries map[string]time.Time, limit int) {
	if limit <= 0 || len(entries) < limit {
		return
	}

	var oldestKey string
	var oldest time.Time
	for key, until := range entries {
		if oldestKey == "" || until.Before(oldest) {
			oldestKey, oldest = key, until
		}
	}
	delete(entries, oldestKey)
}

func evictSoonestPendingLocked(entries map[string]*pendingRevocation) {
	var soonestKey string
	var soonest time.Time
	for key, entry := range entries {
		if soonestKey == "" || entry.until.Before(soonest) {
			soonestKey, soonest = key, entry.until
		}
	}
	if soonestKey != "" {
		delete(entries, soonestKey)
	}
}

// RevocationPending reports that this process was asked to revoke this id and could not.
//
// StandardAuthStrategy consults it before minting a replacement session, because the client's
// copy of a stuck identifier is the only remaining handle on a session that still has to be
// revoked — and overwriting it with a new cookie is what turned a retryable failure into a
// permanent one.
func (m *DbSessionMediator) RevocationPending(id string) bool {
	m.mu.RLock()
	entry, ok := m.pending[id]
	m.mu.RUnlock()

	return ok && entry.until.After(time.Now())
}

// retryPendingRevocationLocked is the retry hook. The caller holds ids.lock(id) and not m.mu.
//
// It runs from GetSession, which is exactly the request that would otherwise be handed a
// replacement cookie — so the client's own retry is what drives the repair, with no scheduler
// and no background goroutine.
func (m *DbSessionMediator) retryPendingRevocation(id string) {
	now := time.Now()

	m.mu.RLock()
	entry, ok := m.pending[id]
	m.mu.RUnlock()

	if !ok || now.Sub(entry.lastTry) < pendingRevocationRetry {
		return
	}

	deleted, err := m.sessionRepo.DeleteById(id)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		entry.attempts++
		entry.lastTry = time.Now()
		entry.lastErr = err
		return
	}

	// Either this delete removed the row or somebody else already had: both mean the
	// obligation is discharged, and the id must stay refused for as long as the session it
	// named could have been honoured.
	_ = deleted
	delete(m.pending, id)
	m.rememberTombstoneLocked(id, entry.until)
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

	// The sweep is the second retry hook, and it exists for the ids GetSession will never see
	// again: a client that threw its cookie away, or was never coming back. Without it a
	// revocation nobody re-presents stays owed until its own expiry passes.
	if retryErr := m.retryPendingRevocations(); retryErr != nil && err == nil {
		err = retryErr
	}

	return err
}

// retryPendingRevocations re-attempts everything still owed, and reports whether any of it
// still cannot be done — so the scheduled job and `session:gc` can exit nonzero rather than
// reporting a sweep that silently left live sessions behind.
func (m *DbSessionMediator) retryPendingRevocations() error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.pending))
	for id := range m.pending {
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	// The per-id lock is taken inside the loop and m.mu is not held across it: this type's
	// invariant is that mu is never held across a database call.
	stillOwed := 0
	for _, id := range ids {
		func() {
			unlock := m.ids.lock(id)
			defer unlock()
			m.retryPendingRevocation(id)
		}()
		if m.RevocationPending(id) {
			stillOwed++
		}
	}

	if stillOwed > 0 {
		return fmt.Errorf(
			"%d session revocation(s) could not be completed and are still owed", stillOwed)
	}
	return nil
}

// ClearCache removes all sessions from cache (useful for testing or memory management)
func (m *DbSessionMediator) ClearCache() {
	m.mu.Lock()
	m.cache = make(map[string]*DbSessionEntity)
	m.mu.Unlock()
}

// isRevoked reports whether this process revoked this id recently, or was asked to and could
// not.
//
// Both sets refuse, and for the same reason from opposite directions: a tombstoned session no
// longer exists, and a pending one was supposed to stop existing. Serving either would undo a
// decision somebody already made. It is only ever an extra refusal on top of the row check in
// GetSession, never a substitute for it: a revocation performed by another process leaves
// nothing here, which is exactly why the row is consulted as well.
func (m *DbSessionMediator) isRevoked(id string) bool {
	now := time.Now()

	m.mu.RLock()
	until, tombstoned := m.tombstones[id]
	entry, isPending := m.pending[id]
	m.mu.RUnlock()

	if tombstoned && until.After(now) {
		return true
	}
	return isPending && entry.until.After(now)
}

// sweepTombstonesLocked drops revocation memories that have outlived anything they could
// protect. The caller holds m.mu.
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
	for id, entry := range m.pending {
		if !entry.until.After(now) {
			delete(m.pending, id)
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
//
// Logging is not on its own enough, and that was the residual defect: an operator saw the
// line, the caller saw nothing. Two paths reach the client with no error-bearing call after
// them — the CSRF endpoint, which hands out a token, and SessionMiddleware's per-request
// heartbeat — so the failure is also *remembered*, by UpdateSession, and surfaced through
// core.PendingWriteError by whoever is in a position to act on it.
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
