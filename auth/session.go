package auth

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/google/uuid"
	"gorgany/app/core"
	"gorgany/internal"
	"time"
)

func GetSessionStorage() core.ISessionStorage {
	return internal.GetFrameworkRegistrar().GetSessionStorage()
}

func InitSessionClear() {
	go func() {
		for range time.Tick(time.Duration(internal.GetFrameworkRegistrar().GetSessionLifetime()) * time.Second) {
			GetSessionStorage().ClearExpiredSessions()
		}
	}()
}

// concrete session
type Session struct {
	username string
	expiry   time.Time
}

func (thiz Session) isExpired() bool {
	return thiz.expiry.Before(time.Now())
}

// MemorySession saves sessions in memory
type MemorySession struct {
	sessions        map[string]*Session
	sessionLifetime int
}

func NewMemorySession() *MemorySession {
	return &MemorySession{sessions: make(map[string]*Session), sessionLifetime: internal.GetFrameworkRegistrar().GetSessionLifetime()}
}

// NewSession returns generated session token
func (thiz *MemorySession) NewSession(user core.Authenticable) (string, time.Time, error) {
	uid := uuid.NewString()
	now := time.Now()

	rawToken := fmt.Sprintf("%s%s%v", user.GetUsername(), uid, now)
	hashedTokenBytes := md5.Sum([]byte(rawToken))
	hashedToken := hex.EncodeToString(hashedTokenBytes[:])

	_, ok := thiz.sessions[hashedToken]
	if ok {
		return "", time.Time{}, fmt.Errorf("Session %s already exists", hashedToken)
	}

	session := &Session{
		username: user.GetUsername(),
		expiry:   now.Add(time.Second * time.Duration(thiz.sessionLifetime)),
	}
	thiz.sessions[hashedToken] = session

	return hashedToken, session.expiry, nil
}

// IsLoggedIn
// ctx - instance of core.IMessageContext
func (thiz *MemorySession) IsLoggedIn(ctx context.Context) bool {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return false
	}

	sessionToken := messageContext.GetSessionToken()
	session, ok := thiz.sessions[sessionToken]
	if !ok {
		return false
	}

	if session.isExpired() {
		delete(thiz.sessions, sessionToken)
		return false
	}

	return true
}

// Logout
// ctx - instance of core.IMessageContext
func (thiz *MemorySession) Logout(ctx context.Context) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return
	}

	sessionToken := messageContext.GetSessionToken()
	delete(thiz.sessions, sessionToken)
}

func (thiz *MemorySession) ClearExpiredSessions() {
	for key, session := range thiz.sessions {
		if session.isExpired() {
			delete(thiz.sessions, key)
		}
	}
}

// CurrentUser
// ctx - instance of core.IMessageContext
func (thiz *MemorySession) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, fmt.Errorf("Ctx is not core.IMessageContext instance")
	}

	sessionToken := messageContext.GetSessionToken()
	session, ok := thiz.sessions[sessionToken]
	if !ok {
		return nil, nil
	}

	if session.isExpired() {
		return nil, nil
	}

	return GetAuthEntityService().GetByUsername(session.username)
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
