package auth

import (
	"sync"
	"time"
)

// DbSessionMediator acts as an intermediary between session entities and the database
// It ensures that all session changes are automatically persisted
type DbSessionMediator struct {
	sessionRepo ISessionRepository
	mu          sync.RWMutex
	cache       map[string]*DbSessionEntity
}

func NewDbSessionMediator(sessionRepo ISessionRepository) *DbSessionMediator {
	return &DbSessionMediator{
		sessionRepo: sessionRepo,
		cache:       make(map[string]*DbSessionEntity),
	}
}

// GetSession retrieves a session from cache or database
func (m *DbSessionMediator) GetSession(id string) (*DbSessionEntity, error) {
	m.mu.RLock()
	if session, exists := m.cache[id]; exists {
		m.mu.RUnlock()
		return session, nil
	}
	m.mu.RUnlock()

	// Not in cache, load from database
	session, err := m.sessionRepo.FindById(id)
	if err != nil {
		return nil, err
	}

	if session != nil {
		m.mu.Lock()
		m.cache[id] = session
		m.mu.Unlock()
	}

	return session, nil
}

// CreateSession creates a new session and persists it
func (m *DbSessionMediator) CreateSession(session *DbSessionEntity) (*DbSessionEntity, error) {
	err := m.sessionRepo.Save(session)
	if err != nil {
		return nil, err
	}

	// Add to cache
	m.mu.Lock()
	m.cache[session.GetId()] = session
	m.mu.Unlock()

	return session, nil
}

// UpdateSession persists session changes to database
func (m *DbSessionMediator) UpdateSession(session *DbSessionEntity) error {
	err := m.sessionRepo.Save(session)
	if err != nil {
		return err
	}

	// Update cache
	m.mu.Lock()
	m.cache[session.GetId()] = session
	m.mu.Unlock()

	return nil
}

// DeleteSession removes session from database and cache
func (m *DbSessionMediator) DeleteSession(id string) error {
	err := m.sessionRepo.DeleteById(id)
	if err != nil {
		return err
	}

	// Remove from cache
	m.mu.Lock()
	delete(m.cache, id)
	m.mu.Unlock()

	return nil
}

// ClearExpired removes expired sessions from database and cache
func (m *DbSessionMediator) ClearExpired() error {
	err := m.sessionRepo.DeleteExpired()
	if err != nil {
		return err
	}

	// Clear expired sessions from cache
	m.mu.Lock()
	for id, session := range m.cache {
		if session.IsExpired() {
			delete(m.cache, id)
		}
	}
	m.mu.Unlock()

	return nil
}

// ClearCache removes all sessions from cache (useful for testing or memory management)
func (m *DbSessionMediator) ClearCache() {
	m.mu.Lock()
	m.cache = make(map[string]*DbSessionEntity)
	m.mu.Unlock()
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

// Override methods to ensure automatic persistence

func (s *DbSessionEntityWithMediator) SetUserId(id string) {
	s.DbSessionEntity.SetUserId(id)
	s.mediator.UpdateSession(s.DbSessionEntity)
}

func (s *DbSessionEntityWithMediator) SetLastActivity(t time.Time) {
	s.DbSessionEntity.SetLastActivity(t)
	s.mediator.UpdateSession(s.DbSessionEntity)
}

func (s *DbSessionEntityWithMediator) SetItem(key string, value string) {
	s.DbSessionEntity.SetItem(key, value)
	s.mediator.UpdateSession(s.DbSessionEntity)
}

func (s *DbSessionEntityWithMediator) ClearItem(attribute string) {
	s.DbSessionEntity.ClearItem(attribute)
	s.mediator.UpdateSession(s.DbSessionEntity)
}

func (s *DbSessionEntityWithMediator) ClearItems() {
	s.DbSessionEntity.ClearItems()
	s.mediator.UpdateSession(s.DbSessionEntity)
}

func (s *DbSessionEntityWithMediator) SetExpiry(t time.Time) {
	s.DbSessionEntity.SetExpiry(t)
	s.mediator.UpdateSession(s.DbSessionEntity)
}
