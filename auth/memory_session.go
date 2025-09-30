package auth

import (
	"github.com/spf13/viper"
	"sync"
	"time"

	"github.com/gorganyio/gorgany/app/core"
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

func (thiz *Session) IsExpired() bool {
	return thiz.expiry.Before(time.Now())
}

func (thiz *Session) SetExpiry(t time.Time) {
	thiz.expiry = t
}

func (thiz *Session) GetExpiry() time.Time {
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

func (thiz *MemorySession) ClearExpiredSessions() {
	for key, session := range thiz.sessions {
		if session.IsExpired() {
			thiz.mu.Lock()
			delete(thiz.sessions, key)
			thiz.mu.Unlock()
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
