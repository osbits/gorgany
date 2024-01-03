package auth

import (
	"context"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	err2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/internal"
	"sync"
	"time"
)

func GetSessionStorage() core.ISessionStorage {
	return internal.GetFrameworkRegistrar().GetSessionStorage()
}

// concrete session
type Session struct {
	id       string
	expiry   time.Time
	username string
	extra    map[string]string
}

func (thiz *Session) GetId() string {
	return thiz.id
}

// SetItem is used to set extra item in session
func (thiz *Session) SetItem(key string, value string) {
	if thiz.extra == nil {
		thiz.extra = make(map[string]string)
	}
	thiz.extra[key] = value
}

// GetItem is used to get extra item in session
func (thiz *Session) GetItem(key string) string {
	if value, ok := thiz.extra[key]; ok {
		return value
	}
	return ""
}

func (thiz *Session) GetUsername() string {
	return thiz.username
}

func (thiz *Session) SetUsername(username string) {
	thiz.username = username
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

// MemorySession memory-bases session manager
type MemorySession struct {
	sessions map[string]core.ISession
	mu       sync.Mutex
}

func NewMemorySession() *MemorySession {
	return &MemorySession{sessions: make(map[string]core.ISession)}
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

func (thiz *MemorySession) DeleteCurrentSession(ctx context.Context) {
	currentSession, err := thiz.CurrentSession(ctx)
	if err != nil {
		err2.HandleError(err)
		return
	}

	thiz.mu.Lock()
	delete(thiz.sessions, currentSession.GetId())
	thiz.mu.Unlock()
}

func (thiz *MemorySession) GetSessionById(id string) core.ISession {
	return thiz.sessions[id]
}

func (thiz *MemorySession) CurrentSession(ctx context.Context) (core.ISession, error) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Ctx is not core.IMessageContext instance")
	}

	sessionCookie := messageContext.GetCookieManager().GetCookie(core.SessionCookieName)
	if sessionCookie == nil {
		return nil, nil
	}

	session := thiz.GetSessionById(sessionCookie.Value)
	if session == nil {
		return nil, nil
	}

	return session, nil
}

// DbSession, not implemented yet
type DbSession struct {
}

func NewDbSession() *DbSession {
	return &DbSession{}
}

func (thiz *DbSession) NewSession(username string) string {
	return ""
}
