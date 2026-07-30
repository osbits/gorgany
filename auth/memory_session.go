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

	for key, session := range thiz.sessions {
		if session.IsExpired() {
			delete(thiz.sessions, key)
		}
	}
}

func (thiz *MemorySession) AddSession(session core.ISession) {
	thiz.mu.Lock()
	thiz.sessions[session.GetId()] = session
	thiz.mu.Unlock()
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
