package auth

import (
	"time"

	"github.com/osbits/gorgany/app/core"
)

// ISessionFactory defines the interface for creating sessions
type ISessionFactory interface {
	// CreateSession creates a new session with the given ID and expiry
	CreateSession(id string, expiry time.Time) core.ISession
	// CreateSessionWithUser creates a new session with user ID
	CreateSessionWithUser(id string, userId string, expiry time.Time) core.ISession
}

// MemorySessionFactory creates in-memory sessions
type MemorySessionFactory struct{}

func NewMemorySessionFactory() *MemorySessionFactory {
	return &MemorySessionFactory{}
}

func (f *MemorySessionFactory) CreateSession(id string, expiry time.Time) core.ISession {
	return NewSession(id, expiry)
}

func (f *MemorySessionFactory) CreateSessionWithUser(id string, userId string, expiry time.Time) core.ISession {
	session := NewSession(id, expiry)
	session.SetUserId(userId)
	return session
}

// DbSessionFactory creates database-backed sessions
type DbSessionFactory struct {
	mediator *DbSessionMediator
}

func NewDbSessionFactory() *DbSessionFactory {
	return &DbSessionFactory{}
}

func (f *DbSessionFactory) SetMediator(mediator *DbSessionMediator) {
	f.mediator = mediator
}

func (f *DbSessionFactory) CreateSession(id string, expiry time.Time) core.ISession {
	now := time.Now()
	session := &DbSessionEntity{
		ID:           id,
		Expiry:       expiry,
		CreatedAt:    now,
		LastActivity: now,
		Attributes:   make(map[string]string),
	}

	return NewDbSessionEntityWithMediator(session, f.mediator)
}

func (f *DbSessionFactory) CreateSessionWithUser(id string, userId string, expiry time.Time) core.ISession {
	now := time.Now()
	session := &DbSessionEntity{
		ID:           id,
		UserID:       userId,
		Expiry:       expiry,
		CreatedAt:    now,
		LastActivity: now,
		Attributes:   make(map[string]string),
	}

	if f.mediator != nil {
		return NewDbSessionEntityWithMediator(session, f.mediator)
	}
	return session
}
