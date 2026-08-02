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
//
// It cannot take a lock — the receiver is the map, not the session that owns it — and
// that is why DbSessionEntity is never handed to the ORM directly. The driver calls this
// deep inside the database round trip, on whatever map value the entity carried, while
// the request that owns the session may be writing the same buckets under the session's
// mutex. A Go map does not merely tear under that: `json.Marshal` iterates it, the
// runtime notices the concurrent write and raises `fatal error: concurrent map
// iteration and map write`, which is not a panic and so RecoveryMiddleware cannot turn
// it into a 500 — the process dies and takes every in-flight request with it.
//
// The fix is upstream of here, in DbSessionMediator: it persists a detached snapshot
// whose Attributes map is copied, so the map this method walks is reachable from one
// goroutine only. Anything else that persists a session must do the same.
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

// Every accessor below takes mu, and the policy is deliberately uniform: mu guards every
// column of this struct, not the subset somebody once saw a race on.
//
// DbSessionMediator caches *DbSessionEntity by id and hands the same pointer to every
// request for that session, so two concurrent requests read and write the same fields —
// and the sweep reads them through IsExpired while holding the *mediator's* mutex, not
// this one. Expiry was locked first, because a time.Time is three words and an unguarded
// read could see a value that never existed. The rest was left unlocked, which made the
// asymmetry worse than the original gap: GetLastActivity read a field SetLastActivity
// wrote under the lock, and SessionMiddleware writes it while ShouldRotateSession reads
// it on literally every request, so a torn timestamp decided whether to rotate. UserID is
// the field authorization is derived from, and the login handler overwrites it on a
// session other requests are already authorizing against.
//
// Taking s.mu inside the mediator's critical section is safe: nothing acquires them in the
// opposite order.
func (s *DbSessionEntity) GetId() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ID
}

func (s *DbSessionEntity) SetId(id string) {
	s.mu.Lock()
	s.ID = id
	s.mu.Unlock()
}

func (s *DbSessionEntity) GetUserId() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.UserID
}

func (s *DbSessionEntity) SetUserId(id string) {
	s.mu.Lock()
	s.UserID = id
	s.mu.Unlock()
}

func (s *DbSessionEntity) IsExpired() bool {
	return s.GetExpiry().Before(time.Now())
}

func (s *DbSessionEntity) SetExpiry(t time.Time) {
	s.mu.Lock()
	s.Expiry = t
	s.mu.Unlock()
}

func (s *DbSessionEntity) GetExpiry() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Expiry
}

func (s *DbSessionEntity) GetCreatedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.CreatedAt
}

func (s *DbSessionEntity) GetLastActivity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LastActivity
}

func (s *DbSessionEntity) SetLastActivity(t time.Time) {
	s.mu.Lock()
	s.LastActivity = t
	s.mu.Unlock()
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

// Snapshot returns a copy of this session that no other goroutine can reach, for
// handing to the ORM.
//
// Persisting the live entity is not safe and locking the accessors does not make it
// safe. The ORM copies the whole struct through reflect (`val.Interface()` in
// db/orm/fields.go, which the race detector confirms against a concurrent SetExpiry) and
// then hands the *same* Attributes map to the driver, which marshals it inside the round
// trip holding no session lock at all. Both reads happen outside anything mu can guard,
// so the only way to make them safe is to give the ORM a value nobody else owns. The
// attributes are copied rather than shared for exactly that reason.
//
// Meta is rebuilt rather than shared for the same reason: it carries maps the ORM writes
// to, and two concurrent saves of one session would otherwise write the same ones.
// AdoptPersistedMeta copies back the part of the result the live entity needs.
func (s *DbSessionEntity) Snapshot() *DbSessionEntity {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := &DbSessionEntity{
		ID:           s.ID,
		UserID:       s.UserID,
		Expiry:       s.Expiry,
		CreatedAt:    s.CreatedAt,
		LastActivity: s.LastActivity,
	}

	if s.Attributes != nil {
		attributes := make(AttributesMap, len(s.Attributes))
		for key, value := range s.Attributes {
			attributes[key] = value
		}
		snapshot.Attributes = attributes
	}

	loadedColumns := make(map[string]bool, len(s.Meta.LoadedColumns))
	for column, loaded := range s.Meta.LoadedColumns {
		loadedColumns[column] = loaded
	}

	snapshot.Meta = orm.EntityMeta{
		TableName:     s.Meta.TableName,
		PrimaryKey:    s.Meta.PrimaryKey,
		DatabaseName:  s.Meta.DatabaseName,
		IsLoaded:      s.Meta.IsLoaded,
		IsDirty:       s.Meta.IsDirty,
		LoadedColumns: loadedColumns,
		RelationMeta:  make(map[string]*orm.RelationMeta),
		DataSource:    s.Meta.DataSource,
	}

	return snapshot
}

// AdoptPersistedMeta copies back what a save learned, so the next save of this session
// knows whether its row exists.
//
// Without it the snapshot would carry IsLoaded away with it and every save would insert.
// The query result is copied too: it is how a caller tells a write that matched a row
// from one that had nowhere to land.
func (s *DbSessionEntity) AdoptPersistedMeta(snapshot *DbSessionEntity) {
	if snapshot == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.Meta.TableName = snapshot.Meta.TableName
	s.Meta.PrimaryKey = snapshot.Meta.PrimaryKey
	s.Meta.DatabaseName = snapshot.Meta.DatabaseName
	s.Meta.IsLoaded = snapshot.Meta.IsLoaded
	s.Meta.IsDirty = snapshot.Meta.IsDirty
	s.Meta.LoadedColumns = snapshot.Meta.LoadedColumns
	s.Meta.DataSource = snapshot.Meta.DataSource
	s.Meta.QueryResult = snapshot.Meta.QueryResult
}

// IsPersisted reports whether this session has a row behind it.
//
// AddSession consults it so a session that was loaded from the table and has since been
// deleted is not written back as a new row.
func (s *DbSessionEntity) IsPersisted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Meta.IsLoaded
}

// GetMeta hands out a pointer into the struct, so it cannot be guarded and callers must
// not race on it. In practice only Snapshot, AdoptPersistedMeta and the ORM — which sees
// snapshots and nothing else — reach for it.
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

// NewDbSessionStorageWithRepository builds a storage over a repository given directly,
// rather than over the mediator the container injects.
//
// The container-wired constructor above leaves the mediator to be injected, which means
// nothing outside the container can build a working storage — including a test that wants
// to prove a revoked session stops authenticating, or an app that keeps sessions in a
// table its own repository owns. Rotation and activity bounds come from configuration
// here as well, so a caller gets the same storage the container would have produced.
func NewDbSessionStorageWithRepository(
	sessionLifetime time.Duration, repository ISessionRepository) *DbSessionStorage {
	storage := NewDbSessionStorage(sessionLifetime)
	storage.mediator = NewDbSessionMediator(repository)
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

// ClearExpiredSessions deletes every expired row.
//
// The error used to be assigned and then discarded with `_ = err`, alone in this file —
// AddSession, DeleteSession and the rest all report through grgErr.HandleError. So a sweep
// that failed every time (a revoked GRANT, a lock timeout, a dropped table) was
// indistinguishable from one that worked, and the only visible symptom was the table growing
// — which is the symptom of the sweep not running at all. H4 gives this a caller, both a
// scheduled job and the `session:gc` command, and neither can report a failure it never
// sees. It is now returned rather than logged here, so those two callers can say the sweep
// failed instead of exiting 0.
func (d *DbSessionStorage) ClearExpiredSessions() error {
	if err := d.mediator.ClearExpired(); err != nil {
		return fmt.Errorf("could not clear expired sessions: %w", err)
	}
	return nil
}

// AddSession inserts a session the store does not have, or persists the current state of
// one it does.
//
// It refuses to insert a session that was loaded from the table and whose row has since
// gone. That combination means exactly one thing — something revoked it — and writing it
// back would undo the revocation and mint a durable row carrying whatever user id the
// stale object still holds. Two callers could reach that: a request holding a session
// object across a concurrent logout, and the CSRF token endpoint, which used to upsert
// unconditionally and is registered by default on a route needing no authentication.
// Neither has any legitimate reason to recreate a revoked session, and revoking on
// another replica leaves no local tombstone to catch it, so the check is on the row.
func (d *DbSessionStorage) AddSession(session core.ISession) error {
	dbSession, okMed := session.(*DbSessionEntityWithMediator)
	if !okMed {
		return fmt.Errorf("session %T is not a DbSessionEntityWithMediator", session)
	}

	existedSession, err := d.GetSessionById(dbSession.GetId())
	if err != nil {
		return fmt.Errorf("could not check whether session %s is stored: %w", dbSession.GetId(), err)
	}

	if existedSession == nil {
		if dbSession.IsPersisted() {
			return fmt.Errorf(
				"session %s is no longer stored and will not be recreated", dbSession.GetId())
		}

		if _, err := d.mediator.CreateSession(dbSession.DbSessionEntity); err != nil {
			return fmt.Errorf("could not create session %s: %w", dbSession.GetId(), err)
		}
		return nil
	}

	if err := d.mediator.UpdateSession(dbSession.DbSessionEntity); err != nil {
		return fmt.Errorf("could not update session %s: %w", dbSession.GetId(), err)
	}
	return nil
}

func (d *DbSessionStorage) DeleteSession(session core.ISession) error {
	return d.DeleteSessionById(session.GetId())
}

// DeleteSessionById revokes a session. The error used to be assigned and thrown away with
// `if err != nil { _ = err }`, which is what made logout fail open: the caller expired the
// browser's cookie and reported success while the row was still there.
func (d *DbSessionStorage) DeleteSessionById(id string) error {
	if _, err := d.mediator.DeleteSession(id); err != nil {
		return fmt.Errorf("could not delete session %s: %w", id, err)
	}
	return nil
}

// RevokeSession implements core.ISessionRevoker.
func (d *DbSessionStorage) RevokeSession(id string) (bool, error) {
	deleted, err := d.mediator.DeleteSession(id)
	if err != nil {
		return false, fmt.Errorf("could not delete session %s: %w", id, err)
	}
	return deleted, nil
}

func (d *DbSessionStorage) GetSessionById(id string) (core.ISession, error) {
	session, err := d.mediator.GetSession(id)
	if err != nil {
		return nil, err
	}

	if session == nil {
		return nil, nil
	}

	if session.IsExpired() {
		if err := d.DeleteSessionById(id); err != nil {
			// The session is expired either way, so the caller is told there is none;
			// the row staying behind is the sweep's problem, not this request's.
			grgErr.HandleError(err)
		}
		return nil, nil
	}

	// Return session with mediator for automatic persistence
	return NewDbSessionEntityWithMediator(session, d.mediator), nil
}

var (
	_ core.ISessionStorage = (*DbSessionStorage)(nil)
	_ core.ISessionRevoker = (*DbSessionStorage)(nil)
)
