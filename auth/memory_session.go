package auth

import (
	"github.com/spf13/viper"
	"sync"
	"time"

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

func (thiz *Session) GetId() string {
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
	return thiz.userId
}

func (thiz *Session) SetUserId(id string) {
	thiz.userId = id
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
func (thiz *MemorySession) ClearExpiredSessions() {
	thiz.mu.Lock()
	defer thiz.mu.Unlock()

	thiz.evictExpiredLocked()
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
func (thiz *MemorySession) AddSession(session core.ISession) {
	thiz.mu.Lock()
	thiz.sessions[session.GetId()] = session
	thiz.sweepLocked(time.Now())
	thiz.mu.Unlock()
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

func (thiz *MemorySession) DeleteSession(session core.ISession) {
	thiz.mu.Lock()
	delete(thiz.sessions, session.GetId())
	thiz.mu.Unlock()
}

func (thiz *MemorySession) DeleteSessionById(id string) {
	thiz.mu.Lock()
	delete(thiz.sessions, id)
	thiz.mu.Unlock()
}

func (thiz *MemorySession) GetSessionById(id string) core.ISession {
	thiz.mu.Lock()
	session := thiz.sessions[id]
	thiz.mu.Unlock()

	return session
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
