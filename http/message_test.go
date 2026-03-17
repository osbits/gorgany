package http

import (
	"bytes"
	"context"
	"encoding/json"
	dbCore "git.qix.sx/gorgany/gorgany.git/db/sql/core"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	"git.qix.sx/gorgany/gorgany.git/model"
)

// Mock implementations
type MockEngineRenderer struct {
	mock.Mock
}

func (m *MockEngineRenderer) DoRender(ctx context.Context, w io.Writer, tpl string, data map[string]any) error {
	args := m.Called(ctx, w, tpl, data)
	return args.Error(0)
}

func (m *MockEngineRenderer) RegisterGlobalFunction(name string, fn interface{}) {
	m.Called(name, fn)
}

func (m *MockEngineRenderer) RegisterGlobalVariable(name string, value interface{}) {
	m.Called(name, value)
}

type MockAuthStrategy struct {
	mock.Mock
}

func (m *MockAuthStrategy) NewSessionWithoutUser(ctx context.Context) (core.ISession, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(core.ISession), args.Error(1)
}

func (m *MockAuthStrategy) Login(user core.Authenticable, ctx context.Context) (core.ISession, error) {
	args := m.Called(user, ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(core.ISession), args.Error(1)
}

func (m *MockAuthStrategy) IsLoggedIn(ctx context.Context) bool {
	args := m.Called(ctx)
	return args.Bool(0)
}

func (m *MockAuthStrategy) Logout(ctx context.Context) {
	m.Called(ctx)
}

func (m *MockAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(core.Authenticable), args.Error(1)
}

func (m *MockAuthStrategy) ResolveSessionId(ctx context.Context) string {
	args := m.Called(ctx)
	return args.String(0)
}

func (m *MockAuthStrategy) IsRequestMadeWithStrategy(ctx context.Context) bool {
	args := m.Called(ctx)
	return args.Bool(0)
}

func (m *MockAuthStrategy) CurrentSession(ctx context.Context) core.ISession {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(core.ISession)
}

func (m *MockAuthStrategy) ShouldRotateSession(session core.ISession) bool {
	args := m.Called(session)
	return args.Bool(0)
}

func (m *MockAuthStrategy) RotateSession(ctx context.Context, oldSession core.ISession) (core.ISession, error) {
	args := m.Called(ctx, oldSession)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(core.ISession), args.Error(1)
}

type MockAuthContext struct {
	mock.Mock
}

func (m *MockAuthContext) ResolveAuthStrategyByContext(ctx context.Context) core.IAuthStrategy {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(core.IAuthStrategy)
}

func (m *MockAuthContext) GetAuthStrategy(names ...string) core.IAuthStrategy {
	args := m.Called(names)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(core.IAuthStrategy)
}

func (m *MockAuthContext) RegisterAuthStrategy(name string, strategy core.IAuthStrategy) {
	m.Called(name, strategy)
}

func (m *MockAuthContext) Strategy(names ...string) core.IAuthStrategy {
	args := m.Called(names)
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(core.IAuthStrategy)
}

type MockSessionStorage struct {
	mock.Mock
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

func (m *MockSessionStorage) GetSessionLifetime() time.Duration {
	args := m.Called()
	return args.Get(0).(time.Duration)
}

func (m *MockSessionStorage) SetSessionLifetime(duration time.Duration) {
	m.Called(duration)
}

func (m *MockSessionStorage) ClearExpiredSessions() {
	m.Called()
}

func (m *MockSessionStorage) GetSessionRotationInterval() time.Duration {
	args := m.Called()
	return args.Get(0).(time.Duration)
}

func (m *MockSessionStorage) GetSessionActivityTimeout() time.Duration {
	args := m.Called()
	return args.Get(0).(time.Duration)
}

type MockSession struct {
	mock.Mock
}

func (m *MockSession) GetId() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockSession) GetExpiry() time.Time {
	args := m.Called()
	return args.Get(0).(time.Time)
}

func (m *MockSession) GetUserId() string {
	args := m.Called()
	return args.String(0)
}

func (m *MockSession) SetUserId(id string) {
	m.Called(id)
}

func (m *MockSession) IsExpired() bool {
	args := m.Called()
	return args.Bool(0)
}

func (m *MockSession) SetExpiry(t time.Time) {
	m.Called(t)
}

func (m *MockSession) GetItem(key string) string {
	args := m.Called(key)
	return args.String(0)
}

func (m *MockSession) SetItem(key, value string) {
	m.Called(key, value)
}

func (m *MockSession) ClearItem(attribute string) {
	m.Called(attribute)
}

func (m *MockSession) ClearItems() {
	m.Called()
}

func (m *MockSession) GetLastActivity() time.Time {
	args := m.Called()
	return args.Get(0).(time.Time)
}

func (m *MockSession) SetLastActivity(t time.Time) {
	m.Called(t)
}

func (m *MockSession) GetCreatedAt() time.Time {
	args := m.Called()
	return args.Get(0).(time.Time)
}

type MockDBContext struct {
	mock.Mock
}

func (m *MockDBContext) Init() {
	m.Called()
}

func (m *MockDBContext) RegisterDataSource(name string, dbConnection dbCore.IDataSource) {
	m.Called(name, dbConnection)
}

func (m *MockDBContext) GetDataSource(name string) dbCore.IDataSource {
	return new(MockDBConnection)
}

type MockDBConnection struct {
	mock.Mock
}

func (thiz *MockDBConnection) NewSession() (dbCore.ISession, error) {
	return nil, nil
}

func (thiz *MockDBConnection) GetDriver() (any, error) {
	return nil, nil
}

func (thiz *MockDBConnection) Close() error {
	return nil
}

func TestHTTPRequestScope_IP(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		expectedIP string
	}{
		{
			name: "X-Forwarded-For header",
			headers: map[string]string{
				"X-Forwarded-For": "192.168.1.1, 10.0.0.1",
			},
			expectedIP: "192.168.1.1",
		},
		{
			name: "X-Real-IP header",
			headers: map[string]string{
				"X-Real-IP": "192.168.1.2",
			},
			expectedIP: "192.168.1.2",
		},
		{
			name:       "RemoteAddr",
			remoteAddr: "192.168.1.3:8080",
			expectedIP: "192.168.1.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}

			scope := NewHTTPRequestScope(req)
			ip := scope.IP()
			assert.Equal(t, tt.expectedIP, ip)
		})
	}
}

func TestHTTPRequestScope_FormFile(t *testing.T) {
	// Create test file
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "test.txt")
	assert.NoError(t, err)
	part.Write([]byte("test content"))
	writer.Close()

	req := httptest.NewRequest("POST", "/", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	scope := NewHTTPRequestScope(req)
	file, err := scope.FormFile("file")
	assert.NoError(t, err)
	assert.NotNil(t, file)

	// Test reading the file
	buf := new(bytes.Buffer)
	_, err = file.Read(buf)
	assert.NoError(t, err)
	assert.Equal(t, "test content", buf.String())

	// Test writing to public storage
	written, err := file.Write("test", bytes.NewReader([]byte("test content")))
	assert.NoError(t, err)
	assert.Greater(t, written, int64(0))

	// Verify file exists in public storage
	assert.True(t, file.IsExists())

	// Cleanup
	err = file.Close()
	assert.NoError(t, err)

	// Verify temp file is removed
	assert.False(t, file.IsExists())
}

func TestMessage_RedirectWithFlash(t *testing.T) {
	// Setup
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	mockSession := new(MockSession)
	mockDBContext := new(MockDBContext)
	mockDBConnection := new(MockDBConnection)
	mockAuthContext := new(MockAuthContext)
	mockSessionStorage := new(MockSessionStorage)
	mockAuthStrategy := new(MockAuthStrategy)

	// Setup DB context
	db.SetDBContext(mockDBContext)
	mockDBContext.On("GetDataSource", core.DefaultKeyInRegistrar).Return(mockDBConnection)
	mockDBConnection.On("WithContext", mock.Anything).Return(mockDBConnection)

	// Setup auth context and strategy
	mockAuthContext.On("ResolveAuthStrategyByContext", mock.Anything).Return(mockAuthStrategy)

	// Setup auth strategy
	mockAuthStrategy.On("CurrentSession", mock.Anything).Return(mockSession)

	// Setup session
	mockSession.On("SetItem", core.OneTimeSessionAttributeKey, mock.Anything).Return()

	msg := &Message{
		writer:         w,
		request:        req,
		authContext:    mockAuthContext,
		sessionStorage: mockSessionStorage,
	}
	msg.Init()

	// Test data
	flashData := map[string]interface{}{
		"message": "Test message",
		"type":    "success",
	}

	// Execute
	msg.RedirectWithFlash("/redirect", http.StatusFound, flashData)

	// Verify
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/redirect", w.Header().Get("Location"))

	// Verify flash data was set
	var oneTimeParams model.OneTimeParams
	flashJSON := mockSession.Calls[len(mockSession.Calls)-1].Arguments[1].(string)
	err := json.Unmarshal([]byte(flashJSON), &oneTimeParams)
	assert.NoError(t, err)
	assert.True(t, oneTimeParams.Start)
	assert.Equal(t, "Test message", oneTimeParams.Values["message"])
	assert.Equal(t, "success", oneTimeParams.Values["type"])

	// Verify all mock expectations were met
	mockSession.AssertExpectations(t)
	mockAuthContext.AssertExpectations(t)
	mockAuthStrategy.AssertExpectations(t)
	mockSessionStorage.AssertExpectations(t)
}

func TestHTTPResponseScope_JSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	scope := NewHTTPResponseScope(w, r)

	testData := map[string]string{
		"key": "value",
	}

	scope.JSON(testData, http.StatusOK)

	assert.Equal(t, "application/json; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, http.StatusOK, w.Code)

	var response map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "value", response["key"])
}

func TestHTTPResponseScope_Text(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	scope := NewHTTPResponseScope(w, r)

	scope.Text("Hello, World!", http.StatusOK)

	assert.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Hello, World!", w.Body.String())
}

func TestMessage_Init(t *testing.T) {
	// Setup mocks
	mockDBContext := new(MockDBContext)
	mockDBConnection := new(MockDBConnection)
	mockViewEngine := new(MockEngineRenderer)
	mockAuthContext := new(MockAuthContext)
	mockSessionStorage := new(MockSessionStorage)
	mockAuthStrategy := new(MockAuthStrategy)
	mockSession := new(MockSession)

	// Setup DB context - must be done before creating Message instance
	db.SetDBContext(mockDBContext)
	mockDBContext.On("GetDataSource", core.DefaultKeyInRegistrar).Return(mockDBConnection)
	mockDBConnection.On("WithContext", mock.Anything).Return(mockDBConnection)

	// Setup auth context and strategy
	mockAuthContext.On("ResolveAuthStrategyByContext", mock.Anything).Return(mockAuthStrategy)

	// Setup auth strategy
	mockAuthStrategy.On("CurrentSession", mock.Anything).Return(mockSession)

	// Create test request and response
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	// Create message instance
	msg := &Message{
		writer:         w,
		request:        req,
		viewEngine:     mockViewEngine,
		authContext:    mockAuthContext,
		sessionStorage: mockSessionStorage,
	}

	// Execute
	msg.Init()

	// Verify
	assert.NotNil(t, msg.Req)
	assert.NotNil(t, msg.Res)
	assert.NotNil(t, msg.Ses)
	assert.NotNil(t, msg.Vw)
	assert.NotNil(t, msg.CookieManager)
	assert.NotNil(t, msg.Context())

	// Verify auth context was used
	mockAuthContext.AssertExpectations(t)
	mockAuthStrategy.AssertExpectations(t)
	mockSession.AssertExpectations(t)
	mockSessionStorage.AssertExpectations(t)

	// Verify context values
	msgCtx := msg.Context().Value(core.MessageContextKey).(*messageContext)
	assert.NotNil(t, msgCtx)
	assert.Equal(t, req.URL, msgCtx.url)
	assert.Equal(t, req.RequestURI, msgCtx.requestURI)
	assert.NotEmpty(t, msgCtx.requestId)
	assert.Equal(t, "192.0.2.1", msgCtx.ip) // IP should be without port
}

func TestMessage_Init_WithPathParams(t *testing.T) {
	// Setup mocks
	mockDBContext := new(MockDBContext)
	mockDBConnection := new(MockDBConnection)
	mockViewEngine := new(MockEngineRenderer)
	mockAuthContext := new(MockAuthContext)
	mockSessionStorage := new(MockSessionStorage)

	// Setup DB context
	db.SetDBContext(mockDBContext)
	mockDBContext.On("GetDataSource", core.DefaultKeyInRegistrar).Return(mockDBConnection)
	mockDBConnection.On("WithContext", mock.Anything).Return(mockDBConnection)

	// Create test request with path parameters
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test/123", nil)

	// Setup chi router context with path params
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "123")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	// Create message instance
	msg := &Message{
		writer:         w,
		request:        req,
		viewEngine:     mockViewEngine,
		authContext:    mockAuthContext,
		sessionStorage: mockSessionStorage,
	}

	// Execute
	msg.Init()

	// Verify path parameters
	assert.Equal(t, "123", msg.Req.PathParam("id"))
}

func TestMessage_Close(t *testing.T) {
	// Setup mocks
	mockDBContext := new(MockDBContext)
	mockDBConnection := new(MockDBConnection)
	mockViewEngine := new(MockEngineRenderer)
	mockAuthContext := new(MockAuthContext)
	mockSessionStorage := new(MockSessionStorage)
	mockAuthStrategy := new(MockAuthStrategy)
	mockSession := new(MockSession)

	// Setup DB context - must be done before creating Message instance
	db.SetDBContext(mockDBContext)
	mockDBContext.On("GetDataSource", core.DefaultKeyInRegistrar).Return(mockDBConnection)
	mockDBConnection.On("WithContext", mock.Anything).Return(mockDBConnection)

	// Setup auth context and strategy
	mockAuthContext.On("ResolveAuthStrategyByContext", mock.Anything).Return(mockAuthStrategy)

	// Setup auth strategy
	mockAuthStrategy.On("CurrentSession", mock.Anything).Return(mockSession)

	// Setup session
	mockSession.On("GetItem", core.OneTimeSessionAttributeKey).Return("")

	// Create test request and response
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)

	// Create message instance
	msg := &Message{
		writer:         w,
		request:        req,
		viewEngine:     mockViewEngine,
		authContext:    mockAuthContext,
		sessionStorage: mockSessionStorage,
	}

	// Initialize and close
	msg.Init()
	err := msg.Close()

	// Verify
	assert.NoError(t, err)
	mockSession.AssertExpectations(t)
}
