package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockSessionRepository for testing
type MockSessionRepository struct {
	mock.Mock
}

func (m *MockSessionRepository) FindById(id string) (*DbSessionEntity, error) {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*DbSessionEntity), args.Error(1)
}

func (m *MockSessionRepository) Save(session *DbSessionEntity) error {
	args := m.Called(session)
	return args.Error(0)
}

func (m *MockSessionRepository) Delete(session *DbSessionEntity) error {
	args := m.Called(session)
	return args.Error(0)
}

func (m *MockSessionRepository) DeleteById(id string) (bool, error) {
	args := m.Called(id)
	return args.Bool(0), args.Error(1)
}

func (m *MockSessionRepository) DeleteExpired() error {
	args := m.Called()
	return args.Error(0)
}

func TestDbSessionStorage_GetSessionById(t *testing.T) {
	// Create mock repository
	mockRepo := &MockSessionRepository{}

	// Create storage with mock repository
	mediator := NewDbSessionMediator(mockRepo)
	storage := &DbSessionStorage{
		sessionLifetime: 30 * time.Minute,
		mediator:        mediator,
	}

	// Test case 1: Session found and not expired
	now := time.Now()
	expiry := now.Add(1 * time.Hour)
	session := &DbSessionEntity{
		ID:           "test-session-1",
		UserID:       "user-1",
		Expiry:       expiry,
		CreatedAt:    now,
		LastActivity: now,
		Attributes:   make(map[string]string),
	}

	mockRepo.On("FindById", "test-session-1").Return(session, nil)

	result, err := storage.GetSessionById("test-session-1")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test-session-1", result.GetId())
	assert.Equal(t, "user-1", result.GetUserId())
	assert.False(t, result.IsExpired())

	// Test case 2: the lookup itself failed. That is reported rather than answered with
	// "there is no such session": a store that cannot be reached and a visitor with no
	// cookie look identical from a single nil, and only one of them is normal.
	mockRepo.On("FindById", "non-existent").Return(nil, assert.AnError)

	result, err = storage.GetSessionById("non-existent")
	assert.ErrorIs(t, err, assert.AnError)
	assert.Nil(t, result)

	// Test case 3: Session expired
	expiredSession := &DbSessionEntity{
		ID:           "expired-session",
		UserID:       "user-2",
		Expiry:       now.Add(-1 * time.Hour), // Expired
		CreatedAt:    now.Add(-2 * time.Hour),
		LastActivity: now.Add(-1 * time.Hour),
		Attributes:   make(map[string]string),
	}

	mockRepo.On("FindById", "expired-session").Return(expiredSession, nil)
	mockRepo.On("DeleteById", "expired-session").Return(true, nil)

	result, err = storage.GetSessionById("expired-session")
	assert.NoError(t, err)
	assert.Nil(t, result)

	mockRepo.AssertExpectations(t)
}

func TestDbSessionStorage_AddSession(t *testing.T) {
	// Create mock repository
	mockRepo := &MockSessionRepository{}

	// Create storage with mock repository
	mediator := NewDbSessionMediator(mockRepo)
	storage := &DbSessionStorage{
		sessionLifetime: 30 * time.Minute,
		mediator:        mediator,
	}

	// Test case 1: Add a new mediated session
	now := time.Now()
	dbSession := &DbSessionEntity{
		ID:           "test-session",
		UserID:       "user-1",
		Expiry:       now.Add(1 * time.Hour),
		CreatedAt:    now,
		LastActivity: now,
		Attributes:   make(map[string]string),
	}
	wrappedSession := NewDbSessionEntityWithMediator(dbSession, mediator)

	// The repository receives a detached copy of the session, never the live pointer — see
	// DbSessionEntity.Snapshot — so the expectation is on the type, and what was persisted
	// is checked afterwards.
	var created *DbSessionEntity
	mockRepo.On("FindById", "test-session").Return(nil, nil)
	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).
		Run(func(args mock.Arguments) { created = args.Get(0).(*DbSessionEntity) }).
		Return(nil).Once()

	assert.NoError(t, storage.AddSession(wrappedSession))
	assert.NotSame(t, dbSession, created)
	assert.Equal(t, "test-session", created.GetId())
	mockRepo.AssertExpectations(t)

	// Test case 2: Update an existing mediated session
	existingSession := &DbSessionEntity{
		ID:           "regular-session",
		UserID:       "user-2",
		Expiry:       now.Add(1 * time.Hour),
		CreatedAt:    now,
		LastActivity: now,
		Attributes:   make(map[string]string),
	}
	wrappedExistingSession := NewDbSessionEntityWithMediator(existingSession, mediator)

	mockRepo.On("FindById", "regular-session").Return(existingSession, nil)
	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).Return(nil)

	assert.NoError(t, storage.AddSession(wrappedExistingSession))
	mockRepo.AssertExpectations(t)
}

// TestDbSessionStorage_AddSessionRefusesARevokedSession. A session that was loaded from the
// table and whose row has since gone was revoked by something, and writing it back would undo
// that — a durable row carrying whatever user id the stale object still holds. Reachable from
// a request holding a session across a concurrent logout, and from the CSRF token endpoint,
// which is registered by default and needs no authentication.
func TestDbSessionStorage_AddSessionRefusesARevokedSession(t *testing.T) {
	mockRepo := &MockSessionRepository{}
	mediator := NewDbSessionMediator(mockRepo)
	storage := &DbSessionStorage{sessionLifetime: 30 * time.Minute, mediator: mediator}

	now := time.Now()
	revoked := &DbSessionEntity{
		ID:           "revoked-session",
		UserID:       "user-9",
		Expiry:       now.Add(time.Hour),
		CreatedAt:    now,
		LastActivity: now,
		Attributes:   make(map[string]string),
	}
	revoked.Meta.IsLoaded = true

	mockRepo.On("FindById", "revoked-session").Return(nil, nil)

	err := storage.AddSession(NewDbSessionEntityWithMediator(revoked, mediator))

	assert.Error(t, err, "a revoked session must not be written back")
	mockRepo.AssertNotCalled(t, "Save", mock.Anything)
}

func TestDbSessionEntity_ISimpleStorage(t *testing.T) {
	// Create a DbSessionEntity
	session := &DbSessionEntity{
		ID:           "test-session",
		UserID:       "user-1",
		Expiry:       time.Now().Add(1 * time.Hour),
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Attributes:   make(map[string]string),
	}

	// Test SetItem and GetItem
	session.SetItem("key1", "value1")
	session.SetItem("key2", "value2")

	assert.Equal(t, "value1", session.GetItem("key1"))
	assert.Equal(t, "value2", session.GetItem("key2"))
	assert.Equal(t, "", session.GetItem("non-existent"))

	// Test ClearItem
	session.ClearItem("key1")
	assert.Equal(t, "", session.GetItem("key1"))
	assert.Equal(t, "value2", session.GetItem("key2"))

	// Test ClearItems
	session.ClearItems()
	assert.Equal(t, "", session.GetItem("key2"))
}

func TestSessionFactory(t *testing.T) {
	// Test MemorySessionFactory
	memoryFactory := NewMemorySessionFactory()

	now := time.Now()
	expiry := now.Add(1 * time.Hour)

	session := memoryFactory.CreateSession("test-id", expiry)
	assert.NotNil(t, session)
	assert.Equal(t, "test-id", session.GetId())
	assert.Equal(t, expiry, session.GetExpiry())
	assert.IsType(t, &Session{}, session)

	// Test DbSessionFactory
	dbFactory := NewDbSessionFactory()

	dbSession := dbFactory.CreateSession("test-id", expiry)
	assert.NotNil(t, dbSession)
	assert.Equal(t, "test-id", dbSession.GetId())
	assert.Equal(t, expiry, dbSession.GetExpiry())
	assert.IsType(t, &DbSessionEntityWithMediator{}, dbSession)

	// Test CreateSessionWithUser
	userSession := dbFactory.CreateSessionWithUser("test-id", "user-1", expiry)
	assert.NotNil(t, userSession)
	assert.Equal(t, "test-id", userSession.GetId())
	assert.Equal(t, "user-1", userSession.GetUserId())
	assert.Equal(t, expiry, userSession.GetExpiry())
}
