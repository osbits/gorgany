package http

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/model"
)

// memoSession is a session whose user id the strategy below resolves the principal from,
// which is how the real session-backed strategy works: the store hands back the session
// and the user is loaded from the id on it.
type memoSession struct {
	id     string
	userId string
	items  map[string]string
}

func (s *memoSession) GetId() string              { return s.id }
func (s *memoSession) GetExpiry() time.Time       { return time.Now().Add(time.Hour) }
func (s *memoSession) GetUserId() string          { return s.userId }
func (s *memoSession) SetUserId(id string)        { s.userId = id }
func (s *memoSession) IsExpired() bool            { return false }
func (s *memoSession) SetExpiry(time.Time)        {}
func (s *memoSession) GetCreatedAt() time.Time    { return time.Now() }
func (s *memoSession) GetLastActivity() time.Time { return time.Now() }
func (s *memoSession) SetLastActivity(time.Time)  {}
func (s *memoSession) GetItem(key string) string  { return s.items[key] }
func (s *memoSession) SetItem(key, value string) {
	if s.items == nil {
		s.items = map[string]string{}
	}
	s.items[key] = value
}
func (s *memoSession) ClearItem(key string) { delete(s.items, key) }
func (s *memoSession) ClearItems()          { s.items = map[string]string{} }

type memoUser struct {
	id    string
	roles []string
}

func (u *memoUser) GetId() string          { return u.id }
func (u *memoUser) GetUsername() string    { return u.id }
func (u *memoUser) GetPassword() string    { return "" }
func (u *memoUser) GetRole() core.UserRole { return core.UserRole(u.roles[0]) }
func (u *memoUser) GetRoles() []string     { return u.roles }

// memoAuthContext counts how often the identity is resolved. In a real application each
// resolution is a session lookup followed by a user load, both of them database round
// trips, which is why the count is the thing under test.
type memoAuthContext struct {
	resolutions int64
	current     core.ISession
}

func (a *memoAuthContext) RegisterAuthStrategy(string, core.IAuthStrategy) {}
func (a *memoAuthContext) GetAuthStrategy(...string) core.IAuthStrategy {
	return &memoAuthStrategy{owner: a}
}
func (a *memoAuthContext) Strategy(...string) core.IAuthStrategy {
	return &memoAuthStrategy{owner: a}
}
func (a *memoAuthContext) ResolveAuthStrategyByContext(context.Context) core.IAuthStrategy {
	return &memoAuthStrategy{owner: a}
}
func (a *memoAuthContext) count() int64 { return atomic.LoadInt64(&a.resolutions) }

type memoAuthStrategy struct {
	owner *memoAuthContext
}

func (s *memoAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	atomic.AddInt64(&s.owner.resolutions, 1)

	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, nil
	}
	session := messageContext.GetSession()
	if session == nil || session.GetUserId() == "" {
		return nil, nil
	}

	roles := []string{"employee"}
	if strings.HasPrefix(session.GetUserId(), "hr-") {
		roles = []string{"hr"}
	}
	return &memoUser{id: session.GetUserId(), roles: roles}, nil
}

func (s *memoAuthStrategy) NewSessionWithoutUser(context.Context) (core.ISession, error) {
	return s.owner.current, nil
}
func (s *memoAuthStrategy) Login(core.Authenticable, context.Context) (core.ISession, error) {
	return s.owner.current, nil
}
func (s *memoAuthStrategy) IsLoggedIn(context.Context) bool                { return true }
func (s *memoAuthStrategy) Logout(context.Context) error                   { return nil }
func (s *memoAuthStrategy) ResolveSessionId(context.Context) string        { return "" }
func (s *memoAuthStrategy) IsRequestMadeWithStrategy(context.Context) bool { return true }
func (s *memoAuthStrategy) CurrentSession(context.Context) core.ISession   { return s.owner.current }
func (s *memoAuthStrategy) ShouldRotateSession(core.ISession) bool         { return false }
func (s *memoAuthStrategy) RotateSession(_ context.Context, old core.ISession) (core.ISession, error) {
	return old, nil
}

// memoAccessControlConfig gates one field on the "hr" role, so a mixed-up identity is a
// visibly different authorization decision and not only a different user id.
func memoAccessControlConfig() *model.AccessControlConfig {
	return model.NewAccessControlBuilder().
		AddDomainOperation("read", true, "employee", "hr").
		AddFieldAccess("name", map[string]model.OperationConfig{
			"read": {Allowed: true},
		}).
		AddFieldAccess("ssn", map[string]model.OperationConfig{
			"read": {Allowed: true, RequiredRoles: []string{"hr"}},
		}).
		Build()
}

func memoMessage(authContext *memoAuthContext) *Message {
	message := &Message{
		writer:      httptest.NewRecorder(),
		request:     httptest.NewRequest("GET", "/employees", nil),
		authContext: authContext,
	}
	message.Init()
	return message
}

func memoReadsField(fields []string, want string) bool {
	for _, field := range fields {
		if strings.EqualFold(field, want) {
			return true
		}
	}
	return false
}

// A list response asks for the readable fields once per row. Against the framework's own
// per-request context that must cost one identity resolution for the whole request, not
// one per row: FieldFilteredDto.MarshalJSON calls GetReadableFields per DTO, and every
// resolution is a session lookup plus a user load.
func TestMessageContextResolvesTheIdentityOncePerRequest(t *testing.T) {
	authContext := &memoAuthContext{current: &memoSession{id: "session-1", userId: "employee-1"}}
	message := memoMessage(authContext)
	rbac := model.NewRoleBasedAccessControl(memoAccessControlConfig(), authContext)

	ctx := message.Context()

	const rows = 100
	for i := 0; i < rows; i++ {
		fields := rbac.GetReadableFields(ctx, nil)
		if !memoReadsField(fields, "name") {
			t.Fatalf("row %d could not read name: %v", i, fields)
		}
		if memoReadsField(fields, "ssn") {
			t.Fatalf("row %d read the hr-only field as an employee: %v", i, fields)
		}
	}

	if got := authContext.count(); got != 1 {
		t.Fatalf("%d rows performed %d identity resolutions; want exactly 1", rows, got)
	}
}

// Login rotates the session onto a new identifier and publishes it into the request scope
// through PublishSession. Everything the request asks afterwards has to be answered about
// the new principal - a memo that survives this is an authorization bug, and a worse one
// than the repeated lookup the memo exists to avoid.
func TestPublishSessionDropsTheIdentityMemoisedBeforeIt(t *testing.T) {
	authContext := &memoAuthContext{current: &memoSession{id: "session-1", userId: "employee-1"}}
	message := memoMessage(authContext)
	rbac := model.NewRoleBasedAccessControl(memoAccessControlConfig(), authContext)

	// The context the handler captured before logging in. The swap has to reach it too.
	ctx := message.Context()

	if user := rbac.GetCurrentUser(ctx); user == nil || user.GetId() != "employee-1" {
		t.Fatalf("pre-login resolution answered about %v", user)
	}
	if memoReadsField(rbac.GetReadableFields(ctx, nil), "ssn") {
		t.Fatal("the hr-only field was readable before the hr login")
	}

	// What StandardAuthStrategy.Login leaves behind: a different session identifier
	// carrying the authenticated user id, installed with PublishSession.
	rotated := &memoSession{id: "session-rotated", userId: "hr-1"}
	authContext.current = rotated
	PublishSession(message, rotated)

	user := rbac.GetCurrentUser(ctx)
	if user == nil || user.GetId() != "hr-1" {
		t.Fatalf("after the login the request is still answered about %v", user)
	}
	if !memoReadsField(rbac.GetReadableFields(ctx, nil), "ssn") {
		t.Fatal("the hr-only field is still hidden after the hr login")
	}
}

// Two requests in flight at once carry two contexts and must never be answered about each
// other's principal. Run with -race.
func TestConcurrentMessagesKeepTheirIdentitiesApart(t *testing.T) {
	config := memoAccessControlConfig()

	var wg sync.WaitGroup
	for r, userId := range []string{"employee-1", "hr-2", "employee-3", "hr-4"} {
		wg.Add(1)
		go func(r int, userId string) {
			defer wg.Done()

			authContext := &memoAuthContext{current: &memoSession{id: "session", userId: userId}}
			message := memoMessage(authContext)
			rbac := model.NewRoleBasedAccessControl(config, authContext)
			ctx := message.Context()

			wantSSN := strings.HasPrefix(userId, "hr-")
			for i := 0; i < 300; i++ {
				user := rbac.GetCurrentUser(ctx)
				if user == nil || user.GetId() != userId {
					t.Errorf("request for %s was answered about %v", userId, user)
					return
				}
				if got := memoReadsField(rbac.GetReadableFields(ctx, nil), "ssn"); got != wantSSN {
					t.Errorf("request for %s: readable ssn = %v, want %v", userId, got, wantSSN)
					return
				}
			}
		}(r, userId)
	}
	wg.Wait()
}

// A handler that fans out drives one request context from several goroutines, so the memo
// slot on that context is shared between them. Run with -race.
func TestOneMessageIsSafeToResolveFromSeveralGoroutines(t *testing.T) {
	authContext := &memoAuthContext{current: &memoSession{id: "session-1", userId: "hr-1"}}
	message := memoMessage(authContext)
	rbac := model.NewRoleBasedAccessControl(memoAccessControlConfig(), authContext)

	ctx := message.Context()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if user := rbac.GetCurrentUser(ctx); user == nil || user.GetId() != "hr-1" {
					t.Errorf("fanned-out call was answered about %v", user)
					return
				}
				if !memoReadsField(rbac.GetReadableFields(ctx, nil), "ssn") {
					t.Error("fanned-out call lost the hr role")
					return
				}
			}
		}()
	}
	wg.Wait()
}
