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

func (m *MockDbSessionRepositoryForMediator) DeleteById(id string) (bool, error) {
	args := m.Called(id)
	return args.Bool(0), args.Error(1)
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

	// Test case 2: a second lookup consults the row again, and hands back the same object.
	//
	// This assertion used to be the other way round — FindById called exactly once, the
	// second lookup served from the cache without touching the database. That is the
	// property that made revocation local to one process: a session another replica had
	// deleted stayed cached here and kept authenticating, with no race and no error
	// involved. The cache still exists, and still guarantees one object per session id so
	// that the middleware and the request's session scope see each other's writes, but it
	// no longer decides whether the session exists.
	result2, err := mediator.GetSession("test-session")
	assert.NoError(t, err)
	assert.NotNil(t, result2)
	assert.Equal(t, "test-session", result2.GetId())
	assert.Same(t, result, result2, "one session object per id, per process")

	mockRepo.AssertNumberOfCalls(t, "FindById", 2)
}

// TestDbSessionMediator_GetSessionStopsWhenTheRowIsGone is the other half of the same
// change: the row, not the cache, decides.
func TestDbSessionMediator_GetSessionStopsWhenTheRowIsGone(t *testing.T) {
	mockRepo := &MockDbSessionRepositoryForMediator{}
	mediator := NewDbSessionMediator(mockRepo)

	session := &DbSessionEntity{
		ID:           "test-session",
		Expiry:       time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		Attributes:   make(map[string]string),
	}

	call := mockRepo.On("FindById", "test-session").Return(session, nil)

	cached, err := mediator.GetSession("test-session")
	assert.NoError(t, err)
	assert.NotNil(t, cached)

	// Somebody else — another replica, the sweep, a DBA — removed the row.
	call.Return(nil, nil)

	gone, err := mediator.GetSession("test-session")
	assert.NoError(t, err)
	assert.Nil(t, gone, "a cached session whose row is gone must stop resolving")

	mediator.mu.RLock()
	_, stillCached := mediator.cache["test-session"]
	mediator.mu.RUnlock()
	assert.False(t, stillCached, "and it must not be left in the cache")
}

func TestDbSessionMediator_CreateSession(t *testing.T) {
	mockRepo := &MockDbSessionRepositoryForMediator{}
	mediator := NewDbSessionMediator(mockRepo)

	now := time.Now()
	expiry := now.Add(time.Hour)
	session := &DbSessionEntity{
		ID:           "new-session",
		UserID:       "user-1",
		Expiry:       expiry,
		CreatedAt:    now,
		LastActivity: now,
		Attributes:   make(map[string]string),
	}

	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).Return(nil)

	session, err := mediator.CreateSession(session)
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

	// The repository is handed a detached copy, never this pointer: the ORM copies the whole
	// struct through reflect and the driver marshals the attribute map inside the round trip,
	// both without any session lock. See DbSessionEntity.Snapshot.
	var persisted *DbSessionEntity
	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).
		Run(func(args mock.Arguments) { persisted = args.Get(0).(*DbSessionEntity) }).
		Return(nil)

	err := mediator.UpdateSession(session)
	assert.NoError(t, err)

	assert.NotSame(t, session, persisted, "the live session must not reach the repository")
	assert.Equal(t, "test-session", persisted.GetId())
	assert.Equal(t, "user-1", persisted.GetUserId())

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
	mockRepo.On("DeleteById", "test-session").Return(true, nil)

	// Get session to add to cache
	_, err := mediator.GetSession("test-session")
	assert.NoError(t, err)

	// Delete session
	deleted, err := mediator.DeleteSession("test-session")
	assert.NoError(t, err)
	assert.True(t, deleted, "the store held the session, so revoking it removed something")

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
	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).Return(nil)
	wrappedSession.SetUserId("user-2")
	assert.Equal(t, "user-2", session.GetUserId())
	mockRepo.AssertExpectations(t)

	// Test SetLastActivity triggers persistence
	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).Return(nil)
	newTime := time.Now().Add(time.Minute)
	wrappedSession.SetLastActivity(newTime)
	assert.Equal(t, newTime, session.GetLastActivity())
	mockRepo.AssertExpectations(t)

	// Test SetItem triggers persistence
	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).Return(nil)
	wrappedSession.SetItem("key1", "value1")
	assert.Equal(t, "value1", session.GetItem("key1"))
	mockRepo.AssertExpectations(t)

	// Test ClearItem triggers persistence
	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).Return(nil)
	wrappedSession.ClearItem("key1")
	assert.Equal(t, "", session.GetItem("key1"))
	mockRepo.AssertExpectations(t)

	// Test ClearItems triggers persistence
	mockRepo.On("Save", mock.AnythingOfType("*auth.DbSessionEntity")).Return(nil)
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
