package model

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// concurrencyTestConfig mirrors the shape of a realistic field-level configuration:
// a couple of role-gated fields plus domain operations, so the validators below
// actually walk the role checks instead of short-circuiting.
func concurrencyTestConfig() *AccessControlConfig {
	return NewAccessControlBuilder().
		SetDefaultRoles("employee").
		AddDomainOperation("read", true, "employee", "manager", "hr", "admin").
		AddDomainOperation("read_owner", true, "employee").
		AddFieldAccess("salary", map[string]OperationConfig{
			"read": {Allowed: true, RequiredRoles: []string{"hr", "admin"}},
		}).
		AddFieldAccess("ssn", map[string]OperationConfig{
			"read": {Allowed: true, RequiredRoles: []string{"hr"}},
		}).
		AddFieldAccess("name", map[string]OperationConfig{
			"read":   {Allowed: true},
			"filter": {Allowed: true},
			"sort":   {Allowed: true},
		}).
		Build()
}

func concurrencyTestEntity() *Employee {
	return &Employee{
		ID:          "emp-shared",
		Name:        "Shared Employee",
		Email:       "shared@company.com",
		Department:  "engineering",
		ManagerID:   "mgr-1",
		Salary:      75000.0,
		SSN:         "123-45-6789",
		Performance: "Good",
		IsActive:    true,
	}
}

func concurrencyTestContext(i int) context.Context {
	user := &TestUser{
		ID:         fmt.Sprintf("user-%d", i),
		Username:   fmt.Sprintf("user-%d", i),
		Email:      fmt.Sprintf("user-%d@company.com", i),
		Roles:      []string{"employee"},
		Department: "engineering",
	}
	return context.WithValue(context.Background(), "current_user", user)
}

// A single RoleBasedAccessControl is shared by every request in an application -
// it is a configuration object, and the framework's own documented usage returns
// one from a service. Concurrent requests must therefore be able to drive it at
// the same time with their own per-request contexts.
func TestRoleBasedAccessControlIsSafeUnderConcurrentRequests(t *testing.T) {
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), &MockAuthContext{})
	entity := concurrencyTestEntity()

	const goroutines = 8
	const requestsPerGoroutine = 2500

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < requestsPerGoroutine; i++ {
				ctx := concurrencyTestContext(g*requestsPerGoroutine + i)

				_ = rbac.IsGuest(ctx)
				_ = rbac.GetUserRoles(ctx)
				_ = rbac.ValidateFieldAccess(ctx, "name", "read")
				_ = rbac.ValidateFilterAccess(ctx, "name", "=")
				_ = rbac.ValidateSortAccess(ctx, "name")
				_ = rbac.CanReadField(ctx, "salary", entity)
				_ = rbac.GetReadableFields(ctx, entity)
				_ = rbac.CanAccessEntity(ctx, entity, "read")
				_ = rbac.CanAccessEntityType(ctx, "read")
			}
		}(g)
	}
	wg.Wait()
}

// Nothing prunes RBAC state on the normal request path, so any per-request state
// kept on the instance is a leak for the lifetime of the process: it pins the
// context.Context and the authenticated user behind every request ever served.
// The users below must become collectable once their requests are over.
func TestRoleBasedAccessControlDoesNotRetainPerRequestState(t *testing.T) {
	const requests = 10000

	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), &MockAuthContext{})
	entity := concurrencyTestEntity()

	// The instance has to stay reachable while we measure. An application holds its
	// access control object for the process lifetime; if we let this one become
	// garbage first, anything it retained is collected along with it and the test
	// would pass without proving a thing.
	defer runtime.KeepAlive(rbac)

	var finalized int64
	drive := func() {
		for i := 0; i < requests; i++ {
			user := &TestUser{
				ID:       fmt.Sprintf("user-%d", i),
				Username: fmt.Sprintf("user-%d", i),
				Roles:    []string{"employee"},
			}
			runtime.SetFinalizer(user, func(*TestUser) { atomic.AddInt64(&finalized, 1) })

			ctx := context.WithValue(context.Background(), "current_user", user)
			_ = rbac.IsGuest(ctx)
			_ = rbac.GetUserRoles(ctx)
			_ = rbac.CanAccessEntity(ctx, entity, "read")
		}
	}
	drive()

	// The most recent request may still be reachable from the stack, so allow a
	// little slack rather than demanding every single user be collected.
	const wanted = requests - requests/10
	for i := 0; i < 40 && atomic.LoadInt64(&finalized) < wanted; i++ {
		runtime.GC()
		time.Sleep(25 * time.Millisecond)
	}

	if got := atomic.LoadInt64(&finalized); got < wanted {
		t.Fatalf("only %d of %d per-request users became collectable; RBAC is retaining request state across requests", got, requests)
	}
}

// Guards the lifetime decision itself: a process-lifetime map keyed by the
// per-request context.Context can never be hit twice, so it is only a leak and a
// race waiting to happen. Fail loudly if one comes back.
//
// Deliberately wider than the map it started as. Reaching for a sync.Map to make the
// concurrent writes safe would answer the race and leave the leak untouched - the entries
// still accumulate one per request for the life of the process, and the key is still
// unique to a request so none of them is ever read a second time. Any lookup table on this
// instance is that same mistake, whatever type it wears; per-request memoisation belongs
// on the request, which is where resolveUserContext puts it.
func TestRoleBasedAccessControlKeepsNoContextKeyedState(t *testing.T) {
	rbacType := reflect.TypeOf(RoleBasedAccessControl{})
	syncMapType := reflect.TypeOf(sync.Map{})

	for i := 0; i < rbacType.NumField(); i++ {
		field := rbacType.Field(i)

		fieldType := field.Type
		if fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}

		if fieldType.Kind() == reflect.Map || fieldType == syncMapType {
			t.Fatalf("RoleBasedAccessControl.%s is a %s: this object is shared by every request and configuration-only, so it must hold no lookup table of per-request state", field.Name, field.Type)
		}
	}
}

// ClearUserCache and ClearAllUserCache are exported, so they must stay callable
// and must not disturb subsequent decisions, whatever state the instance keeps.
func TestClearUserCacheIsCallableAndHarmless(t *testing.T) {
	rbac := NewRoleBasedAccessControl(concurrencyTestConfig(), &MockAuthContext{})
	ctx := concurrencyTestContext(1)

	before := rbac.GetUserRoles(ctx)
	rbac.ClearUserCache(ctx)
	rbac.ClearAllUserCache()
	after := rbac.GetUserRoles(ctx)

	if !reflect.DeepEqual(before, after) {
		t.Fatalf("roles changed across cache clears: %v then %v", before, after)
	}
	if rbac.IsGuest(ctx) {
		t.Fatal("authenticated context reported as guest after cache clears")
	}
}
