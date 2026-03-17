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

func (m *MockSessionRepository) DeleteById(id string) error {
	args := m.Called(id)
	return args.Error(0)
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

	result := storage.GetSessionById("test-session-1")
	assert.NotNil(t, result)
	assert.Equal(t, "test-session-1", result.GetId())
	assert.Equal(t, "user-1", result.GetUserId())
	assert.False(t, result.IsExpired())

	// Test case 2: Session not found
	mockRepo.On("FindById", "non-existent").Return(nil, assert.AnError)

	result = storage.GetSessionById("non-existent")
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
	mockRepo.On("DeleteById", "expired-session").Return(nil)

	result = storage.GetSessionById("expired-session")
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

	mockRepo.On("FindById", "test-session").Return(nil, nil)
	mockRepo.On("Save", dbSession).Return(nil)

	storage.AddSession(wrappedSession)
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
	mockRepo.On("Save", existingSession).Return(nil)

	storage.AddSession(wrappedExistingSession)
	mockRepo.AssertExpectations(t)
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
