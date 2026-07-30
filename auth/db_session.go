package auth

import (
	"database/sql/driver"
	"encoding/json"
	"github.com/spf13/viper"
	"sync"
	"time"

	"fmt"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db/orm"
	grgErr "github.com/osbits/gorgany/v2/err"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type AttributesMap map[string]string

func (AttributesMap) GormDataType() string {
	return "string"
}

func (AttributesMap) GormDBDataType(db *gorm.DB, _ *schema.Field) string {
	return "text"
}

// Value implements driver.Valuer (send JSON text to DB).
func (m AttributesMap) Value() (driver.Value, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(map[string]string(m))
	if err != nil {
		return nil, err
	}
	return string(b), nil
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
	Meta         orm.EntityMeta `gorm:"-"`
	ID           string         `gorm:"primaryKey;column:id"`
	UserID       string         `gorm:"column:user_id"`
	Expiry       time.Time      `gorm:"column:expiry"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
	LastActivity time.Time      `gorm:"column:last_activity"`
	Attributes   AttributesMap  `gorm:"column:attributes"`
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

func (s *DbSessionEntity) GetMeta() *orm.EntityMeta {
	return &s.Meta
}

func (e *DbSessionEntity) SetMeta(meta *orm.EntityMeta) {
	e.Meta = *meta
}

type DbSessionStorage struct {
	sessionLifetime         time.Duration
	mediator                *DbSessionMediator `container:"inject"`
	sessionRotationInterval time.Duration
	sessionActivityTimeout  time.Duration
}

func NewDbSessionStorage(sessionLifetime time.Duration) *DbSessionStorage {
	rotationInterval := viper.GetDuration("auth.session.rotationInterval")
	if rotationInterval.Minutes() == 0 {
		rotationInterval = 24 * time.Hour
	}

	activityTimeout := viper.GetDuration("auth.session.activityTimeout")
	if activityTimeout.Minutes() == 0 {
		activityTimeout = 30 * time.Minute
	}

	storage := &DbSessionStorage{
		sessionLifetime:         sessionLifetime,
		sessionRotationInterval: rotationInterval,
		sessionActivityTimeout:  activityTimeout,
	}
	return storage
}

func (d *DbSessionStorage) SetSessionLifetime(lifetime time.Duration) {
	d.sessionLifetime = lifetime
}

func (d *DbSessionStorage) GetSessionLifetime() time.Duration {
	return d.sessionLifetime
}

func (thiz *DbSessionStorage) GetSessionRotationInterval() time.Duration {
	return thiz.sessionRotationInterval
}

func (thiz *DbSessionStorage) GetSessionActivityTimeout() time.Duration {
	return thiz.sessionActivityTimeout
}

func (d *DbSessionStorage) ClearExpiredSessions() {
	err := d.mediator.ClearExpired()
	if err != nil {
		_ = err
	}
}

func (d *DbSessionStorage) AddSession(session core.ISession) {
	dbSession, okMed := session.(*DbSessionEntityWithMediator)
	if !okMed {
		grgErr.HandleError(fmt.Errorf("Session is not DbSessionEntityWithMediator"))
		return
	}

	existedSession := d.GetSessionById(dbSession.GetId())
	if existedSession == nil {
		_, err := d.mediator.CreateSession(dbSession.DbSessionEntity)
		if err != nil {
			grgErr.HandleError(fmt.Errorf("Error creating session: %v", err))
		}
		return
	}

	if err := d.mediator.UpdateSession(session.(*DbSessionEntityWithMediator).DbSessionEntity); err != nil {
		grgErr.HandleError(fmt.Errorf("Error updating session: %v", err))
		return
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
