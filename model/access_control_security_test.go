package model

import (
	"context"
	"errors"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SEC-H06, SEC-H07 and SEC-H09. Three defects with one shape: a decision that granted more
// than the configuration said, because two different situations were being given one answer.
//
// H06 — "nobody is logged in" and "we could not find out" were the same value, so a session
// store that blinked served every caller as a guest; and an admitted guest was exempted from
// RequiredRoles entirely, so a rule requiring "admin" let anonymous visitors through while
// correctly refusing authenticated non-admins.
// H07 — GetAccessibleEntitiesWithFields returned its input unfiltered.
// H09 — the owner and access-level rules read `Allowed` and ignored `RequiredRoles`.

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

// answeringStrategy answers CurrentUser however the test says.
type answeringStrategy struct {
	user core.Authenticable
	err  error
}

func (s *answeringStrategy) CurrentUser(context.Context) (core.Authenticable, error) {
	return s.user, s.err
}
func (s *answeringStrategy) NewSessionWithoutUser(context.Context) (core.ISession, error) {
	return nil, nil
}
func (s *answeringStrategy) Login(core.Authenticable, context.Context) (core.ISession, error) {
	return nil, nil
}
func (s *answeringStrategy) IsLoggedIn(context.Context) bool                { return s.user != nil }
func (s *answeringStrategy) Logout(context.Context) error                   { return nil }
func (s *answeringStrategy) ResolveSessionId(context.Context) string        { return "" }
func (s *answeringStrategy) IsRequestMadeWithStrategy(context.Context) bool { return true }
func (s *answeringStrategy) CurrentSession(context.Context) core.ISession   { return nil }
func (s *answeringStrategy) ShouldRotateSession(core.ISession) bool         { return false }
func (s *answeringStrategy) RotateSession(_ context.Context, old core.ISession) (core.ISession, error) {
	return old, nil
}

type answeringAuthContext struct{ strategy core.IAuthStrategy }

func (a *answeringAuthContext) RegisterAuthStrategy(string, core.IAuthStrategy) {}
func (a *answeringAuthContext) GetAuthStrategy(...string) core.IAuthStrategy    { return a.strategy }
func (a *answeringAuthContext) Strategy(...string) core.IAuthStrategy           { return a.strategy }
func (a *answeringAuthContext) ResolveAuthStrategyByContext(context.Context) core.IAuthStrategy {
	return a.strategy
}

// rbacFor builds an access control whose caller is whatever the test supplies.
func rbacFor(config *AccessControlConfig, user core.Authenticable, err error) *RoleBasedAccessControl {
	return NewRoleBasedAccessControl(config,
		&answeringAuthContext{strategy: &answeringStrategy{user: user, err: err}})
}

// ownedDoc is an entity that reports ownership and an access level, which is what selects the
// `_owner` and `_<level>` rules.
type ownedDoc struct {
	id          string
	ownerId     string
	accessLevel string
}

func (d *ownedDoc) GetId() string { return d.id }

func (d *ownedDoc) GetAccessibleFields(_ context.Context, _ core.Authenticable, _ string) []string {
	return []string{"title", "secret"}
}

func (d *ownedDoc) CanAccessField(_ context.Context, _ core.Authenticable, _ string, _ string) bool {
	return true
}

func (d *ownedDoc) GetOwnershipInfo(_ context.Context, user core.Authenticable) core.OwnershipInfo {
	return core.OwnershipInfo{
		IsOwner:     user != nil && user.GetId() == d.ownerId,
		OwnerId:     d.ownerId,
		AccessLevel: d.accessLevel,
	}
}

// ---------------------------------------------------------------------------
// SEC-H06
// ---------------------------------------------------------------------------

// TestIdentityResolutionFailureIsNotAGuest is the headline. Every surface must refuse, and
// AllowGuestAccess must not rescue the caller: the guest role is a different privilege set,
// so demoting an unknown caller into it grants as well as removes.
func TestIdentityResolutionFailureIsNotAGuest(t *testing.T) {
	config := NewAccessControlConfig()
	config.AllowGuestAccess = true
	config.DomainOperations = map[string]OperationConfig{
		"read": {Allowed: true, RequiredRoles: []string{GuestRole}},
	}

	rbac := rbacFor(config, nil, errors.New("session store unreachable"))
	ctx := context.Background()

	assert.False(t, rbac.CanAccessEntity(ctx, &ownedDoc{id: "d1"}, "read"))
	assert.False(t, rbac.CanAccessEntityType(ctx, "read"))
	assert.False(t, rbac.CanReadField(ctx, "title", nil))
	assert.Empty(t, rbac.GetReadableFields(ctx, nil))
	assert.Empty(t, rbac.GetReadableFieldsForCollection(ctx, nil))
	assert.Empty(t, rbac.GetInheritedFieldPermissions(ctx, "read"))
	assert.False(t, rbac.CanUseDBLevelRBAC(ctx, "read"))

	assert.ErrorIs(t, rbac.ValidateFieldAccess(ctx, "title", "read"), ErrIdentityUnresolved)
	assert.ErrorIs(t, rbac.ValidateFilterAccess(ctx, "title", "="), ErrIdentityUnresolved)
	assert.ErrorIs(t, rbac.ValidateSortAccess(ctx, "title"), ErrIdentityUnresolved)
}

// TestAnEstablishedGuestIsStillServedWhenTheConfigurationSaysSo — the over-blocking fence.
// The refusal must be about not knowing, not about being anonymous.
func TestAnEstablishedGuestIsStillServedWhenTheConfigurationSaysSo(t *testing.T) {
	config := NewAccessControlConfig()
	config.AllowGuestAccess = true
	config.DomainOperations = map[string]OperationConfig{
		"read": {Allowed: true, RequiredRoles: []string{GuestRole}},
	}

	rbac := rbacFor(config, nil, nil) // (nil, nil) is "there is no session", not a failure
	assert.True(t, rbac.CanAccessEntityType(context.Background(), "read"))
}

// TestIdentityResolutionFailurePropagatesToTheCaller. A handler needs to answer 503 rather
// than 403, and the bool surfaces cannot say which it is.
func TestIdentityResolutionFailurePropagatesToTheCaller(t *testing.T) {
	underlying := errors.New("session store unreachable")
	rbac := rbacFor(NewAccessControlConfig(), nil, underlying)

	var reporter IdentityResolutionReporter = rbac
	err := reporter.IdentityResolutionError(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIdentityUnresolved)
	assert.ErrorContains(t, err, underlying.Error())

	anonymous := rbacFor(NewAccessControlConfig(), nil, nil)
	assert.NoError(t, anonymous.IdentityResolutionError(context.Background()),
		"a caller established to be anonymous is not a failure")
}

// TestGuestIsNotExemptFromRequiredRoles. The inversion: an anonymous visitor got in where an
// authenticated non-admin was refused.
func TestGuestIsNotExemptFromRequiredRoles(t *testing.T) {
	config := NewAccessControlConfig()
	config.AllowGuestAccess = true
	config.DomainOperations = map[string]OperationConfig{
		"read": {Allowed: true, RequiredRoles: []string{"admin"}},
	}

	guest := rbacFor(config, nil, nil)
	ctx := context.Background()

	assert.False(t, guest.CanAccessEntity(ctx, &ownedDoc{id: "d1"}, "read"),
		"an anonymous caller must not satisfy a rule requiring admin")
	assert.False(t, guest.CanAccessEntityType(ctx, "read"),
		"and the type-level check must agree with the entity-level one")

	// The same policy, an authenticated non-admin: already refused before the fix, and the
	// point is that the guest is now refused *the same way*.
	member := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"member"}}, nil)
	assert.False(t, member.CanAccessEntityType(ctx, "read"))
}

// TestGuestListedInRequiredRolesStillPasses pins the migration path: a configuration that
// means to admit guests says so, and the guest role is already on the caller.
func TestGuestListedInRequiredRolesStillPasses(t *testing.T) {
	config := NewAccessControlConfig()
	config.AllowGuestAccess = true
	config.DomainOperations = map[string]OperationConfig{
		"read": {Allowed: true, RequiredRoles: []string{"admin", GuestRole}},
	}

	assert.True(t, rbacFor(config, nil, nil).CanAccessEntityType(context.Background(), "read"))
}

// TestDBFiltersForAGuestDoNotBorrowAnotherRolesFilters. The scan looked for the *shape* of a
// full-access filter across every role's bucket, discarding the role key — so `1 = 1` written
// for admins was adopted by anonymous callers.
func TestDBFiltersForAGuestDoNotBorrowAnotherRolesFilters(t *testing.T) {
	config := NewAccessControlConfig()
	config.AllowGuestAccess = true
	config.DBRBAC = &DBRBACConfig{
		EnableDBLevelRBAC: true,
		CustomFilters: map[string][]DBFilter{
			"admin": {{Field: "1", Operator: "=", Value: 1, Logic: "AND"}},
		},
	}

	guest := rbacFor(config, nil, nil)
	ctx := context.Background()

	assert.False(t, guest.CanUseDBLevelRBAC(ctx, "read"),
		"a guest with no filters of its own cannot use DB-level RBAC")

	_, err := guest.GenerateDBFilters(ctx, "read")
	assert.Error(t, err, "and asking anyway must not produce another role's full-access filter")
}

// TestDBFiltersRefuseWhenOwnershipCannotBeApplied. Omitting the ownership predicate turns
// "only your own rows" into "every row".
func TestDBFiltersRefuseWhenOwnershipCannotBeApplied(t *testing.T) {
	config := NewAccessControlConfig()
	config.AllowGuestAccess = true
	config.DBRBAC = &DBRBACConfig{
		EnableDBLevelRBAC: true,
		OwnershipField:    "owner_id",
		CustomFilters:     map[string][]DBFilter{GuestRole: {{Field: "published", Operator: "=", Value: true}}},
	}

	_, err := rbacFor(config, nil, nil).GenerateDBFilters(context.Background(), "read")
	require.Error(t, err)
	assert.ErrorContains(t, err, "owner_id")
}

// ---------------------------------------------------------------------------
// SEC-H07
// ---------------------------------------------------------------------------

func collectionConfig() *AccessControlConfig {
	config := NewAccessControlConfig()
	config.DomainOperations = map[string]OperationConfig{
		"read":       {Allowed: false},
		"read_owner": {Allowed: true},
	}
	return config
}

// TestGetAccessibleEntitiesWithFieldsFiltersPerEntity is the headline for SEC-H07.
func TestGetAccessibleEntitiesWithFieldsFiltersPerEntity(t *testing.T) {
	user := &TestUser{ID: "u1", Roles: []string{"member"}}
	rbac := rbacFor(collectionConfig(), user, nil)

	entities := []any{
		&ownedDoc{id: "mine", ownerId: "u1"},
		&ownedDoc{id: "theirs", ownerId: "u2"},
		&ownedDoc{id: "also-theirs", ownerId: "u3"},
	}

	accessible, _ := rbac.GetAccessibleEntitiesWithFields(context.Background(), entities, "read")

	require.Len(t, accessible, 1, "only the caller's own document is readable")
	assert.Equal(t, "mine", accessible[0].(*ownedDoc).id)
}

// TestGetAccessibleEntitiesWithFieldsAgreesWithGetAccessibleEntities is the anti-drift
// assertion: two exported methods over one collection must not apply different policies.
func TestGetAccessibleEntitiesWithFieldsAgreesWithGetAccessibleEntities(t *testing.T) {
	entities := []any{
		&ownedDoc{id: "mine", ownerId: "u1"},
		&ownedDoc{id: "theirs", ownerId: "u2"},
		&ownedDoc{id: "elevated", ownerId: "u2", accessLevel: "reviewer"},
	}

	configs := map[string]*AccessControlConfig{
		"owner only": collectionConfig(),
		"owner or reviewer": func() *AccessControlConfig {
			config := collectionConfig()
			config.DomainOperations["read_reviewer"] = OperationConfig{Allowed: true}
			return config
		}(),
		"everybody": func() *AccessControlConfig {
			config := collectionConfig()
			config.DomainOperations["read"] = OperationConfig{Allowed: true}
			return config
		}(),
	}

	for name, config := range configs {
		t.Run(name, func(t *testing.T) {
			rbac := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"member"}}, nil)
			ctx := context.Background()

			plain := rbac.GetAccessibleEntities(ctx, entities, "read")
			withFields, _ := rbac.GetAccessibleEntitiesWithFields(ctx, entities, "read")

			assert.Equal(t, len(plain), len(withFields),
				"the two collection helpers must return the same rows")
			for i := range plain {
				assert.Same(t, plain[i], withFields[i])
			}
		})
	}
}

// TestGetAccessibleEntitiesWithFieldsGrantsAnOwnerWhoseOnlyRuleIsReadOwner is why the
// type-level pre-check had to go rather than being kept as a short-circuit: it knows nothing
// about the `_owner` rule, so it would have refused before the loop ran.
func TestGetAccessibleEntitiesWithFieldsGrantsAnOwnerWhoseOnlyRuleIsReadOwner(t *testing.T) {
	rbac := rbacFor(collectionConfig(), &TestUser{ID: "u1", Roles: []string{"member"}}, nil)

	accessible, _ := rbac.GetAccessibleEntitiesWithFields(
		context.Background(), []any{&ownedDoc{id: "mine", ownerId: "u1"}}, "read")

	require.Len(t, accessible, 1,
		"canAccessEntityType knows nothing about read_owner, so keeping it as a pre-check "+
			"would have refused the owner before the per-entity loop ran")
}

// TestGetReadableFieldsDeniesAGuestWhenGuestAccessIsOff. The entity's own field logic was
// called with a nil user for a caller the configuration refuses outright.
func TestGetReadableFieldsDeniesAGuestWhenGuestAccessIsOff(t *testing.T) {
	config := NewAccessControlConfig()
	config.AllowGuestAccess = false

	rbac := rbacFor(config, nil, nil)
	ctx := context.Background()
	entity := &ownedDoc{id: "d1"}

	assert.Empty(t, rbac.GetReadableFields(ctx, entity),
		"the global guest denial applies before the entity is asked")
	assert.Empty(t, rbac.GetReadableFieldsForCollection(ctx, entity))
	assert.Empty(t, rbac.GetInheritedFieldPermissions(ctx, "read"))
}

// ---------------------------------------------------------------------------
// SEC-H09
// ---------------------------------------------------------------------------

// TestAccessLevelRuleEnforcesRequiredRoles is the real bypass. AccessLevel is a free-form
// string the application's entity returns, concatenated into a rule name — so an entity
// reporting "admin" selected `read_admin` and was granted whatever roles that rule declared.
func TestAccessLevelRuleEnforcesRequiredRoles(t *testing.T) {
	config := NewAccessControlConfig()
	config.DomainOperations = map[string]OperationConfig{
		"read":       {Allowed: false},
		"read_admin": {Allowed: true, RequiredRoles: []string{"admin"}},
	}

	entity := &ownedDoc{id: "d1", ownerId: "someone", accessLevel: "admin"}

	nonAdmin := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"member"}}, nil)
	assert.False(t, nonAdmin.CanAccessEntity(context.Background(), entity, "read"),
		"the rule requires admin, and this caller is not one")

	admin := rbacFor(config, &TestUser{ID: "u2", Roles: []string{"admin"}}, nil)
	assert.True(t, admin.CanAccessEntity(context.Background(), entity, "read"))
}

// TestOwnerRuleEnforcesRequiredRoles — the same defect on the ownership branch.
func TestOwnerRuleEnforcesRequiredRoles(t *testing.T) {
	config := NewAccessControlConfig()
	config.DomainOperations = map[string]OperationConfig{
		"read":       {Allowed: false},
		"read_owner": {Allowed: true, RequiredRoles: []string{"verified"}},
	}

	entity := &ownedDoc{id: "d1", ownerId: "u1"}

	unverified := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"member"}}, nil)
	assert.False(t, unverified.CanAccessEntity(context.Background(), entity, "read"),
		"owning it does not exempt the caller from the role the rule declares")

	verified := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"verified"}}, nil)
	assert.True(t, verified.CanAccessEntity(context.Background(), entity, "read"))
}

// TestAnAccessLevelOutsideTheAllowlistIsRefused. Which rule an entity may select has to be
// bounded by configuration, not by whatever the application's method returns.
func TestAnAccessLevelOutsideTheAllowlistIsRefused(t *testing.T) {
	config := NewAccessControlConfig()
	config.AllowedAccessLevels = []string{"reviewer"}
	config.DomainOperations = map[string]OperationConfig{
		"read":       {Allowed: false},
		"read_admin": {Allowed: true},
	}

	rbac := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"member"}}, nil)
	entity := &ownedDoc{id: "d1", accessLevel: "admin"}

	assert.False(t, rbac.CanAccessEntity(context.Background(), entity, "read"))
}

// TestOwnershipCanDeny is the new capability that makes the mechanism bounded: before, every
// rule was grant-only and first-match-wins, so an ownership rule could only widen.
func TestOwnershipCanDeny(t *testing.T) {
	config := NewAccessControlConfig()
	config.DomainOperations = map[string]OperationConfig{
		"read":          {Allowed: true},
		"read_viewer":   {Deny: true},
		"read_reviewer": {Allowed: true},
	}

	rbac := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"member"}}, nil)
	ctx := context.Background()

	assert.False(t, rbac.CanAccessEntity(ctx, &ownedDoc{id: "d1", accessLevel: "viewer"}, "read"),
		"a denying rule ends the decision even though the general rule allows")
	assert.True(t, rbac.CanAccessEntity(ctx, &ownedDoc{id: "d2", accessLevel: "reviewer"}, "read"))
}

// TestOwnershipFieldRestrictsFieldRead. AccessControlConfig.OwnershipField was written by the
// builder and read by nothing that could refuse: both arms of the check returned true.
func TestOwnershipFieldRestrictsFieldRead(t *testing.T) {
	config := NewAccessControlConfig()
	config.OwnershipField = "owner_id"
	config.FieldAccess = map[string]FieldAccessConfig{
		"salary": {Operations: map[string]OperationConfig{
			"read":  {Allowed: true},
			"owner": {Allowed: true},
		}},
	}

	entity := &ownerProvidingDoc{ownerId: "u1"}

	owner := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"member"}}, nil)
	assert.True(t, owner.CanReadField(context.Background(), "salary", entity))

	other := rbacFor(config, &TestUser{ID: "u2", Roles: []string{"member"}}, nil)
	assert.False(t, other.CanReadField(context.Background(), "salary", entity),
		"a field that declares an owner operation is owner-restricted, not owner-bonused")
}

// TestAFieldWithNoOwnerOperationIsUnaffectedByOwnershipField is the over-blocking fence.
func TestAFieldWithNoOwnerOperationIsUnaffectedByOwnershipField(t *testing.T) {
	config := NewAccessControlConfig()
	config.OwnershipField = "owner_id"
	config.FieldAccess = map[string]FieldAccessConfig{
		"title": {Operations: map[string]OperationConfig{"read": {Allowed: true}}},
	}

	other := rbacFor(config, &TestUser{ID: "u2", Roles: []string{"member"}}, nil)
	assert.True(t, other.CanReadField(context.Background(), "title",
		&ownerProvidingDoc{ownerId: "u1"}))
}

// TestOwnershipIsNotInferredFromMatchingIds. The removed fallback treated an entity whose own
// id equalled the caller's as owned by them — a grant on a coincidence, for any entity type
// sharing an id space with users.
func TestOwnershipIsNotInferredFromMatchingIds(t *testing.T) {
	config := NewAccessControlConfig()
	config.OwnershipField = "owner_id"
	config.FieldAccess = map[string]FieldAccessConfig{
		"salary": {Operations: map[string]OperationConfig{
			"read":  {Allowed: true},
			"owner": {Allowed: true},
		}},
	}

	// An entity that expresses ownership neither way, whose id happens to equal the user's.
	entity := &plainDoc{id: "7"}
	rbac := rbacFor(config, &TestUser{ID: "7", Roles: []string{"member"}}, nil)

	assert.False(t, rbac.CanReadField(context.Background(), "salary", entity))
}

// ---------------------------------------------------------------------------
// SEC-M11
// ---------------------------------------------------------------------------

// TestConfiguredComplexityCapsAreEnforced. MaxFilters and MaxSorts were configuration nothing
// read.
func TestConfiguredComplexityCapsAreEnforced(t *testing.T) {
	config := NewAccessControlConfig()
	config.MaxFilters = 2
	config.MaxSorts = 1
	config.DomainOperations = map[string]OperationConfig{
		"read": {Allowed: true}, "filter": {Allowed: true}, "sort": {Allowed: true},
	}

	rbac := rbacFor(config, &TestUser{ID: "u1", Roles: []string{"member"}}, nil)

	assert.NoError(t, rbac.CheckComplexity(2, 1), "at the limit is allowed")
	assert.Error(t, rbac.CheckComplexity(3, 1), "above the filter limit is refused")
	assert.Error(t, rbac.CheckComplexity(2, 2), "above the sort limit is refused")

	_, err := NewPaginationParamsWithAccess(1, 10,
		[]SortParam{{Field: "a"}, {Field: "b"}},
		[]Filter{{Field: "a", Operator: "="}}, rbac, context.Background())
	assert.Error(t, err, "the cap is applied before the filters are iterated")
}

// TestAnUnsetComplexityCapMeansUnbounded — the zero value of an int config field must not turn
// every request into a refusal.
func TestAnUnsetComplexityCapMeansUnbounded(t *testing.T) {
	config := NewAccessControlConfig()
	config.MaxFilters = 0
	config.MaxSorts = 0

	rbac := rbacFor(config, &TestUser{ID: "u1"}, nil)
	assert.NoError(t, rbac.CheckComplexity(100, 100))
}

// ---------------------------------------------------------------------------
// Entities used above
// ---------------------------------------------------------------------------

// ownerProvidingDoc expresses ownership through core.OwnerProvider.
type ownerProvidingDoc struct{ ownerId string }

func (d *ownerProvidingDoc) GetOwnerId() string { return d.ownerId }

// plainDoc expresses ownership neither way.
type plainDoc struct{ id string }

func (d *plainDoc) GetId() string { return d.id }
