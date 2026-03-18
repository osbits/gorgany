package model

import (
	"context"
	"time"

	"github.com/osbits/gorgany/app/core"
)

// MockAuthContext for testing and examples
type MockAuthContext struct {
	currentUser core.Authenticable
}

func (m *MockAuthContext) RegisterAuthStrategy(name string, strategy core.IAuthStrategy) {}

func (m *MockAuthContext) GetAuthStrategy(strategyName ...string) core.IAuthStrategy {
	return &MockAuthStrategy{currentUser: m.currentUser}
}

func (m *MockAuthContext) Strategy(strategyName ...string) core.IAuthStrategy {
	return &MockAuthStrategy{currentUser: m.currentUser}
}

func (m *MockAuthContext) ResolveAuthStrategyByContext(ctx context.Context) core.IAuthStrategy {
	// Check if user is in context first
	if user, ok := ctx.Value("current_user").(core.Authenticable); ok {
		return &MockAuthStrategy{currentUser: user}
	}
	return &MockAuthStrategy{currentUser: m.currentUser}
}

// MockAuthStrategy for testing and examples
type MockAuthStrategy struct {
	currentUser core.Authenticable
}

func (m *MockAuthStrategy) NewSessionWithoutUser(ctx context.Context) (core.ISession, error) {
	return &MockSession{}, nil
}

func (m *MockAuthStrategy) Login(user core.Authenticable, ctx context.Context) (core.ISession, error) {
	m.currentUser = user
	return &MockSession{}, nil
}

func (m *MockAuthStrategy) IsLoggedIn(ctx context.Context) bool {
	return m.currentUser != nil
}

func (m *MockAuthStrategy) Logout(ctx context.Context) {
	m.currentUser = nil
}

func (m *MockAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	return m.currentUser, nil
}

func (m *MockAuthStrategy) ResolveSessionId(ctx context.Context) string {
	return "mock-session-id"
}

func (m *MockAuthStrategy) IsRequestMadeWithStrategy(ctx context.Context) bool {
	return true
}

func (m *MockAuthStrategy) CurrentSession(ctx context.Context) core.ISession {
	return &MockSession{}
}

func (m *MockAuthStrategy) ShouldRotateSession(session core.ISession) bool {
	return false
}

func (m *MockAuthStrategy) RotateSession(ctx context.Context, oldSession core.ISession) (core.ISession, error) {
	return &MockSession{}, nil
}

// MockSession for testing
type MockSession struct {
	id           string
	userId       string
	expiry       time.Time
	createdAt    time.Time
	lastActivity time.Time
	data         map[string]string
}

func (m *MockSession) GetId() string {
	return m.id
}

func (m *MockSession) GetExpiry() time.Time {
	return m.expiry
}

func (m *MockSession) GetUserId() string {
	return m.userId
}

func (m *MockSession) SetUserId(id string) {
	m.userId = id
}

func (m *MockSession) IsExpired() bool {
	return time.Now().After(m.expiry)
}

func (m *MockSession) SetExpiry(t time.Time) {
	m.expiry = t
}

func (m *MockSession) GetCreatedAt() time.Time {
	return m.createdAt
}

func (m *MockSession) GetLastActivity() time.Time {
	return m.lastActivity
}

func (m *MockSession) SetLastActivity(t time.Time) {
	m.lastActivity = t
}

// ISimpleStorage implementation
func (m *MockSession) GetItem(key string) string {
	if m.data == nil {
		m.data = make(map[string]string)
	}
	return m.data[key]
}

func (m *MockSession) SetItem(key string, value string) {
	if m.data == nil {
		m.data = make(map[string]string)
	}
	m.data[key] = value
}

func (m *MockSession) ClearItem(key string) {
	if m.data != nil {
		delete(m.data, key)
	}
}

func (m *MockSession) ClearItems() {
	m.data = make(map[string]string)
}
