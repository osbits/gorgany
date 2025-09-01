package auth

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Mock implementations
type MockSessionStorage struct {
	mock.Mock
}

type MockSessionFactory struct {
	mock.Mock
}

func (m *MockSessionFactory) CreateSession(id string, expiry time.Time) core.ISession {
	args := m.Called(id, expiry)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(core.ISession)
}

func (m *MockSessionFactory) CreateSessionWithUser(id string, userId string, expiry time.Time) core.ISession {
	args := m.Called(id, userId, expiry)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(core.ISession)
}

func (m *MockSessionStorage) ClearExpiredSessions() {
	m.Called()
}

func (m *MockSessionStorage) AddSession(session core.ISession) {
	m.Called(session)
}

func (m *MockSessionStorage) DeleteSession(session core.ISession) {
	m.Called(session)
}

func (m *MockSessionStorage) DeleteSessionById(id string) {
	m.Called(id)
}

func (m *MockSessionStorage) GetSessionById(id string) core.ISession {
	args := m.Called(id)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(core.ISession)
}

func (m *MockSessionStorage) SetSessionLifetime(lifetime time.Duration) {
	m.Called(lifetime)
}

func (m *MockSessionStorage) GetSessionLifetime() time.Duration {
	args := m.Called()
	return args.Get(0).(time.Duration)
}

type MockUserService struct {
	mock.Mock
}

func (m *MockUserService) Get(id any) (core.Authenticable, error) {
	args := m.Called(id)
	return args.Get(0).(core.Authenticable), args.Error(1)
}

func (m *MockUserService) GetByUsername(username string) (core.Authenticable, error) {
	args := m.Called(username)
	return args.Get(0).(core.Authenticable), args.Error(1)
}

func (m *MockUserService) Save(authEntity core.Authenticable) error {
	args := m.Called(authEntity)
	return args.Error(0)
}

type MockMessageContext struct {
	mock.Mock
}

func (m *MockMessageContext) GetURL() *url.URL {
	args := m.Called()
	return args.Get(0).(*url.URL)
}

func (m *MockMessageContext) GetRequestURL() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockMessageContext) GetCookieManager() core.ICookieManager {
	args := m.Called()
	return args.Get(0).(core.ICookieManager)
}

func (m *MockMessageContext) GetHeader() http.Header {
	args := m.Called()
	return args.Get(0).(http.Header)
}

func (m *MockMessageContext) GetPathParam(name string) string {
	args := m.Called(name)
	return args.String(0)
}

func (m *MockMessageContext) GetSession() core.ISession {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(core.ISession)
}

func (m *MockMessageContext) SetSession(session core.ISession) {
	m.Called(session)
}

func (m *MockMessageContext) GetRequest() *http.Request {
	args := m.Called()
	return args.Get(0).(*http.Request)
}

func (m *MockMessageContext) GetRequestId() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockMessageContext) GetIp() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockMessageContext) GetRequestContext() context.Context {
	args := m.Called()
	return args.Get(0).(context.Context)
}

type MockCookieManager struct {
	mock.Mock
}

func (m *MockCookieManager) SetCookie(cookie *http.Cookie) {
	m.Called(cookie)
}

func (m *MockCookieManager) GetCookie(key string) *http.Cookie {
	args := m.Called(key)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(*http.Cookie)
}

func (m *MockCookieManager) GetCookies() []*http.Cookie {
	args := m.Called()
	return args.Get(0).([]*http.Cookie)
}

// Test cases
func TestStandardAuthStrategy_NewSessionWithoutUser(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockSessionFactory := new(MockSessionFactory)
	mockMsgCtx := new(MockMessageContext)
	mockCookieManager := new(MockCookieManager)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
		sessionFactory: mockSessionFactory,
	}

	ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)

	// Expectations
	mockStorage.On("GetSessionLifetime").Return(time.Duration(3600))
	mockStorage.On("GetSessionById", mock.Anything).Return(nil)
	mockStorage.On("AddSession", mock.Anything).Return()
	
	// Create a test session
	testSession := &Session{
		id:           "test-session-id",
		expiry:       time.Now().Add(time.Hour),
		createdAt:    time.Now(),
		lastActivity: time.Now(),
		attributes:   make(map[string]string),
	}
	mockSessionFactory.On("CreateSession", mock.Anything, mock.Anything).Return(testSession)
	
	mockMsgCtx.On("GetCookieManager").Return(mockCookieManager)
	mockCookieManager.On("SetCookie", mock.Anything).Return()

	// Test
	session, err := strategy.NewSessionWithoutUser(ctx)

	// Assertions
	assert.NoError(t, err)
	assert.NotNil(t, session)
	assert.NotEmpty(t, session.GetId())
	assert.True(t, session.GetExpiry().After(time.Now()))
	assert.Empty(t, session.GetUserId())

	mockStorage.AssertExpectations(t)
	mockSessionFactory.AssertExpectations(t)
	mockMsgCtx.AssertExpectations(t)
	mockCookieManager.AssertExpectations(t)
}

func TestStandardAuthStrategy_ShouldRotateSession(t *testing.T) {
	// Setup
	strategy := &StandardAuthStrategy{}
	now := time.Now()

	tests := []struct {
		name     string
		session  core.ISession
		expected bool
	}{
		{
			name:     "nil session",
			session:  nil,
			expected: true,
		},
		{
			name: "expired session",
			session: &Session{
				createdAt:    now.Add(-sessionActivityTimeout - time.Minute),
				lastActivity: now.Add(-sessionActivityTimeout - time.Minute),
			},
			expected: false,
		},
		{
			name: "inactive session",
			session: &Session{
				createdAt:    now,
				expiry:       now.Add(time.Hour),
				lastActivity: now.Add(-sessionActivityTimeout - time.Minute),
			},
			expected: true,
		},
		{
			name: "valid session",
			session: &Session{
				createdAt:    now,
				lastActivity: now,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := strategy.ShouldRotateSession(tt.session)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestStandardAuthStrategy_Login(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockSessionFactory := new(MockSessionFactory)
	mockMsgCtx := new(MockMessageContext)
	mockCookieManager := new(MockCookieManager)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
		sessionFactory: mockSessionFactory,
	}

	ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)
	user := &MockUser{id: "test-user"}

	// Expectations
	mockStorage.On("GetSessionLifetime").Return(time.Duration(3600))
	mockStorage.On("GetSessionById", mock.Anything).Return(nil)
	mockStorage.On("AddSession", mock.Anything).Return()
	
	// Create a test session
	testSession := &Session{
		id:           "test-session-id",
		expiry:       time.Now().Add(time.Hour),
		createdAt:    time.Now(),
		lastActivity: time.Now(),
		attributes:   make(map[string]string),
	}
	mockSessionFactory.On("CreateSession", mock.Anything, mock.Anything).Return(testSession)
	
	mockMsgCtx.On("GetCookieManager").Return(mockCookieManager)
	mockCookieManager.On("SetCookie", mock.Anything).Return()
	mockCookieManager.On("GetCookie", core.SessionCookieName).Return(nil)
	mockMsgCtx.On("GetSession").Return(nil)

	// Test
	session, err := strategy.Login(user, ctx)

	// Assertions
	assert.NoError(t, err)
	assert.NotNil(t, session)
	assert.Equal(t, user.GetId(), session.GetUserId())
	assert.True(t, session.GetExpiry().After(time.Now()))

	mockStorage.AssertExpectations(t)
	mockSessionFactory.AssertExpectations(t)
	mockMsgCtx.AssertExpectations(t)
	mockCookieManager.AssertExpectations(t)
}

func TestStandardAuthStrategy_CurrentUser(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockMsgCtx := new(MockMessageContext)
	mockCookieManager := new(MockCookieManager)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
	}

	ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)
	user := &MockUser{id: "test-user"}
	session := &Session{
		id:        "test-session",
		userId:    user.GetId(),
		expiry:    time.Now().Add(time.Hour),
		createdAt: time.Now(),
	}

	// Expectations
	mockMsgCtx.On("GetCookieManager").Return(mockCookieManager)
	mockCookieManager.On("GetCookie", core.SessionCookieName).Return(&http.Cookie{Value: session.GetId()})
	mockStorage.On("GetSessionById", session.GetId()).Return(session)
	mockUserService.On("Get", user.GetId()).Return(user, nil)
	mockMsgCtx.On("GetSession").Return(session)

	// Test
	currentUser, err := strategy.CurrentUser(ctx)

	// Assertions
	assert.NoError(t, err)
	assert.NotNil(t, currentUser)
	assert.Equal(t, user.GetId(), currentUser.GetId())

	mockStorage.AssertExpectations(t)
	mockUserService.AssertExpectations(t)
}

func TestStandardAuthStrategy_CurrentUser_NoSession(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockMsgCtx := new(MockMessageContext)
	mockCookieManager := new(MockCookieManager)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
	}

	ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)

	// Expectations
	mockMsgCtx.On("GetCookieManager").Return(mockCookieManager)
	mockCookieManager.On("GetCookie", core.SessionCookieName).Return(nil)
	mockMsgCtx.On("GetSession").Return(nil)
	mockStorage.On("GetSessionById", mock.Anything).Return(nil)

	// Test
	user, err := strategy.CurrentUser(ctx)

	// Assertions
	assert.NoError(t, err)
	assert.Nil(t, user)

	mockStorage.AssertExpectations(t)
	mockUserService.AssertExpectations(t)
	mockMsgCtx.AssertExpectations(t)
	mockCookieManager.AssertExpectations(t)
}

func TestStandardAuthStrategy_CurrentUser_ExpiredSession(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockMsgCtx := new(MockMessageContext)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
	}

	ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)
	session := &Session{
		id:        "test-session",
		expiry:    time.Now().Add(-time.Hour), // Expired session
		createdAt: time.Now().Add(-time.Hour),
	}

	// Expectations
	mockStorage.On("GetSessionById", session.GetId()).Return(session)
	mockStorage.On("DeleteSession", session).Return()
	mockMsgCtx.On("GetSession").Return(session)

	// Test
	user, err := strategy.CurrentUser(ctx)

	// Assertions
	assert.NoError(t, err)
	assert.Nil(t, user)

	mockStorage.AssertExpectations(t)
	mockUserService.AssertExpectations(t)
	mockMsgCtx.AssertExpectations(t)
}

func TestStandardAuthStrategy_RotateSession(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockSessionFactory := new(MockSessionFactory)
	mockMsgCtx := new(MockMessageContext)
	mockCookieManager := new(MockCookieManager)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
		sessionFactory: mockSessionFactory,
	}

	ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)
	oldSession := &Session{
		id:        "old-session",
		userId:    "test-user",
		expiry:    time.Now().Add(time.Hour),
		createdAt: time.Now().Add(-sessionActivityTimeout - time.Minute),
	}

	// Expectations
	mockStorage.On("GetSessionLifetime").Return(time.Duration(3600))
	mockStorage.On("GetSessionById", mock.Anything).Return(nil)
	mockStorage.On("AddSession", mock.Anything).Return()
	mockStorage.On("DeleteSession", oldSession).Return()
	
	// Create a test session for the factory
	testSession := &Session{
		id:           "new-session-id",
		expiry:       time.Now().Add(time.Hour),
		createdAt:    time.Now(),
		lastActivity: time.Now(),
		attributes:   make(map[string]string),
	}
	mockSessionFactory.On("CreateSession", mock.Anything, mock.Anything).Return(testSession)
	
	mockMsgCtx.On("GetCookieManager").Return(mockCookieManager)
	mockCookieManager.On("SetCookie", mock.Anything).Return()

	// Test
	newSession, err := strategy.RotateSession(ctx, oldSession)

	// Assertions
	assert.NoError(t, err)
	assert.NotNil(t, newSession)
	assert.NotEqual(t, oldSession.GetId(), newSession.GetId())
	assert.Equal(t, oldSession.GetUserId(), newSession.GetUserId())
	assert.True(t, newSession.GetExpiry().After(time.Now()))
	assert.True(t, newSession.GetCreatedAt().After(oldSession.GetCreatedAt()))

	mockStorage.AssertExpectations(t)
	mockSessionFactory.AssertExpectations(t)
	mockMsgCtx.AssertExpectations(t)
	mockCookieManager.AssertExpectations(t)
}

func TestStandardAuthStrategy_Logout(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockMsgCtx := new(MockMessageContext)
	mockCookieManager := new(MockCookieManager)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
	}

	ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)
	session := &Session{
		id:        "test-session",
		userId:    "test-user",
		expiry:    time.Now().Add(time.Hour),
		createdAt: time.Now(),
	}

	// Expectations
	mockMsgCtx.On("GetCookieManager").Return(mockCookieManager)
	mockMsgCtx.On("GetSession").Return(session)
	mockStorage.On("DeleteSessionById", session.GetId()).Return()
	mockCookieManager.On("SetCookie", mock.MatchedBy(func(cookie *http.Cookie) bool {
		return cookie.Name == core.SessionCookieName && cookie.MaxAge < 0
	})).Return()

	// Test
	strategy.Logout(ctx)

	// Assertions
	mockStorage.AssertExpectations(t)
	mockMsgCtx.AssertExpectations(t)
}

func TestStandardAuthStrategy_IsLoggedIn(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockMsgCtx := new(MockMessageContext)
	mockCookieManager := new(MockCookieManager)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
	}

	tests := []struct {
		name           string
		session        core.ISession
		expectedResult bool
		setupMocks     func()
	}{
		{
			name:           "no session",
			session:        nil,
			expectedResult: false,
			setupMocks: func() {
				mockMsgCtx.On("GetSession").Return(nil)
				mockMsgCtx.On("GetCookieManager").Return(mockCookieManager)
				mockCookieManager.On("GetCookie", core.SessionCookieName).Return(nil)
				mockStorage.On("GetSessionById", mock.Anything).Return(nil)
			},
		},
		{
			name: "expired session",
			session: &Session{
				id:           "test-session",
				expiry:       time.Now().Add(-time.Hour),
				createdAt:    time.Now().Add(-sessionActivityTimeout - time.Minute),
				lastActivity: time.Now().Add(-sessionActivityTimeout - time.Minute),
			},
			expectedResult: false,
			setupMocks: func() {
				session := &Session{
					id:           "test-session",
					expiry:       time.Now().Add(-time.Hour),
					createdAt:    time.Now().Add(-sessionActivityTimeout - time.Minute),
					lastActivity: time.Now().Add(-sessionActivityTimeout - time.Minute),
				}
				mockStorage.On("GetSessionById", "test-session").Return(session)
				mockMsgCtx.On("GetSession").Return(session)
				mockStorage.On("DeleteSession", mock.Anything).Return()
			},
		},
		{
			name: "valid session with user",
			session: &Session{
				id:           "test-session",
				userId:       "test-user",
				expiry:       time.Now().Add(time.Hour),
				createdAt:    time.Now(),
				lastActivity: time.Now(),
			},
			expectedResult: true,
			setupMocks: func() {
				session := &Session{
					id:           "test-session",
					userId:       "test-user",
					expiry:       time.Now().Add(time.Hour),
					createdAt:    time.Now(),
					lastActivity: time.Now(),
				}
				mockMsgCtx.On("GetSession").Return(session)
				mockStorage.On("GetSessionById", "test-session").Return(session)
			},
		},
		{
			name: "valid session without user",
			session: &Session{
				id:           "test-session",
				expiry:       time.Now().Add(time.Hour),
				createdAt:    time.Now(),
				lastActivity: time.Now(),
			},
			expectedResult: false,
			setupMocks: func() {
				session := &Session{
					id:           "test-session",
					expiry:       time.Now().Add(time.Hour),
					createdAt:    time.Now(),
					lastActivity: time.Now(),
				}
				mockMsgCtx.On("GetSession").Return(session)
				mockStorage.On("GetSessionById", "test-session").Return(session)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mocks
			mockStorage = new(MockSessionStorage)
			mockUserService = new(MockUserService)
			mockMsgCtx = new(MockMessageContext)
			mockCookieManager = new(MockCookieManager)

			strategy = &StandardAuthStrategy{
				sessionManager: mockStorage,
				userService:    mockUserService,
			}

			// Setup mocks
			tt.setupMocks()

			ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)

			// Test
			result := strategy.IsLoggedIn(ctx)

			// Assertions
			assert.Equal(t, tt.expectedResult, result)
			mockStorage.AssertExpectations(t)
			mockMsgCtx.AssertExpectations(t)
			mockCookieManager.AssertExpectations(t)
		})
	}
}

func TestStandardAuthStrategy_IsRequestMadeWithStrategy(t *testing.T) {
	// Setup
	mockStorage := new(MockSessionStorage)
	mockUserService := new(MockUserService)
	mockMsgCtx := new(MockMessageContext)
	mockCookieManager := new(MockCookieManager)

	strategy := &StandardAuthStrategy{
		sessionManager: mockStorage,
		userService:    mockUserService,
	}

	tests := []struct {
		name           string
		sessionId      string
		expectedResult bool
		setupMocks     func()
	}{
		{
			name:           "no session id",
			sessionId:      "",
			expectedResult: false,
			setupMocks: func() {
				mockMsgCtx.On("GetSession").Return(nil)
				mockMsgCtx.On("GetCookieManager").Return(mockCookieManager)
				mockCookieManager.On("GetCookie", core.SessionCookieName).Return(nil)
			},
		},
		{
			name:           "valid session id",
			sessionId:      "test-session",
			expectedResult: true,
			setupMocks: func() {
				mockMsgCtx.On("GetSession").Return(&Session{id: "test-session"})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset mocks
			mockStorage = new(MockSessionStorage)
			mockUserService = new(MockUserService)
			mockMsgCtx = new(MockMessageContext)
			mockCookieManager = new(MockCookieManager)

			strategy = &StandardAuthStrategy{
				sessionManager: mockStorage,
				userService:    mockUserService,
			}

			// Setup mocks
			tt.setupMocks()

			ctx := context.WithValue(context.Background(), core.MessageContextKey, mockMsgCtx)

			// Test
			result := strategy.IsRequestMadeWithStrategy(ctx)

			// Assertions
			assert.Equal(t, tt.expectedResult, result)
			mockStorage.AssertExpectations(t)
			mockMsgCtx.AssertExpectations(t)
			mockCookieManager.AssertExpectations(t)
		})
	}
}

// MockUser implements core.Authenticable for testing
type MockUser struct {
	id       string
	username string
	password string
	role     core.UserRole
}

func (m *MockUser) GetId() string {
	return m.id
}

func (m *MockUser) GetUsername() string {
	return m.username
}

func (m *MockUser) GetPassword() string {
	return m.password
}

func (m *MockUser) GetRole() core.UserRole {
	return m.role
}
