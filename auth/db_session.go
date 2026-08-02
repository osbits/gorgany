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
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
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

// The session table's columns, named once so the dirty-tracking below and the ORM agree.
const (
	SessionColumnID           = "id"
	SessionColumnUserID       = "user_id"
	SessionColumnExpiry       = "expiry"
	SessionColumnCreatedAt    = "created_at"
	SessionColumnLastActivity = "last_activity"
	SessionColumnAttributes   = "attributes"
	SessionColumnVersion      = "version"
)

// sessionStateColumns are the columns an authorization decision can turn on.
//
// The split matters because it decides which writes are guarded. Expiry and last activity
// move forward on their own on every request, from every replica, and are monotonic enough
// that whoever writes last is right — guarding them would make two parallel requests from one
// browser conflict continuously for no benefit. The user id and the attribute bag are the
// opposite: they change because something decided they should, and a write that silently
// reverts one is the defect.
var sessionStateColumns = map[string]bool{
	SessionColumnUserID:     true,
	SessionColumnAttributes: true,
}

// attributeOpKind is what an attribute mutation did, so it can be replayed onto a row that
// moved underneath it.
type attributeOpKind uint8

const (
	attributeOpSet attributeOpKind = iota
	attributeOpClear
	attributeOpClearAll
)

// attributeOp is one recorded mutation of the attribute bag.
//
// Recording operations rather than the resulting map is what makes a conflict recoverable.
// Attributes are a single JSON column, so two replicas writing different keys are writing the
// same column, and the loser cannot merge by comparing snapshots: it cannot tell a key the
// winner added from one it deleted itself. Replaying "set cart" onto the winner's map keeps
// both changes; replaying a whole map would resurrect whatever the winner cleared.
type attributeOp struct {
	kind  attributeOpKind
	key   string
	value string
}

type DbSessionEntity struct {
	Meta         orm.EntityMeta `gorm:"-"`
	ID           string         `gorm:"primaryKey;column:id"`
	UserID       string         `gorm:"column:user_id"`
	Expiry       time.Time      `gorm:"column:expiry"`
	CreatedAt    time.Time      `gorm:"column:created_at"`
	LastActivity time.Time      `gorm:"column:last_activity"`
	Attributes   AttributesMap  `gorm:"column:attributes"`

	// Version is the row's optimistic-concurrency counter. A write that changes security
	// state carries version+1 and matches on the version it read, so a replica writing from
	// a stale copy is refused rather than silently winning.
	Version int64 `gorm:"column:version"`

	// dirty and attrOps are unexported so reflection skips them: db/orm/fields.go ignores any
	// field it cannot Interface(), which is also how mu stays out of the SET list.
	dirty   map[string]bool
	attrOps []attributeOp

	// pendingErr remembers a write-through that did not reach the store, so the next
	// operation that *can* report a failure does. See PendingWriteError.
	pendingErr error

	mu sync.Mutex
}

// notePersistFailure records that a write did not land.
//
// It does not have to track *which* columns failed, and that is a property of the dirty set
// rather than a simplification: a write that fails clears nothing, so its columns stay dirty
// and the next write necessarily carries them again. Clearing on any success is therefore the
// same rule as clearing on a success that covered the failure — see
// TestASuccessfulWriteCarriesTheColumnsAnEarlierFailureLeftBehind, which pins it.
func (s *DbSessionEntity) notePersistFailure(err error) {
	if err == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Named so the diagnostic identifies the session even when the underlying error does
	// not. The value that failed to write is deliberately not included: this ends up in a
	// log, and the attribute being written may be the CSRF token.
	s.pendingErr = fmt.Errorf("session %s has unpersisted changes: %w", s.ID, err)
}

// notePersistSuccess records that the store now has this session's state.
func (s *DbSessionEntity) notePersistSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingErr = nil
}

// PendingWriteError implements core.ISessionWriteStatus.
//
// It reports rather than consumes: the failure stays until a write succeeds. Consuming it
// would mean whichever caller asked first — GenerateCSRFToken or AddSession — swallowed the
// failure on behalf of the other, and which of them that is depends on call order rather than
// on anything a reader could reason about.
func (s *DbSessionEntity) PendingWriteError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingErr
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
	s.markDirtyLocked(SessionColumnUserID)
	s.mu.Unlock()
}

// markDirtyLocked records that this column has to be written. The caller holds s.mu.
func (s *DbSessionEntity) markDirtyLocked(column string) {
	if s.dirty == nil {
		s.dirty = map[string]bool{}
	}
	s.dirty[column] = true
}

// recordAttributeOpLocked appends a mutation to the replay log. The caller holds s.mu.
func (s *DbSessionEntity) recordAttributeOpLocked(op attributeOp) {
	s.markDirtyLocked(SessionColumnAttributes)
	if op.kind == attributeOpClearAll {
		// Everything before it is unreachable: replaying a clear-all and then the earlier
		// sets would put back exactly what the clear-all removed.
		s.attrOps = s.attrOps[:0]
	}
	s.attrOps = append(s.attrOps, op)
}

func (s *DbSessionEntity) IsExpired() bool {
	return s.GetExpiry().Before(time.Now())
}

func (s *DbSessionEntity) SetExpiry(t time.Time) {
	s.mu.Lock()
	s.Expiry = t
	s.markDirtyLocked(SessionColumnExpiry)
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
	s.markDirtyLocked(SessionColumnLastActivity)
	s.mu.Unlock()
}

func (s *DbSessionEntity) SetItem(key string, value string) {
	s.mu.Lock()
	if s.Attributes == nil {
		s.Attributes = make(map[string]string)
	}
	s.Attributes[key] = value
	s.recordAttributeOpLocked(attributeOp{kind: attributeOpSet, key: key, value: value})
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
	s.recordAttributeOpLocked(attributeOp{kind: attributeOpClear, key: attribute})
	s.mu.Unlock()
}

func (s *DbSessionEntity) ClearItems() {
	s.mu.Lock()
	s.Attributes = make(map[string]string)
	s.recordAttributeOpLocked(attributeOp{kind: attributeOpClearAll})
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
// The snapshot also carries the write's shape, and that is the second half of what it is for.
//
// A full-row UPDATE writes every column from the in-memory copy, and the mediator caches one
// entity per session id per *process* — so on a second replica the copy is whatever that
// replica last read. A routine heartbeat there (SessionMiddleware touches expiry, the flash
// marker and last activity on every single request) re-sent the user id and the whole
// attribute bag along with them, putting back an MFA flag, an impersonation flag or an
// identity that another replica had just cleared. Nothing raced and nothing errored; the
// stale writer simply said the last word.
//
// So the snapshot names the columns that actually changed, and when any of them is one an
// authorization decision turns on it also carries a version guard. See sessionStateColumns for
// why the guard is not applied to the heartbeat columns.
func (s *DbSessionEntity) Snapshot() *DbSessionEntity {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := &DbSessionEntity{
		ID:           s.ID,
		UserID:       s.UserID,
		Expiry:       s.Expiry,
		CreatedAt:    s.CreatedAt,
		LastActivity: s.LastActivity,
		Version:      s.Version,
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

	dirty := make(map[string]bool, len(s.dirty)+1)
	guarded := false
	for column := range s.dirty {
		dirty[column] = true
		if sessionStateColumns[column] {
			guarded = true
		}
	}

	if guarded {
		// The SET carries the next version and the WHERE matches the one this entity read,
		// so a writer working from a copy somebody else has already superseded matches no
		// row. It also guarantees the statement changes at least one column, which is what
		// makes zero-rows-affected decidable on MySQL — see orm.updateEntity.
		snapshot.Version = s.Version + 1
		dirty[SessionColumnVersion] = true
		snapshot.Meta.UpdateGuard = []dbCore.Condition{
			&dbCore.BinaryCondition{
				Left:     SessionColumnVersion,
				Operator: "=",
				Right:    s.Version,
			},
		}
	}

	// An empty set means "every column" to the ORM, which is what a create wants and what a
	// caller that asked for a full write through MarkAllDirty gets.
	if len(dirty) > 0 {
		snapshot.Meta.DirtyColumns = dirty
	}

	return snapshot
}

// MarkAllDirty asks for the next write to carry every column.
//
// This is what AddSession does: it means "persist the state I am holding", not "persist the
// field I just touched", and it is the path Login and RotateSession use to confirm a session
// reached the store. Because the user id is among the columns, the write is still guarded.
func (s *DbSessionEntity) MarkAllDirty() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.dirty = map[string]bool{
		SessionColumnUserID:       true,
		SessionColumnExpiry:       true,
		SessionColumnCreatedAt:    true,
		SessionColumnLastActivity: true,
		SessionColumnAttributes:   true,
	}
}

// hasPendingWrite reports whether anything is waiting to be written.
func (s *DbSessionEntity) hasPendingWrite() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dirty) > 0
}

// AdoptPersistedState copies the stored row over this entity's own copy of it.
//
// The row is authoritative and the cached object is not. GetSession used to keep the cached
// user id, attributes and activity stamp and take only a *later* expiry from the row, which
// meant a replica could not see an attribute another replica had cleared, could not see an
// identity it had removed, and could not see an expiry it had shortened — the merge took the
// least restrictive of the two by construction.
//
// Adoption is not a merge of the two values: for any column this process is not itself
// waiting to write, the row simply wins. The one exception is a column that is still dirty —
// a change made here whose write has not landed yet. Taking the row's value for that would
// discard a write the caller was told nothing about, so the local value stands, the column
// stays dirty, and the write is retried against the version the row now holds. Attributes are
// reconciled rather than chosen: the row's bag is the base and this entity's recorded
// mutations are replayed on top, so two replicas touching different keys both keep theirs.
func (s *DbSessionEntity) AdoptPersistedState(row *DbSessionEntity) {
	if row == nil {
		return
	}

	// Read the row's fields through its own accessors before taking this entity's lock: the
	// two are different objects, and reaching into one while holding the other's mutex is how
	// a lock-ordering rule gets broken later.
	rowUserID := row.GetUserId()
	rowExpiry := row.GetExpiry()
	rowCreatedAt := row.GetCreatedAt()
	rowLastActivity := row.GetLastActivity()
	rowVersion := row.GetVersion()
	rowAttributes := row.attributesCopy()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.dirty[SessionColumnUserID] {
		s.UserID = rowUserID
	}
	if !s.dirty[SessionColumnExpiry] {
		s.Expiry = rowExpiry
	}
	if !s.dirty[SessionColumnLastActivity] {
		s.LastActivity = rowLastActivity
	}
	s.CreatedAt = rowCreatedAt
	s.Version = rowVersion
	s.Meta.IsLoaded = true

	// The row's bag is always the base, even when attributes are dirty: the replay below is
	// what carries this process's own changes across, and starting from the local map instead
	// would put back keys the row no longer has.
	s.Attributes = rowAttributes
	s.replayAttributeOpsLocked()
}

// replayAttributeOpsLocked re-applies the recorded mutations on top of s.Attributes.
// The caller holds s.mu.
func (s *DbSessionEntity) replayAttributeOpsLocked() {
	if len(s.attrOps) == 0 {
		return
	}
	if s.Attributes == nil {
		s.Attributes = make(AttributesMap)
	}
	for _, op := range s.attrOps {
		switch op.kind {
		case attributeOpSet:
			s.Attributes[op.key] = op.value
		case attributeOpClear:
			delete(s.Attributes, op.key)
		case attributeOpClearAll:
			s.Attributes = make(AttributesMap)
		}
	}
}

// GetVersion returns the row version this entity believes it holds.
func (s *DbSessionEntity) GetVersion() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Version
}

// attributesCopy returns a copy of the attribute bag that no other goroutine can reach.
func (s *DbSessionEntity) attributesCopy() AttributesMap {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Attributes == nil {
		return nil
	}
	attributes := make(AttributesMap, len(s.Attributes))
	for key, value := range s.Attributes {
		attributes[key] = value
	}
	return attributes
}

// AdoptPersistedMeta copies back what a save learned, so the next save of this session
// knows whether its row exists.
//
// Without it the snapshot would carry IsLoaded away with it and every save would insert.
// The query result is copied too: it is how a caller tells a write that matched a row
// from one that had nowhere to land.
//
// It also settles the write: the columns the snapshot carried are no longer dirty, the
// attribute replay log for them is spent, and the version the row now holds is the one the
// snapshot wrote. Clearing only the columns that were written is deliberate — a heartbeat
// that succeeds must not be taken as evidence that an earlier attribute write landed.
func (s *DbSessionEntity) AdoptPersistedMeta(snapshot *DbSessionEntity) {
	if snapshot == nil {
		return
	}

	written := snapshot.Meta.DirtyColumns

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

	s.Version = snapshot.Version

	if len(written) == 0 {
		// A create, or a full-row write with no dirty set: everything reached the row.
		s.dirty = nil
		s.attrOps = nil
		return
	}

	for column := range written {
		delete(s.dirty, column)
	}
	if written[SessionColumnAttributes] {
		s.attrOps = nil
	}
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

	// "Persist the state I am holding", not "persist the field I just touched": AddSession is
	// how Login and RotateSession confirm a whole session reached the store, and it is the
	// path that repairs a session whose earlier write-through failed. Asking for every column
	// keeps that repair complete rather than limited to whatever is still marked dirty.
	dbSession.MarkAllDirty()

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

// RevocationPending implements core.ISessionRevocationStatus.
func (d *DbSessionStorage) RevocationPending(id string) bool {
	return d.mediator.RevocationPending(id)
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
	_ core.ISessionStorage          = (*DbSessionStorage)(nil)
	_ core.ISessionRevoker          = (*DbSessionStorage)(nil)
	_ core.ISessionRevocationStatus = (*DbSessionStorage)(nil)
	_ core.ISessionWriteStatus      = (*DbSessionEntity)(nil)
)
