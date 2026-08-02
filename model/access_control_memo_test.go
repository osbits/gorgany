package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/osbits/gorgany/v2/app/core"
)

// ---------------------------------------------------------------------------
// A per-request carrier that memoises one resolved identity.
//
// The framework's own carrier is http's messageContext; this stub stands in for it so
// that the model package can be tested without importing http (which imports model).
// The behaviour that matters here is the contract, not the implementation: hold one
// identity, hand it back only for the key it was stored under, and be safe when more
// than one goroutine of the same request touches it.
// ---------------------------------------------------------------------------

type memoTestSession struct {
	id     string
	userId string
}

func (s *memoTestSession) GetId() string              { return s.id }
func (s *memoTestSession) GetExpiry() time.Time       { return time.Now().Add(time.Hour) }
func (s *memoTestSession) GetUserId() string          { return s.userId }
func (s *memoTestSession) SetUserId(id string)        { s.userId = id }
func (s *memoTestSession) IsExpired() bool            { return false }
func (s *memoTestSession) SetExpiry(time.Time)        {}
func (s *memoTestSession) GetCreatedAt() time.Time    { return time.Now() }
func (s *memoTestSession) GetLastActivity() time.Time { return time.Now() }
func (s *memoTestSession) SetLastActivity(time.Time)  {}
func (s *memoTestSession) GetItem(string) string      { return "" }
func (s *memoTestSession) SetItem(_ string, _ string) {}
func (s *memoTestSession) ClearItem(string)           {}
func (s *memoTestSession) ClearItems()                {}

type memoTestMessageContext struct {
	mu      sync.Mutex
	session core.ISession

	identityKey    string
	identity       any
	identityStored bool
}

// The stub has to satisfy the same contracts the framework's own carrier does, or it stops
// standing in for it.
var (
	_ core.IMessageContext      = (*memoTestMessageContext)(nil)
	_ core.IRequestIdentityMemo = (*memoTestMessageContext)(nil)
)

func (c *memoTestMessageContext) GetURL() *url.URL                      { return &url.URL{Path: "/"} }
func (c *memoTestMessageContext) GetRequestURL() string                 { return "/" }
func (c *memoTestMessageContext) GetCookieManager() core.ICookieManager { return nil }
func (c *memoTestMessageContext) GetHeader() http.Header                { return http.Header{} }
func (c *memoTestMessageContext) GetPathParam(string) string            { return "" }
func (c *memoTestMessageContext) GetRequest() *http.Request             { return nil }
func (c *memoTestMessageContext) GetRequestId() string                  { return "req" }
func (c *memoTestMessageContext) GetIp() string                         { return "127.0.0.1" }
func (c *memoTestMessageContext) GetRequestContext() context.Context    { return context.Background() }

func (c *memoTestMessageContext) GetSession() core.ISession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// setSession is what the session middleware and PublishSession do to the real carrier.
func (c *memoTestMessageContext) setSession(session core.ISession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.session = session
}

func (c *memoTestMessageContext) LoadIdentity(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.identityStored || c.identityKey != key {
		return nil, false
	}
	return c.identity, true
}

func (c *memoTestMessageContext) StoreIdentity(key string, identity any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.identityKey = key
	c.identity = identity
	c.identityStored = true
}

func (c *memoTestMessageContext) InvalidateIdentity() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.identityKey = ""
	c.identity = nil
	c.identityStored = false
}

// ---------------------------------------------------------------------------
// A counting auth strategy: resolution is a session lookup plus a user load in a real
// application, so the number of times it runs is the thing under test.
// ---------------------------------------------------------------------------

type countingAuthContext struct {
	resolutions int64
}

func (a *countingAuthContext) RegisterAuthStrategy(string, core.IAuthStrategy) {}
func (a *countingAuthContext) GetAuthStrategy(...string) core.IAuthStrategy {
	return &countingAuthStrategy{owner: a}
}
func (a *countingAuthContext) Strategy(...string) core.IAuthStrategy {
	return &countingAuthStrategy{owner: a}
}
func (a *countingAuthContext) ResolveAuthStrategyByContext(context.Context) core.IAuthStrategy {
	return &countingAuthStrategy{owner: a}
}
func (a *countingAuthContext) count() int64 { return atomic.LoadInt64(&a.resolutions) }

type countingAuthStrategy struct {
	owner *countingAuthContext
}

// memoTestPrincipal builds the user a session's user id refers to. Roles come from the
// id prefix so that a mixed-up identity shows up as a different authorization decision
// and not only as a different id.
func memoTestPrincipal(userId string) core.Authenticable {
	roles := []string{"employee"}
	switch {
	case strings.HasPrefix(userId, "hr-"):
		roles = []string{"hr"}
	case strings.HasPrefix(userId, "admin-"):
		roles = []string{"admin"}
	}
	return &TestUser{ID: userId, Username: userId, Roles: roles}
}

func (s *countingAuthStrategy) CurrentUser(ctx context.Context) (core.Authenticable, error) {
	atomic.AddInt64(&s.owner.resolutions, 1)

	messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return nil, nil
	}
	session := messageContext.GetSession()
	if session == nil || session.GetUserId() == "" {
		return nil, nil
	}
	return memoTestPrincipal(session.GetUserId()), nil
}

func (s *countingAuthStrategy) NewSessionWithoutUser(context.Context) (core.ISession, error) {
	return &memoTestSession{id: "new"}, nil
}
func (s *countingAuthStrategy) Login(core.Authenticable, context.Context) (core.ISession, error) {
	return &memoTestSession{id: "new"}, nil
}
func (s *countingAuthStrategy) IsLoggedIn(context.Context) bool                { return true }
func (s *countingAuthStrategy) Logout(context.Context) error                   { return nil }
func (s *countingAuthStrategy) ResolveSessionId(context.Context) string        { return "" }
func (s *countingAuthStrategy) IsRequestMadeWithStrategy(context.Context) bool { return true }
func (s *countingAuthStrategy) CurrentSession(ctx context.Context) core.ISession {
	if messageContext, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext); ok {
		return messageContext.GetSession()
	}
	return nil
}
func (s *countingAuthStrategy) ShouldRotateSession(core.ISession) bool { return false }
func (s *countingAuthStrategy) RotateSession(_ context.Context, old core.ISession) (core.ISession, error) {
	return old, nil
}

// memoTestRequest builds a request context carrying its own memoising carrier, with the
// session already published as the session middleware would have left it.
func memoTestRequest(sessionId, userId string) (context.Context, *memoTestMessageContext) {
	carrier := &memoTestMessageContext{session: &memoTestSession{id: sessionId, userId: userId}}
	return context.WithValue(context.Background(), core.MessageContextKey, carrier), carrier
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// One request asks the same question once per row of its response. Resolving the identity
// is a session lookup plus a user load - a database round trip each, for a session-backed
// strategy - so a 100-row list must not pay for 100 of them.
func TestIdentityIsResolvedOncePerRequest(t *testing.T) {
	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)
	entity := concurrencyTestEntity()

	ctx, _ := memoTestRequest("session-1", "hr-1")

	const rows = 100
	for i := 0; i < rows; i++ {
		fields := rbac.GetReadableFields(ctx, entity)
		if len(fields) == 0 {
			t.Fatalf("call %d resolved no readable fields at all", i)
		}
	}

	if got := authContext.count(); got != 1 {
		t.Fatalf("%d calls to GetReadableFields performed %d identity resolutions; want exactly 1", rows, got)
	}
}

// The marshalling path is where the amplification actually lands: FieldFilteredDto asks
// for the readable fields once per DTO, so a collection asks once per row.
func TestMarshallingACollectionResolvesTheIdentityOnce(t *testing.T) {
	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)

	ctx, _ := memoTestRequest("session-1", "hr-1")

	const rows = 100
	items := make([]interface{}, 0, rows)
	for i := 0; i < rows; i++ {
		items = append(items, &Employee{ID: fmt.Sprintf("emp-%d", i), Name: "Employee"})
	}

	collection := NewFieldFilteredCollection(items, rbac, ctx, nil)
	if _, err := json.Marshal(collection); err != nil {
		t.Fatalf("marshalling the collection failed: %v", err)
	}

	if got := authContext.count(); got != 1 {
		t.Fatalf("marshalling %d rows performed %d identity resolutions; want exactly 1", rows, got)
	}
}

// Two requests in flight at once must never be answered about each other's principal.
// Run with -race.
func TestConcurrentRequestsNeverSeeEachOthersIdentity(t *testing.T) {
	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)
	entity := concurrencyTestEntity()

	principals := []string{"hr-1", "employee-2", "admin-3", "employee-4", "hr-5", "employee-6"}

	var wg sync.WaitGroup
	for r, userId := range principals {
		wg.Add(1)
		go func(r int, userId string) {
			defer wg.Done()

			ctx, _ := memoTestRequest(fmt.Sprintf("session-%d", r), userId)
			expectSSN := strings.HasPrefix(userId, "hr-") || strings.HasPrefix(userId, "admin-")

			for i := 0; i < 500; i++ {
				user := rbac.GetCurrentUser(ctx)
				if user == nil || user.GetId() != userId {
					t.Errorf("request for %s was answered about %v", userId, user)
					return
				}

				if got := containsField(rbac.GetReadableFields(ctx, entity), "ssn"); got != expectSSN {
					t.Errorf("request for %s: readable ssn = %v, want %v", userId, got, expectSSN)
					return
				}
			}
		}(r, userId)
	}
	wg.Wait()
}

// A handler that fans out drives the same request context from several goroutines. The
// memo is one slot shared by all of them and must be safe. Run with -race.
func TestOneRequestIsSafeToResolveFromSeveralGoroutines(t *testing.T) {
	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)
	entity := concurrencyTestEntity()

	ctx, _ := memoTestRequest("session-1", "hr-1")

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				if user := rbac.GetCurrentUser(ctx); user == nil || user.GetId() != "hr-1" {
					t.Errorf("fanned-out call was answered about %v", user)
					return
				}
				_ = rbac.GetReadableFields(ctx, entity)
			}
		}()
	}
	wg.Wait()
}

// The identity can legitimately change in the middle of a request: Login rotates the
// session onto a new identifier and republishes it into the request scope. A memo taken
// before that must not survive it - answering the rest of the request about the
// pre-login principal is an authorization bug, and a worse one than the repeated lookup
// the memo exists to avoid.
func TestMemoDoesNotSurviveALoginWithinOneRequest(t *testing.T) {
	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)

	ctx, carrier := memoTestRequest("session-anonymous", "")

	if user := rbac.GetCurrentUser(ctx); user != nil {
		t.Fatalf("pre-login resolution found user %s", user.GetId())
	}
	if !rbac.IsGuest(ctx) {
		t.Fatal("pre-login request was not treated as a guest")
	}

	// What Login plus http.PublishSession leave behind: a different session identifier
	// carrying the authenticated user id.
	carrier.setSession(&memoTestSession{id: "session-rotated", userId: "hr-1"})

	user := rbac.GetCurrentUser(ctx)
	if user == nil || user.GetId() != "hr-1" {
		t.Fatalf("after login the request is still answered about %v", user)
	}
	if rbac.IsGuest(ctx) {
		t.Fatal("after login the request is still treated as a guest")
	}
}

// The same session object re-pointed at another user is also a change of principal, even
// though the identifier did not move.
func TestMemoDoesNotSurviveTheSessionChangingUser(t *testing.T) {
	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)

	session := &memoTestSession{id: "session-1", userId: "employee-1"}
	carrier := &memoTestMessageContext{session: session}
	ctx := context.WithValue(context.Background(), core.MessageContextKey, carrier)

	if user := rbac.GetCurrentUser(ctx); user == nil || user.GetId() != "employee-1" {
		t.Fatalf("first resolution answered about %v", user)
	}

	session.SetUserId("hr-1")

	if user := rbac.GetCurrentUser(ctx); user == nil || user.GetId() != "hr-1" {
		t.Fatalf("after the session changed user the request is answered about %v", user)
	}
}

// ClearUserCache is exported and is the escape hatch for a request whose identity changed
// in a way the memo key cannot see. It must force the next resolution.
func TestClearUserCacheForcesReResolution(t *testing.T) {
	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)

	ctx, _ := memoTestRequest("session-1", "hr-1")

	_ = rbac.GetCurrentUser(ctx)
	_ = rbac.GetCurrentUser(ctx)
	if got := authContext.count(); got != 1 {
		t.Fatalf("two resolutions within one request cost %d lookups; want 1", got)
	}

	rbac.ClearUserCache(ctx)

	if user := rbac.GetCurrentUser(ctx); user == nil || user.GetId() != "hr-1" {
		t.Fatalf("resolution after ClearUserCache answered about %v", user)
	}
	if got := authContext.count(); got != 2 {
		t.Fatalf("ClearUserCache did not force a re-resolution: %d lookups, want 2", got)
	}
}

// CLI commands, background jobs and plain unit tests hand RBAC a context with no
// per-request carrier on it at all. That must resolve every time rather than fail.
func TestIdentityResolvesWithoutAMessageContext(t *testing.T) {
	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if !rbac.IsGuest(ctx) {
			t.Fatal("a context with no carrier and no session is not a guest")
		}
	}
	if got := authContext.count(); got != 3 {
		t.Fatalf("a context with no carrier memoised somewhere: %d resolutions, want 3", got)
	}

	// And the identity itself still arrives, through whatever the strategy resolves it
	// from - here the mock's own context value, which is what the rest of the suite uses.
	mockRbac := NewRoleBasedAccessControl(concurrencyTestConfig(), &MockAuthContext{})
	mockCtx := concurrencyTestContext(7)
	if user := mockRbac.GetCurrentUser(mockCtx); user == nil || user.GetId() != "user-7" {
		t.Fatalf("carrier-less context resolved %v", user)
	}
	if mockRbac.IsGuest(mockCtx) {
		t.Fatal("carrier-less authenticated context reported as guest")
	}
}

// The memo lives for one request and dies with it. Nothing prunes RBAC state on the
// normal path, so a memo that outlived its request would pin the identity of every
// request the process ever served - the leak that was removed, in a new shape.
func TestMemoisedIdentitiesDoNotOutliveTheirRequests(t *testing.T) {
	const requests = 5000

	authContext := &countingAuthContext{}
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), authContext)
	entity := concurrencyTestEntity()

	// An application holds its access control object for the process lifetime; if this one
	// became garbage first, anything it retained would be collected with it and the test
	// would prove nothing.
	defer runtime.KeepAlive(rbac)

	var finalized int64
	drive := func() {
		for i := 0; i < requests; i++ {
			session := &memoTestSession{id: fmt.Sprintf("session-%d", i), userId: fmt.Sprintf("employee-%d", i)}
			carrier := &memoTestMessageContext{session: session}
			runtime.SetFinalizer(carrier, func(*memoTestMessageContext) { atomic.AddInt64(&finalized, 1) })

			ctx := context.WithValue(context.Background(), core.MessageContextKey, carrier)
			_ = rbac.IsGuest(ctx)
			_ = rbac.GetUserRoles(ctx)
			_ = rbac.GetReadableFields(ctx, entity)
			_ = rbac.CanAccessEntity(ctx, entity, "read")
		}
	}
	drive()

	const wanted = requests - requests/10
	for i := 0; i < 40 && atomic.LoadInt64(&finalized) < wanted; i++ {
		runtime.GC()
		time.Sleep(25 * time.Millisecond)
	}

	if got := atomic.LoadInt64(&finalized); got < wanted {
		t.Fatalf("only %d of %d per-request carriers became collectable; the memo is outliving its request", got, requests)
	}
}

func containsField(fields []string, want string) bool {
	for _, field := range fields {
		if strings.EqualFold(field, want) {
			return true
		}
	}
	return false
}
