package auth

import (
	"database/sql/driver"
	"encoding/json"
	"sync"
	"time"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db/orm"
)

type AttributesMap map[string]string

func (m AttributesMap) Value() (driver.Value, error) {
	if m == nil {
		return []byte("null"), nil
	}
	b, err := json.Marshal(map[string]string(m))
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (m *AttributesMap) Scan(value any) error {
	if value == nil {
		*m = nil
		return nil
	}
	switch v := value.(type) {
	case []byte:
		var tmp map[string]string
		if err := json.Unmarshal(v, &tmp); err != nil {
			return err
		}
		*m = tmp
		return nil
	case string:
		var tmp map[string]string
		if err := json.Unmarshal([]byte(v), &tmp); err != nil {
			return err
		}
		*m = tmp
		return nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		var tmp map[string]string
		if err := json.Unmarshal(b, &tmp); err != nil {
			return err
		}
		*m = tmp
		return nil
	}
}

type DbSessionEntity struct {
	orm.BaseEntity
	ID           string        `gorm:"primaryKey;column:id"`
	UserID       string        `gorm:"column:user_id"`
	Expiry       time.Time     `gorm:"column:expiry"`
	CreatedAt    time.Time     `gorm:"column:created_at"`
	LastActivity time.Time     `gorm:"column:last_activity"`
	Attributes   AttributesMap `gorm:"column:attributes;type:jsonb;serializer:json"`
	mu           sync.Mutex
}

func (s *DbSessionEntity) TableName() string {
	return "sessions"
}

func (s *DbSessionEntity) GetId() string {
	return s.ID
}

func (s *DbSessionEntity) SetId(id string) {
	s.ID = id
}

func (s *DbSessionEntity) GetUserId() string {
	return s.UserID
}

func (s *DbSessionEntity) SetUserId(id string) {
	s.UserID = id
}

func (s *DbSessionEntity) IsExpired() bool {
	return s.Expiry.Before(time.Now())
}

func (s *DbSessionEntity) SetExpiry(t time.Time) {
	s.Expiry = t
}

func (s *DbSessionEntity) GetExpiry() time.Time {
	return s.Expiry
}

func (s *DbSessionEntity) GetCreatedAt() time.Time {
	return s.CreatedAt
}

func (s *DbSessionEntity) GetLastActivity() time.Time {
	return s.LastActivity
}

func (s *DbSessionEntity) SetLastActivity(t time.Time) {
	s.LastActivity = t
}

func (s *DbSessionEntity) SetItem(key string, value string) {
	s.mu.Lock()
	if s.Attributes == nil {
		s.Attributes = make(map[string]string)
	}
	s.Attributes[key] = value
	s.mu.Unlock()
}

func (s *DbSessionEntity) GetItem(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, ok := s.Attributes[key]; ok {
		return value
	}
	return ""
}

func (s *DbSessionEntity) ClearItem(attribute string) {
	s.mu.Lock()
	delete(s.Attributes, attribute)
	s.mu.Unlock()
}

func (s *DbSessionEntity) ClearItems() {
	s.mu.Lock()
	s.Attributes = make(map[string]string)
	s.mu.Unlock()
}

type DbSessionStorage struct {
	sessionLifetime time.Duration
	mediator        *DbSessionMediator `container:"inject"`
}

func NewDbSessionStorage(sessionLifetime time.Duration) *DbSessionStorage {
	storage := &DbSessionStorage{
		sessionLifetime: sessionLifetime,
	}
	return storage
}

func (d *DbSessionStorage) SetSessionLifetime(lifetime time.Duration) {
	d.sessionLifetime = lifetime
}

func (d *DbSessionStorage) GetSessionLifetime() time.Duration {
	return d.sessionLifetime
}

func (d *DbSessionStorage) ClearExpiredSessions() {
	err := d.mediator.ClearExpired()
	if err != nil {
		_ = err
	}
}

func (d *DbSessionStorage) AddSession(session core.ISession) {
	dbSession, ok := session.(*DbSessionEntity)
	if !ok {
		// Create new session with mediator
		_, err := d.mediator.CreateSession(
			session.GetId(),
			session.GetUserId(),
			session.GetExpiry(),
		)
		if err != nil {
			_ = err
		}
		return
	}

	// Session already exists, just update it
	err := d.mediator.UpdateSession(dbSession)
	if err != nil {
		_ = err
	}
}

func (d *DbSessionStorage) DeleteSession(session core.ISession) {
	d.DeleteSessionById(session.GetId())
}

func (d *DbSessionStorage) DeleteSessionById(id string) {
	err := d.mediator.DeleteSession(id)
	if err != nil {
		_ = err
	}
}

func (d *DbSessionStorage) GetSessionById(id string) core.ISession {
	session, err := d.mediator.GetSession(id)
	if err != nil {
		return nil
	}

	if session == nil {
		return nil
	}

	if session.IsExpired() {
		d.DeleteSessionById(id)
		return nil
	}

	// Return session with mediator for automatic persistence
	return NewDbSessionEntityWithMediator(session, d.mediator)
}
