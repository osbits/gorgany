package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// MockSessionRepository for testing
type MockDbSessionRepositoryForMediator struct {
	mock.Mock
}

func (m *MockDbSessionRepositoryForMediator) FindById(id string) (*DbSessionEntity, error) {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*DbSessionEntity), args.Error(1)
}

func (m *MockDbSessionRepositoryForMediator) Save(session *DbSessionEntity) error {
	args := m.Called(session)
	return args.Error(0)
}

func (m *MockDbSessionRepositoryForMediator) Delete(session *DbSessionEntity) error {
	args := m.Called(session)
	return args.Error(0)
}

func (m *MockDbSessionRepositoryForMediator) DeleteById(id string) error {
	args := m.Called(id)
	return args.Error(0)
}

func (m *MockDbSessionRepositoryForMediator) DeleteExpired() error {
	args := m.Called()
	return args.Error(0)
}

func TestDbSessionMediator_GetSession(t *testing.T) {
	mockRepo := &MockDbSessionRepositoryForMediator{}
	mediator := NewDbSessionMediator(mockRepo)

	// Test case 1: Session not in cache, load from database
	session := &DbSessionEntity{
		ID:           "test-session",
		UserID:       "user-1",
		Expiry:       time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Attributes:   make(map[string]string),
	}

	mockRepo.On("FindById", "test-session").Return(session, nil)

	result, err := mediator.GetSession("test-session")
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "test-session", result.GetId())

	// Test case 2: Session in cache, should not call database
	result2, err := mediator.GetSession("test-session")
	assert.NoError(t, err)
	assert.NotNil(t, result2)
	assert.Equal(t, "test-session", result2.GetId())

	// Verify FindById was only called once
	mockRepo.AssertNumberOfCalls(t, "FindById", 1)
}

func TestDbSessionMediator_CreateSession(t *testing.T) {
	mockRepo := &MockDbSessionRepositoryForMediator{}
	mediator := NewDbSessionMediator(mockRepo)

	now := time.Now()
	expiry := now.Add(time.Hour)

	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).Return(nil)

	session, err := mediator.CreateSession("new-session", "user-1", expiry)
	assert.NoError(t, err)
	assert.NotNil(t, session)
	assert.Equal(t, "new-session", session.GetId())
	assert.Equal(t, "user-1", session.GetUserId())
	assert.Equal(t, expiry, session.GetExpiry())

	mockRepo.AssertExpectations(t)
}

func TestDbSessionMediator_UpdateSession(t *testing.T) {
	mockRepo := &MockDbSessionRepositoryForMediator{}
	mediator := NewDbSessionMediator(mockRepo)

	session := &DbSessionEntity{
		ID:           "test-session",
		UserID:       "user-1",
		Expiry:       time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Attributes:   make(map[string]string),
	}

	mockRepo.On("Save", session).Return(nil)

	err := mediator.UpdateSession(session)
	assert.NoError(t, err)

	mockRepo.AssertExpectations(t)
}

func TestDbSessionMediator_DeleteSession(t *testing.T) {
	mockRepo := &MockDbSessionRepositoryForMediator{}
	mediator := NewDbSessionMediator(mockRepo)

	// Add session to cache first
	session := &DbSessionEntity{
		ID:           "test-session",
		UserID:       "user-1",
		Expiry:       time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Attributes:   make(map[string]string),
	}

	mockRepo.On("FindById", "test-session").Return(session, nil)
	mockRepo.On("DeleteById", "test-session").Return(nil)

	// Get session to add to cache
	_, err := mediator.GetSession("test-session")
	assert.NoError(t, err)

	// Delete session
	err = mediator.DeleteSession("test-session")
	assert.NoError(t, err)

	// Verify session is removed from cache by checking cache directly
	mediator.mu.RLock()
	_, exists := mediator.cache["test-session"]
	mediator.mu.RUnlock()
	assert.False(t, exists, "Session should be removed from cache")

	mockRepo.AssertExpectations(t)
}

func TestDbSessionEntityWithMediator_AutomaticPersistence(t *testing.T) {
	mockRepo := &MockDbSessionRepositoryForMediator{}
	mediator := NewDbSessionMediator(mockRepo)

	session := &DbSessionEntity{
		ID:           "test-session",
		UserID:       "user-1",
		Expiry:       time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Attributes:   make(map[string]string),
	}

	wrappedSession := NewDbSessionEntityWithMediator(session, mediator)

	// Test SetUserId triggers persistence
	mockRepo.On("Save", session).Return(nil)
	wrappedSession.SetUserId("user-2")
	assert.Equal(t, "user-2", session.GetUserId())
	mockRepo.AssertExpectations(t)

	// Test SetLastActivity triggers persistence
	mockRepo.On("Save", session).Return(nil)
	newTime := time.Now().Add(time.Minute)
	wrappedSession.SetLastActivity(newTime)
	assert.Equal(t, newTime, session.GetLastActivity())
	mockRepo.AssertExpectations(t)

	// Test SetItem triggers persistence
	mockRepo.On("Save", session).Return(nil)
	wrappedSession.SetItem("key1", "value1")
	assert.Equal(t, "value1", session.GetItem("key1"))
	mockRepo.AssertExpectations(t)

	// Test ClearItem triggers persistence
	mockRepo.On("Save", session).Return(nil)
	wrappedSession.ClearItem("key1")
	assert.Equal(t, "", session.GetItem("key1"))
	mockRepo.AssertExpectations(t)

	// Test ClearItems triggers persistence
	mockRepo.On("Save", session).Return(nil)
	wrappedSession.SetItem("key2", "value2") // Add an item first
	wrappedSession.ClearItems()
	assert.Equal(t, "", session.GetItem("key2"))
	mockRepo.AssertExpectations(t)
}

func TestDbSessionMediator_ClearExpired(t *testing.T) {
	mockRepo := &MockDbSessionRepositoryForMediator{}
	mediator := NewDbSessionMediator(mockRepo)

	// Add expired session to cache
	expiredSession := &DbSessionEntity{
		ID:           "expired-session",
		UserID:       "user-1",
		Expiry:       time.Now().Add(-time.Hour), // Expired
		CreatedAt:    time.Now().Add(-2 * time.Hour),
		LastActivity: time.Now().Add(-time.Hour),
		Attributes:   make(map[string]string),
	}

	// Add valid session to cache
	validSession := &DbSessionEntity{
		ID:           "valid-session",
		UserID:       "user-2",
		Expiry:       time.Now().Add(time.Hour), // Valid
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Attributes:   make(map[string]string),
	}

	// Manually add to cache
	mediator.mu.Lock()
	mediator.cache["expired-session"] = expiredSession
	mediator.cache["valid-session"] = validSession
	mediator.mu.Unlock()

	mockRepo.On("DeleteExpired").Return(nil)

	err := mediator.ClearExpired()
	assert.NoError(t, err)

	// Verify expired session is removed from cache
	mediator.mu.RLock()
	_, expiredExists := mediator.cache["expired-session"]
	_, validExists := mediator.cache["valid-session"]
	mediator.mu.RUnlock()

	assert.False(t, expiredExists, "Expired session should be removed from cache")
	assert.True(t, validExists, "Valid session should remain in cache")

	mockRepo.AssertExpectations(t)
}
