package auth

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/google/uuid"
	"time"
)

// session storage is a place where we keep all sessions
var sessionStorage ISessionStorage
var sessionLifetime int

func GetSessionStorage() ISessionStorage {
	return sessionStorage
}

func SetSessionStorage(storage ISessionStorage, sessionLife int) {
	sessionLifetime = sessionLife
	sessionStorage = storage
	initSessionClear()
}

func initSessionClear() {
	go func() {
		for range time.Tick(time.Duration(sessionLifetime) * time.Second) {
			GetSessionStorage().ClearExpiredSessions()
		}
	}()
}

// interfaces
type Authenticable interface {
	GetUsername() string
	GetPassword() string
}

type ISessionStorage interface {
	NewSession(user Authenticable) (string, time.Time, error)
	IsLoggedIn(sessionToken string) bool
	Logout(sessionToken string)
	CurrentUser()
	ClearExpiredSessions()
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
	sessions map[string]*Session
}

func NewMemorySession() *MemorySession {
	return &MemorySession{sessions: make(map[string]*Session)}
}

// NewSession returns generated session token
func (thiz *MemorySession) NewSession(user Authenticable) (string, time.Time, error) {
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
		expiry:   now.Add(time.Second * time.Duration(sessionLifetime)),
	}
	thiz.sessions[hashedToken] = session

	return hashedToken, session.expiry, nil
}

func (thiz *MemorySession) IsLoggedIn(sessionToken string) bool {
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

func (thiz *MemorySession) Logout(sessionToken string) {
	delete(thiz.sessions, sessionToken)
}

func (thiz *MemorySession) ClearExpiredSessions() {
	for key, session := range thiz.sessions {
		if session.isExpired() {
			delete(thiz.sessions, key)
		}
	}
}

func (thiz *MemorySession) CurrentUser(ctx context.Context) Authenticable {
	//message := ctx.Value("httpMessage").(*http.Message)
	//cookie, err := message.GetCookie(http.SessionCookieName)
	//if err != nil {
	//	return nil
	//}
	//sessionToken := cookie.Value
	//session, ok := thiz.sessions[sessionToken]
	//if !ok {
	//	return nil
	//}
	//
	//if session.isExpired() {
	//	return nil
	//}
	//
	//session.username
	panic("implement me")
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
