package auth

import (
	"fmt"
	"sync"
	"time"

	"github.com/spf13/viper"

	"github.com/osbits/gorgany/v2/app/core"
)

func NewSession(id string, expiry time.Time) *Session {
	now := time.Now()
	return &Session{
		id:           id,
		expiry:       expiry,
		createdAt:    now,
		lastActivity: now,
	}
}

type Session struct {
	id           string
	expiry       time.Time
	userId       string
	attributes   map[string]string
	createdAt    time.Time
	lastActivity time.Time
	mu           sync.Mutex
}

// Every accessor on Session takes mu. The policy is deliberately uniform rather than
// applied field by field: id and createdAt are written only by the constructor, before the
// session is published, but userId and the rest are read and written by every concurrent
// request carrying the cookie, and a per-field judgement is how GetUserId came to be
// unguarded while GetExpiry was locked. userId is the field authorization is derived from,
// and the login handler overwrites it on a session other requests are already authorizing
// against.
func (thiz *Session) GetId() string {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	return thiz.id
}

func (thiz *Session) SetItem(key string, value string) {
	thiz.mu.Lock()
	if thiz.attributes == nil {
		thiz.attributes = make(map[string]string)
	}
	thiz.attributes[key] = value
	thiz.mu.Unlock()
}

func (thiz *Session) GetItem(key string) string {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	if value, ok := thiz.attributes[key]; ok {
		return value
	}
	return ""
}

func (thiz *Session) GetUserId() string {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	return thiz.userId
}

func (thiz *Session) SetUserId(id string) {
	thiz.mu.Lock()
	thiz.userId = id
	thiz.mu.Unlock()
}

// IsExpired, SetExpiry and GetExpiry all take mu.
//
// They did not, and expiry is written on every single request — SessionMiddleware calls
// SetExpiry to slide the window — while the session sweep reads it through IsExpired. The
// race detector reports it at memory_session.go:62 against :137. A time.Time is three words,
// so a torn read can produce a timestamp that never existed and expire a live session or keep
// a dead one.
//
// It went unseen because nothing swept on a schedule: ClearExpiredSessions had no caller.
// H4 gives it one, which is exactly why this had to be fixed in the same change.
func (thiz *Session) IsExpired() bool {
	return thiz.GetExpiry().Before(time.Now())
}

func (thiz *Session) SetExpiry(t time.Time) {
	thiz.mu.Lock()
	thiz.expiry = t
	thiz.mu.Unlock()
}

func (thiz *Session) GetExpiry() time.Time {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	return thiz.expiry
}

func (thiz *Session) ClearItem(attribute string) {
	thiz.mu.Lock()
	delete(thiz.attributes, attribute)
	thiz.mu.Unlock()
}

func (thiz *Session) ClearItems() {
	thiz.mu.Lock()
	thiz.attributes = make(map[string]string)
	thiz.mu.Unlock()
}

func (thiz *Session) GetCreatedAt() time.Time {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	return thiz.createdAt
}

func (thiz *Session) GetLastActivity() time.Time {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	return thiz.lastActivity
}

func (thiz *Session) SetLastActivity(t time.Time) {
	thiz.mu.Lock()
	thiz.lastActivity = t
	thiz.mu.Unlock()
}

type MemorySession struct {
	sessionLifetime time.Duration
	sessions        map[string]core.ISession
	mu              sync.Mutex

	sessionRotationInterval time.Duration
	sessionActivityTimeout  time.Duration

	// lastSweep is when AddSession last evicted expired sessions. Guarded by mu.
	lastSweep time.Time

	// tombstones remembers ids this store revoked, so a caller still holding the session
	// object cannot write it back. Guarded by mu. See AddSession.
	tombstones         map[string]time.Time
	lastTombstoneSweep time.Time
}

func NewMemorySession(sessionLifetime time.Duration) *MemorySession {
	rotationInterval := viper.GetDuration("auth.session.rotationInterval")
	if rotationInterval.Minutes() == 0 {
		rotationInterval = 24 * time.Hour
	}

	activityTimeout := viper.GetDuration("auth.session.activityTimeout")
	if activityTimeout.Minutes() == 0 {
		activityTimeout = 30 * time.Minute
	}

	return &MemorySession{
		sessions:                make(map[string]core.ISession),
		tombstones:              make(map[string]time.Time),
		sessionLifetime:         sessionLifetime,
		sessionRotationInterval: rotationInterval,
		sessionActivityTimeout:  activityTimeout,
	}
}

func (thiz *MemorySession) SetSessionLifetime(lifetime time.Duration) {
	thiz.sessionLifetime = lifetime
}

// ClearExpiredSessions deletes every expired session.
//
// It used to range over thiz.sessions with **no lock held**, taking the lock only around each
// individual delete:
//
//	for key, session := range thiz.sessions {   // unguarded read
//	    if session.IsExpired() {
//	        thiz.mu.Lock()
//	        delete(thiz.sessions, key)
//	        thiz.mu.Unlock()
//	    }
//	}
//
// Every other method on this type locks correctly, so the sweep raced against all of them.
// On a Go map a concurrent iteration and write is not merely a torn value: the runtime
// detects it and raises `fatal error: concurrent map iteration and map write`, which
// RecoveryMiddleware cannot catch — it takes the process down rather than the request. Same
// failure mode as the event bus race (G1).
//
// It survived because nothing called it. H4 schedules it, so the race became reachable in
// the same change that made the sweep run.
//
// The expired set is collected under the lock and the deletes happen under the same
// acquisition. session.IsExpired() takes the *session's* mutex, not this one, so calling it
// from inside the critical section cannot deadlock.
// It returns an error only because ISessionStorage's contract has to accommodate a store
// that can fail; an in-memory map cannot, so this one never does.
func (thiz *MemorySession) ClearExpiredSessions() error {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()

	thiz.evictExpiredLocked()
	thiz.sweepTombstonesLocked(time.Now())

	return nil
}

// MemorySweepInterval bounds how often AddSession evicts expired sessions.
//
// Short and *not* derived from the session lifetime, unlike the scheduled job's interval. A
// sweep can only remove sessions that have already expired, so the map necessarily holds
// everything created within one lifetime — that part is inherent to memory storage and no
// sweep frequency changes it. What the interval controls is how far past that bound the map
// drifts, so a small fixed value is right, and the pass is cheap precisely because sweeping
// keeps the map small.
var MemorySweepInterval = time.Minute

// AddSession stores a session and occasionally evicts expired ones.
//
// The eviction is here because this is the only method that grows the map, and because a
// memory store has to bound itself. H4 registered the scheduled sweep for
// `auth.session.storage: database` only, on the reasoning that memory sessions "are collected
// when the process exits" — which is not a bound for a server that runs for weeks. It left
// MemorySession.ClearExpiredSessions with no caller at all: the same hole H4 set out to close,
// for the other backend, and for the *default* one, since AppProvider treats anything that is
// not "database" as memory.
//
// Fixing that by registering the job for memory storage would have been wrong. It would make
// an in-process bound depend on an app remembering to wire JobProvider, and an app that does
// not would still leak. The store owns its own memory.
//
// SessionMiddleware already deletes an expired session when its client comes back
// (session_middleware.go:79), so this is specifically about sessions whose client never
// returns — a crawler, a scanner, a client that discards cookies. Those are never read again,
// so read-through eviction cannot reach them and only a sweep can.
//
// It refuses an id this store has revoked. Session ids are 256 bits of hash, so an id is
// never legitimately re-used and "add the session that was just deleted" always means a
// caller is holding a stale object across a revocation. The reachable case is the CSRF token
// endpoint, which resolved the session at the top of the handler and used to upsert it at
// the bottom unconditionally — a plain map insert here, on a route the framework registers
// by default and that needs no authentication, which put a revoked session with its user id
// intact straight back into the store.
func (thiz *MemorySession) AddSession(session core.ISession) error {
	id := session.GetId()
	now := time.Now()

	thiz.mu.Lock()
	defer thiz.mu.Unlock()

	if until, revoked := thiz.tombstones[id]; revoked && until.After(now) {
		return fmt.Errorf("session %s has been revoked and will not be stored again", id)
	}

	thiz.sessions[id] = session
	thiz.sweepLocked(now)

	return nil
}

// sweepLocked evicts expired sessions at most once per MemorySweepInterval.
//
// The caller holds thiz.mu. The time guard means the O(n) pass costs one request per interval
// rather than every session creation.
func (thiz *MemorySession) sweepLocked(now time.Time) {
	if now.Sub(thiz.lastSweep) < MemorySweepInterval {
		return
	}
	thiz.lastSweep = now

	thiz.evictExpiredLocked()
	thiz.sweepTombstonesLocked(now)
}

// sweepTombstonesLocked drops tombstones that have outlived anything they could protect: a
// revoked session's own expiry has passed, so nothing would accept it any more. The caller
// holds thiz.mu.
func (thiz *MemorySession) sweepTombstonesLocked(now time.Time) {
	if now.Sub(thiz.lastTombstoneSweep) < tombstoneSweepInterval {
		return
	}
	thiz.lastTombstoneSweep = now

	for id, until := range thiz.tombstones {
		if !until.After(now) {
			delete(thiz.tombstones, id)
		}
	}
}

// revokeLocked deletes a session and remembers that it did. The caller holds thiz.mu.
//
// Only a session this store actually held is remembered. Revoking is idempotent, so being
// asked to remove an id that is not here is normal and leaves nothing for a tombstone to
// protect — while the entry would sit in the map for SessionTombstoneRetention under a key
// the caller supplied. The id comes off the session cookie, so on an app whose logout
// route is not behind the CSRF middleware every unauthenticated request could add a
// kilobytes-long key that nothing removes for a day, which is the unbounded growth the
// sweep above exists to prevent, arriving through the other door.
func (thiz *MemorySession) revokeLocked(id string) bool {
	_, held := thiz.sessions[id]
	delete(thiz.sessions, id)

	now := time.Now()
	if held {
		if thiz.tombstones == nil {
			thiz.tombstones = make(map[string]time.Time)
		}
		thiz.tombstones[id] = now.Add(SessionTombstoneRetention)
	}
	thiz.sweepTombstonesLocked(now)

	return held
}

// Len is the number of sessions currently held.
//
// Exported because it is the only way to observe that the store bounds itself — the property
// H4 got wrong — and because an app running memory sessions has no other way to see the size
// of the thing living in its heap. MemoryRateLimitStore.Len exists for the same reason.
func (thiz *MemorySession) Len() int {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()
	return len(thiz.sessions)
}

// evictExpiredLocked removes every expired session. The caller holds thiz.mu.
//
// session.IsExpired() takes the *session's* mutex, not this one, so calling it from inside the
// critical section cannot deadlock.
func (thiz *MemorySession) evictExpiredLocked() {
	for key, session := range thiz.sessions {
		if session.IsExpired() {
			delete(thiz.sessions, key)
		}
	}
}

func (thiz *MemorySession) DeleteSession(session core.ISession) error {
	return thiz.DeleteSessionById(session.GetId())
}

func (thiz *MemorySession) DeleteSessionById(id string) error {
	thiz.mu.Lock()
	thiz.revokeLocked(id)
	thiz.mu.Unlock()

	return nil
}

// RevokeSession implements core.ISessionRevoker.
func (thiz *MemorySession) RevokeSession(id string) (bool, error) {
	thiz.mu.Lock()
	held := thiz.revokeLocked(id)
	thiz.mu.Unlock()

	return held, nil
}

func (thiz *MemorySession) GetSessionById(id string) (core.ISession, error) {
	thiz.mu.Lock()
	session := thiz.sessions[id]
	thiz.mu.Unlock()

	if session == nil {
		return nil, nil
	}
	return session, nil
}

func (thiz *MemorySession) GetSessionLifetime() time.Duration {
	return thiz.sessionLifetime
}

func (thiz *MemorySession) GetSessionRotationInterval() time.Duration {
	return thiz.sessionRotationInterval
}

func (thiz *MemorySession) GetSessionActivityTimeout() time.Duration {
	return thiz.sessionActivityTimeout
}

var (
	_ core.ISessionStorage = (*MemorySession)(nil)
	_ core.ISessionRevoker = (*MemorySession)(nil)
)
