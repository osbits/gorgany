package model

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	"github.com/osbits/gorgany/v2/log"
)

// DBTestUser is used for testing DB RBAC functionality
type DBTestUser struct {
	ID         string   `json:"id"`
	Username   string   `json:"username"`
	Email      string   `json:"email"`
	Roles      []string `json:"roles"`
	Department string   `json:"department"`
	TenantID   string   `json:"tenant_id"`
}

func (u *DBTestUser) GetId() string {
	return u.ID
}

func (u *DBTestUser) GetUsername() string {
	return u.Username
}

func (u *DBTestUser) GetPassword() string {
	return "hashed_password"
}

func (u *DBTestUser) GetRole() core.UserRole {
	if len(u.Roles) > 0 {
		return core.UserRole(u.Roles[0])
	}
	return core.UserRole("user")
}

func (u *DBTestUser) GetRoles() []string {
	return u.Roles
}

// AccessControl defines the interface for implementing custom access control rules
type AccessControl interface {
	// ValidateFieldAccess validates if a user can access a specific field
	ValidateFieldAccess(ctx context.Context, field string, operation string) error

	// ValidateFilterAccess validates if a user can use a specific filter
	ValidateFilterAccess(ctx context.Context, field string, operator string) error

	// ValidateSortAccess validates if a user can sort by a specific field
	ValidateSortAccess(ctx context.Context, field string) error

	// GetUserRoles returns the roles for the current user
	GetUserRoles(ctx context.Context) []string

	// IsGuest checks if the current user is a guest
	IsGuest(ctx context.Context) bool

	// GetCurrentUser returns the current user from context
	GetCurrentUser(ctx context.Context) core.Authenticable

	// CanReadField checks if the current user can read a specific field
	CanReadField(ctx context.Context, field string, entity any) bool

	// GetReadableFields returns the list of fields the current user can read
	GetReadableFields(ctx context.Context, entity any) []string

	// CanAccessEntity checks if the current user can access the domain for a specific operation
	CanAccessEntity(ctx context.Context, entity any, operation string) bool

	// GetAccessibleEntities filters a list of entities based on access permissions
	GetAccessibleEntities(ctx context.Context, entities []any, operation string) []any

	// GetReadableFieldsForCollection returns fields readable for a collection (optimized for collections)
	GetReadableFieldsForCollection(ctx context.Context, entityType any) []string

	// GetAccessibleEntitiesWithFields filters entities and returns optimized field list for collections
	GetAccessibleEntitiesWithFields(ctx context.Context, entities []any, operation string) ([]any, []string)

	// CanAccessEntityType checks if user can access the entity type (simplified check)
	CanAccessEntityType(ctx context.Context, operation string) bool

	// GetInheritedFieldPermissions returns field permissions that inherit from entity roles
	GetInheritedFieldPermissions(ctx context.Context, operation string) []string

	// GenerateDBFilters generates database-level filters for RBAC
	GenerateDBFilters(ctx context.Context, operation string) ([]DBFilter, error)

	// CanUseDBLevelRBAC checks if RBAC can be handled at DB level
	CanUseDBLevelRBAC(ctx context.Context, operation string) bool
}

// AccessControlGeneric defines the generic interface for type-safe access control
// Note: This interface is kept for compatibility but the actual generic functions are standalone
type AccessControlGeneric[T any] interface {
	AccessControl
}

// UserContextCache holds the identity and roles resolved for one request, so that the
// dozens of access control questions a response asks do not each have to go and ask the
// auth strategy who the caller is.
//
// Treat it as immutable once resolved. Several goroutines of a fanned-out request can be
// handed the same snapshot, and it is only safe to share because nothing writes to it
// after resolveUserContext returns.
type UserContextCache struct {
	user  core.Authenticable
	roles []string
	state identityState

	// resolveErr says why resolution failed. Non-nil exactly when state is
	// identityUnresolved.
	resolveErr error
}

// identityState is what is known about the caller, in three states rather than two.
//
// "Anonymous" and "we could not find out" used to be the same value, and collapsing them is
// the defect: the auth strategy deliberately distinguishes them — CurrentUser returns
// (nil, nil) for no session and (nil, err) for a store it could not reach, with a comment
// saying the two mean very different things — and access control threw the error away and
// called both a guest. A session store or user table that blinks therefore demoted every
// authenticated caller to the guest role rather than failing the request, and with
// AllowGuestAccess on, the guest role is a *different* privilege set, not a smaller one.
type identityState uint8

const (
	identityAuthenticated identityState = iota
	// identityAnonymous means we established the caller carries no identity.
	identityAnonymous
	// identityUnresolved means we asked and could not find out.
	identityUnresolved
)

// isGuest reports a caller positively established to carry no identity.
//
// Deliberately false for an unresolved caller: everything that grants on the strength of
// "this is a guest" — the guest role, the guest DB filters — must not fire for a caller we
// know nothing about.
func (c *UserContextCache) isGuest() bool { return c.state == identityAnonymous }

// unresolved reports that identity resolution failed.
func (c *UserContextCache) unresolved() bool { return c.state == identityUnresolved }

// isAuthenticated reports a caller whose identity was resolved to a user.
func (c *UserContextCache) isAuthenticated() bool { return c.state == identityAuthenticated }

// GuestRole is the role assigned to a caller established to be anonymous, when the
// configuration admits guests at all.
const GuestRole = "guest"

// ErrIdentityUnresolved reports that the auth strategy could not answer who the caller is.
//
// It is not "nobody is logged in". A caller mapping this onto a response wants 503, not 401
// and not 403: the request could not be decided, rather than decided against.
var ErrIdentityUnresolved = errors.New("access control: the caller's identity could not be resolved")

// IdentityResolutionReporter is implemented by access-control implementations that can tell
// "nobody is logged in" from "we could not find out".
//
// It is a separate interface rather than a method on AccessControl because AccessControl is
// published and applications implement it; adding a method would break every one of them. A
// handler that needs to answer 503 rather than 403 type-asserts for this.
type IdentityResolutionReporter interface {
	IdentityResolutionError(ctx context.Context) error
}

// IdentityResolutionError implements IdentityResolutionReporter.
func (rbac *RoleBasedAccessControl) IdentityResolutionError(ctx context.Context) error {
	return rbac.resolveUserContext(ctx).resolveErr
}

// admits reports whether this caller may be served at all, before any rule is consulted.
//
// Two separate refusals that used to be one. An unresolved caller is refused unconditionally;
// an anonymous one is refused unless the configuration admits anonymous callers.
// AllowGuestAccess does not rescue the unresolved case, and that is the point of separating
// them: it is a statement about callers we have established carry no identity, and we have
// established nothing.
func (rbac *RoleBasedAccessControl) admits(uc *UserContextCache) error {
	if uc.unresolved() {
		return uc.resolveErr
	}
	if uc.isGuest() && !rbac.config.AllowGuestAccess {
		return fmt.Errorf("guest access is not allowed")
	}
	return nil
}

// admitsBool is admits for the decision surfaces that return a bool. They cannot report why,
// so they fail closed — which is the safe answer whether or not the caller checks anything.
func (rbac *RoleBasedAccessControl) admitsBool(uc *UserContextCache) bool {
	return rbac.admits(uc) == nil
}

// DBFilter represents a database-level filter for RBAC.
//
// Field used to be documented as possibly containing raw SQL, with the caller's user id,
// username, role and other attributes substituted into it by string replacement — and Field is
// emitted into the query as SQL syntax. That is second-order SQL injection through the
// authorization predicate itself: a username of `x' OR 'a'='a` turned `author_name =
// '{{user_username}}'` into `author_name = 'x' OR 'a'='a'`, and because the WHERE clause is
// joined with bare ANDs and ORs, the injected OR neutralised every sibling condition too.
//
// So Field is a column reference and nothing else, raw SQL moves to RawSQL where its
// parameters are bound, and user context reaches SQL only as a bound value. The three are
// mutually exclusive; Validate enforces that.
type DBFilter struct {
	// Field is a column reference — "col" or "table.col" — validated as an identifier.
	Field    string      `json:"field"`
	Operator string      `json:"operator"`
	Value    interface{} `json:"value"`
	Logic    string      `json:"logic"` // "AND" or "OR"

	// RawSQL is a predicate the configuration author vouches for, emitted verbatim with
	// RawArgs bound as query parameters.
	//
	// Placeholders are deliberately NOT expanded into it. That is the whole difference from
	// the old Field: user context reaches a raw predicate only through RawArgs, where the
	// driver binds it, so there is no string for an attacker's value to become part of. For a
	// dynamic *identifier*, use dbCore.RawCondition's `?.id` / `?.?` convention rather than
	// interpolating one here.
	RawSQL  string        `json:"raw_sql,omitempty"`
	RawArgs []interface{} `json:"raw_args,omitempty"`

	// Processor names an entry in DBRBACConfig.CustomFilterProcessors.
	//
	// It replaces looking the processor up by Field, which forced callers to put a processor
	// name — not a column — into the column slot. See model/custom_rbac_processors.go.
	Processor string `json:"processor,omitempty"`

	// For complex queries
	Subquery *DBSubquery `json:"subquery,omitempty"`
	Join     *DBJoin     `json:"join,omitempty"`
}

// dbFilterOperators is the closed set of operators a filter may use.
//
// Closed, and checked before the query is built. The emitter used to switch on the operator
// with `default: return builder` — so an operator it did not recognise silently dropped the
// filter, and a dropped *restrictive* filter is not a restriction that fails, it is no
// restriction at all.
var dbFilterOperators = map[string]bool{
	"=": true, "!=": true, ">": true, ">=": true, "<": true, "<=": true,
	"like": true, "not like": true, "in": true, "not in": true,
}

// dbSubqueryOperators is the closed set for a subquery filter.
var dbSubqueryOperators = map[string]bool{
	"IN": true, "NOT IN": true, "EXISTS": true, "NOT EXISTS": true,
}

// dbJoinTypes is the closed set of join types.
var dbJoinTypes = map[string]bool{"INNER": true, "LEFT": true, "RIGHT": true, "FULL": true}

// unexpandedPlaceholder matches a placeholder that survived substitution.
//
// A leftover `{{user_dept}}` means the configuration referenced a user attribute that
// UserContextFields does not declare. Emitting it would put the literal text into the query;
// refusing says which key is missing.
var unexpandedPlaceholder = regexp.MustCompile(`\{\{[^}]*\}\}`)

// Validate reports whether this filter is safe to turn into SQL.
//
// It runs on every filter GenerateDBFilters is about to return and again in the emitter,
// because an application can build a []DBFilter by hand and hand it straight to
// ApplyDBFiltersToQueryBuilder.
func (f DBFilter) Validate(allowedOperators []string) error {
	slots := 0
	for _, occupied := range []bool{f.Field != "", f.RawSQL != "", f.Processor != ""} {
		if occupied {
			slots++
		}
	}
	if f.Subquery == nil && f.Join == nil && slots == 0 {
		return fmt.Errorf("filter names neither a field, a raw predicate nor a processor")
	}
	if slots > 1 {
		return fmt.Errorf(
			"filter sets more than one of field, raw_sql and processor; they are alternatives")
	}

	if f.Field != "" && !dbCore.IsSimpleIdentifier(f.Field) {
		return fmt.Errorf(
			"filter field %q is not a column reference; raw SQL belongs in raw_sql, where its "+
				"parameters are bound", f.Field)
	}
	if match := unexpandedPlaceholder.FindString(f.RawSQL); match != "" {
		return fmt.Errorf(
			"raw_sql still contains the placeholder %s; user context reaches a raw predicate "+
				"through raw_args, not by substitution", match)
	}

	if f.Logic != "" && !strings.EqualFold(f.Logic, "AND") && !strings.EqualFold(f.Logic, "OR") {
		return fmt.Errorf("filter logic %q is neither AND nor OR", f.Logic)
	}

	if f.Subquery != nil {
		return f.Subquery.validate(allowedOperators)
	}
	if f.Join != nil {
		if err := f.Join.validate(); err != nil {
			return err
		}
	}

	// A raw predicate carries its own operator inside the SQL; only a field comparison needs
	// one from the allowlist.
	if f.Field != "" {
		if !dbFilterOperators[strings.ToLower(f.Operator)] {
			return fmt.Errorf("filter operator %q is not one this builder will emit", f.Operator)
		}
		if len(allowedOperators) > 0 && !containsFold(allowedOperators, f.Operator) {
			return fmt.Errorf("filter operator %q is not in the configured allowlist", f.Operator)
		}
	}

	return nil
}

func (q *DBSubquery) validate(allowedOperators []string) error {
	if !dbSubqueryOperators[strings.ToUpper(q.Operator)] {
		return fmt.Errorf("subquery operator %q is not one this builder will emit", q.Operator)
	}
	if !dbCore.IsSimpleIdentifier(q.Table) {
		return fmt.Errorf("subquery table %q is not an identifier", q.Table)
	}
	if !dbCore.IsSimpleIdentifier(q.Select) {
		return fmt.Errorf("subquery select %q is not an identifier", q.Select)
	}
	for _, join := range q.Join {
		if err := join.validate(); err != nil {
			return err
		}
	}
	for _, where := range q.Where {
		if err := where.Validate(allowedOperators); err != nil {
			return fmt.Errorf("inside the subquery on %s: %w", q.Table, err)
		}
	}
	return nil
}

func (j DBJoin) validate() error {
	if !dbJoinTypes[strings.ToUpper(j.Type)] {
		return fmt.Errorf("join type %q is not one this builder will emit", j.Type)
	}
	for name, value := range map[string]string{
		"table": j.Table, "left_key": j.LeftKey, "right_key": j.RightKey,
	} {
		if !dbCore.IsSimpleIdentifier(value) {
			return fmt.Errorf("join %s %q is not an identifier", name, value)
		}
	}
	return nil
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

// DBSubquery represents a subquery for complex filtering
type DBSubquery struct {
	Table    string     `json:"table"`
	Select   string     `json:"select"`
	Where    []DBFilter `json:"where,omitempty"`
	Join     []DBJoin   `json:"join,omitempty"`
	Operator string     `json:"operator"` // "IN", "NOT IN", "EXISTS", "NOT EXISTS"
}

// DBJoin represents a join for complex queries
type DBJoin struct {
	Type      string `json:"type"` // "INNER", "LEFT", "RIGHT", "FULL"
	Table     string `json:"table"`
	LeftKey   string `json:"left_key"`
	RightKey  string `json:"right_key"`
	Condition string `json:"condition,omitempty"`
}

// DBRBACConfig defines configuration for DB-level RBAC
type DBRBACConfig struct {
	// EnableDBLevelRBAC enables database-level RBAC filtering
	EnableDBLevelRBAC bool `json:"enable_db_level_rbac,omitempty"`

	// OwnershipField specifies the field used for ownership-based filtering
	OwnershipField string `json:"ownership_field,omitempty"`

	// SkipOwnershipForRoles specifies roles that should skip ownership filtering
	// These roles will only use custom filters, not ownership-based filters
	SkipOwnershipForRoles []string `json:"skip_ownership_for_roles,omitempty"`

	// CustomFilters defines custom database filters for specific roles
	// Each role can have multiple filters that will be combined with OR logic
	CustomFilters map[string][]DBFilter `json:"custom_filters,omitempty"`

	// UserContextFields defines fields to extract from user context for filtering
	// Key is the field name, value is the placeholder template
	// These fields will be available as {{user_field}} in filter values
	UserContextFields map[string]string `json:"user_context_fields,omitempty"`

	// CustomFilterProcessors defines custom processors for complex filtering logic
	CustomFilterProcessors map[string]CustomFilterProcessor `json:"custom_filter_processors,omitempty"`
}

// CustomFilterProcessor defines a function that can process complex filters
type CustomFilterProcessor func(ctx context.Context, user core.Authenticable, filter DBFilter) ([]DBFilter, error)

// RoleBasedAccessControl implements AccessControl with role-based validation.
//
// The instance carries configuration only, deliberately no per-user state. It used
// to memoise the resolved user in a map on the instance keyed by the incoming
// context.Context, and that was wrong in three ways at once. The key is unique to
// one request, so an entry was never reused by a later request; instead the map
// grew by one entry for every request the process had ever served, holding the
// context.Context and the authenticated user behind it alive forever, and nothing
// on the normal path ever pruned it. The map had no mutex either, and since an
// application naturally shares one configuration-driven access control object
// across all of its requests, two concurrent requests writing their own misses
// into it produced "fatal error: concurrent map writes" - a runtime fatal rather
// than a panic, so the recovery middleware could not contain it and the process
// died together with every request in flight.
//
// The user is now resolved once per call and threaded through the private helpers, and
// memoised for the length of one request on the request's own context rather than here -
// see resolveUserContext. That is the lifetime the data actually has: it is thrown away
// with the request, it is reachable from nothing that outlives it, and two requests have
// nowhere to meet.
type RoleBasedAccessControl struct {
	config      *AccessControlConfig
	authContext core.IAuthContext
}

// NewRoleBasedAccessControl creates a new role-based access control instance
func NewRoleBasedAccessControl(config *AccessControlConfig, authContext core.IAuthContext) *RoleBasedAccessControl {
	return &RoleBasedAccessControl{
		config:      config,
		authContext: authContext,
	}
}

// ValidateFieldAccess validates field access based on user roles
func (rbac *RoleBasedAccessControl) ValidateFieldAccess(ctx context.Context, field string, operation string) error {
	return rbac.validateFieldAccess(ctx, rbac.resolveUserContext(ctx), field, operation)
}

func (rbac *RoleBasedAccessControl) validateFieldAccess(ctx context.Context, userContext *UserContextCache, field string, operation string) error {
	userRoles := userContext.roles

	if err := rbac.admits(userContext); err != nil {
		return fmt.Errorf("access to field %s refused: %w", field, err)
	}

	// Get field configuration
	fieldConfig, exists := rbac.config.FieldAccess[strings.ToLower(field)]
	if !exists {
		// If no specific field config, check domain-level access
		return rbac.validateDomainLevelAccess(ctx, operation, userRoles)
	}

	// Check operation-specific access
	operationConfig, exists := fieldConfig.Operations[operation]
	if !exists {
		return fmt.Errorf("operation %s not allowed on field %s", operation, field)
	}

	// Check if operation is allowed
	if !operationConfig.Allowed {
		return fmt.Errorf("operation %s not allowed on field %s", operation, field)
	}

	// Check role requirements
	if len(operationConfig.RequiredRoles) > 0 {
		if !rbac.hasAnyRole(userRoles, operationConfig.RequiredRoles) {
			return fmt.Errorf("insufficient permissions for operation %s on field %s", operation, field)
		}
	}

	return nil
}

// ValidateFilterAccess validates filter access based on user roles
func (rbac *RoleBasedAccessControl) ValidateFilterAccess(ctx context.Context, field string, operator string) error {
	userContext := rbac.resolveUserContext(ctx)
	userRoles := userContext.roles

	if err := rbac.admits(userContext); err != nil {
		return fmt.Errorf("filtering refused: %w", err)
	}

	// Check if field is allowed for filtering
	if err := rbac.validateFieldAccess(ctx, userContext, field, "filter"); err != nil {
		return err
	}

	// Check if operator is allowed
	if !rbac.config.IsOperatorAllowed(operator) {
		return fmt.Errorf("operator %s not allowed for filtering", operator)
	}

	// Check role requirements for filtering
	if len(rbac.config.FilterRoles) > 0 {
		if !rbac.hasAnyRole(userRoles, rbac.config.FilterRoles) {
			return fmt.Errorf("insufficient permissions for filtering")
		}
	}

	return nil
}

// ValidateSortAccess validates sort access based on user roles
func (rbac *RoleBasedAccessControl) ValidateSortAccess(ctx context.Context, field string) error {
	userContext := rbac.resolveUserContext(ctx)
	userRoles := userContext.roles

	if err := rbac.admits(userContext); err != nil {
		return fmt.Errorf("sorting refused: %w", err)
	}

	// Check if field is allowed for sorting
	if err := rbac.validateFieldAccess(ctx, userContext, field, "sort"); err != nil {
		return err
	}

	// Check role requirements for sorting
	if len(rbac.config.SortRoles) > 0 {
		if !rbac.hasAnyRole(userRoles, rbac.config.SortRoles) {
			return fmt.Errorf("insufficient permissions for sorting")
		}
	}

	return nil
}

// GetUserRoles returns the roles for the current user
func (rbac *RoleBasedAccessControl) GetUserRoles(ctx context.Context) []string {
	return rbac.resolveUserContext(ctx).roles
}

// IsGuest reports that the caller carries no identity — either because it established that,
// or because it could not find out.
//
// The two are deliberately the same answer here, because "treat as a guest" is the safe label
// for both and a caller reaching for this wants the safe label. They are *not* the same
// answer inside the decision functions, which use the tri-state directly: no grant is keyed
// off this method. Use IdentityResolutionError to tell them apart.
func (rbac *RoleBasedAccessControl) IsGuest(ctx context.Context) bool {
	return !rbac.resolveUserContext(ctx).isAuthenticated()
}

// GetCurrentUser retrieves the current user from context
func (rbac *RoleBasedAccessControl) GetCurrentUser(ctx context.Context) core.Authenticable {
	return rbac.getCurrentUser(ctx)
}

// GetCachedUserContext returns the identity and roles resolved for this context, from the
// request's memo when it has one. The result is a snapshot of one request: it is not stored
// on the instance, and holding on to it past the request will hold on to a stale user.
func (rbac *RoleBasedAccessControl) GetCachedUserContext(ctx context.Context) *UserContextCache {
	return rbac.resolveUserContext(ctx)
}

// ClearUserCache drops whatever identity has been memoised for this request, so the next
// question about it resolves again.
//
// It exists for the case the memo key cannot see. The key covers the session being replaced
// and the session being re-pointed at another user, which is every way the framework's own
// strategies change the principal mid-request; an application that changes it some other way
// - a custom strategy reading it from somewhere else, a role edit that must take effect
// before the response is written - calls this and is believed. On a context with no request
// behind it there is nothing memoised and nothing to do.
func (rbac *RoleBasedAccessControl) ClearUserCache(ctx context.Context) {
	if memo, _ := requestIdentityMemo(ctx); memo != nil {
		memo.InvalidateIdentity()
	}
}

// ClearAllUserCache does nothing, deliberately, and is kept so that existing callers still
// compile. There is no state spanning requests to clear: each request memoises its own
// identity on its own context and takes it away with it. What this used to do - swap in a
// fresh instance-level map while other requests were reading the old one - was itself
// unsafe on an object every request shares, and the map it cleared was the leak.
func (rbac *RoleBasedAccessControl) ClearAllUserCache() {
}

// resolveUserContext resolves the caller's identity and roles.
//
// Exported entry points call it exactly once and hand the result to the private helpers
// below, so that GetReadableFields does not re-ask the auth strategy for the current user
// once per field. That covers one call; the memo covers the request. Resolution is not
// free - a session-backed strategy looks the session up in its store and then loads the
// user, both database round trips in a real application - and a response asks per row:
// FieldFilteredDto.MarshalJSON asks for the readable fields once per DTO, so a hundred-row
// list without a memo performs a hundred session lookups and a hundred user loads for one
// principal that cannot have changed in between.
//
// The memo lives on the request, not here. See core.IRequestIdentityMemo for why: an
// instance-level map keyed by anything request-shaped is shared mutable state on an object
// every request drives at once, and nothing prunes it.
//
// The key is what keeps it honest, and getting it wrong is worse than having no memo at
// all - a stale identity is an authorization decision made about the wrong user. It is
// derived from the session the request currently carries: both its identifier, which moves
// when the session is replaced (Login rotates it and republishes it, so a memo taken before
// a login cannot be found after one), and the user id on it, which moves when the same
// session object is re-pointed at somebody else. Both are reads of an object already in
// memory, so checking them costs nothing like a resolution. A request carrying no session
// at all - a bearer token, where the principal is fixed by a header for the whole request -
// keys as such and is memoised too.
//
// Contexts with no per-request carrier on them, which is every CLI command, background job
// and unit test, resolve every time. That is the previous behaviour and it is correct, just
// not cheap; the amplification this avoids is a property of serving a request.
//
// Resolution deliberately runs outside the memo's lock: it can call into a session store
// and a database, and holding a request-wide lock across that would serialise a fanned-out
// handler on it. Two goroutines of the same request may therefore both resolve and both
// store, which costs one extra lookup and yields the same identity twice.
func (rbac *RoleBasedAccessControl) resolveUserContext(ctx context.Context) *UserContextCache {
	memo, key := requestIdentityMemo(ctx)
	if memo != nil {
		if memoised, ok := memo.LoadIdentity(key); ok {
			if cache, ok := memoised.(*UserContextCache); ok {
				return cache
			}
		}
	}

	cache := rbac.resolveUserContextUncached(ctx)

	if memo != nil {
		memo.StoreIdentity(key, cache)
	}

	return cache
}

// requestIdentityMemo returns the memo slot the context carries, if it carries one, along
// with the key the identity resolved from that context belongs under. A nil memo means
// "resolve every time", which is what a context with no request behind it gets.
func requestIdentityMemo(ctx context.Context) (core.IRequestIdentityMemo, string) {
	if ctx == nil {
		return nil, ""
	}

	carrier := ctx.Value(core.MessageContextKey)
	memo, ok := carrier.(core.IRequestIdentityMemo)
	if !ok {
		return nil, ""
	}

	messageContext, ok := carrier.(core.IMessageContext)
	if !ok {
		return memo, ""
	}

	session := messageContext.GetSession()
	if session == nil {
		return memo, ""
	}

	// The separator matters: without it a session id ending in a digit and a user id
	// beginning with one could collide with a different pairing of the two.
	return memo, "session:" + session.GetId() + "\x00" + session.GetUserId()
}

func (rbac *RoleBasedAccessControl) resolveUserContextUncached(ctx context.Context) *UserContextCache {
	cache := &UserContextCache{}

	// Get user from auth context
	var strategy core.IAuthStrategy
	if rbac.authContext != nil {
		strategy = rbac.authContext.ResolveAuthStrategyByContext(ctx)
	}

	// A nil strategy is not a failure: an application that configured no authentication has
	// positively established that its callers are anonymous.
	if strategy == nil {
		rbac.markAnonymous(cache)
		return cache
	}

	user, err := strategy.CurrentUser(ctx)
	switch {
	case err != nil:
		// The error used to be bound and never looked at, so a store that could not answer
		// produced a guest. Refusing is the fail-closed direction, and it is also the only
		// honest one: nothing has been established about this caller.
		log.Log().Errorf(
			"access control: the caller's identity could not be resolved, so the request is "+
				"refused rather than served as anonymous: %v", err)
		cache.user = nil
		cache.roles = []string{}
		cache.state = identityUnresolved
		cache.resolveErr = fmt.Errorf("%w: %v", ErrIdentityUnresolved, err)
	case user == nil:
		rbac.markAnonymous(cache)
	default:
		cache.user = user
		cache.state = identityAuthenticated
		if roleProvider, ok := user.(core.RoleProvider); ok {
			cache.roles = roleProvider.GetRoles()
		} else {
			cache.roles = []string{}
		}
	}

	return cache
}

// markAnonymous records a caller established to carry no identity.
func (rbac *RoleBasedAccessControl) markAnonymous(cache *UserContextCache) {
	cache.user = nil
	cache.state = identityAnonymous
	if rbac.config.AllowGuestAccess {
		cache.roles = []string{GuestRole}
	} else {
		cache.roles = []string{}
	}
}

// getCurrentUser retrieves the current user from context
func (rbac *RoleBasedAccessControl) getCurrentUser(ctx context.Context) core.Authenticable {
	return rbac.resolveUserContext(ctx).user
}

// CheckComplexity implements ComplexityLimiter.
//
// A limit of zero or less means "unbounded" rather than "refuse everything": the zero value of
// an int config field must not turn every request into a refusal for an application that never
// set it. NewAccessControlConfig sets real defaults (10 filters, 5 sorts).
func (rbac *RoleBasedAccessControl) CheckComplexity(filters int, sorts int) error {
	if rbac.config.MaxFilters > 0 && filters > rbac.config.MaxFilters {
		return fmt.Errorf(
			"the request asks for %d filters and at most %d are permitted",
			filters, rbac.config.MaxFilters)
	}
	if rbac.config.MaxSorts > 0 && sorts > rbac.config.MaxSorts {
		return fmt.Errorf(
			"the request asks for %d sort fields and at most %d are permitted",
			sorts, rbac.config.MaxSorts)
	}
	return nil
}

// evaluateOperationRule is the single place a configured operation rule is read.
//
// Every decision surface goes through it, which is the point: CanAccessEntity and CanReadField
// used to read the same rule with different code, so they disagreed — one exempted guests from
// RequiredRoles and the other did not, and the owner and access-level branches read `Allowed`
// while ignoring `RequiredRoles` entirely.
//
// `matched` says the rule decided the question. `allowed` is its answer. A rule that does not
// exist has not decided anything, so the caller applies its own default.
func evaluateOperationRule(
	uc *UserContextCache, rule OperationConfig, exists bool) (allowed bool, matched bool) {

	if !exists {
		return false, false
	}
	if rule.Deny {
		return false, true
	}
	if !rule.Allowed {
		return false, true
	}
	if len(rule.RequiredRoles) > 0 && !hasAnyRoleIn(uc.roles, rule.RequiredRoles) {
		return false, true
	}
	return true, true
}

// hasAnyRoleIn is hasAnyRole without a receiver, so evaluateOperationRule can be a function.
func hasAnyRoleIn(userRoles []string, requiredRoles []string) bool {
	for _, userRole := range userRoles {
		for _, requiredRole := range requiredRoles {
			if strings.EqualFold(userRole, requiredRole) {
				return true
			}
		}
	}
	return false
}

// validateDomainLevelAccess validates domain-level access
func (rbac *RoleBasedAccessControl) validateDomainLevelAccess(ctx context.Context, operation string, userRoles []string) error {
	// Check domain-level operation access
	domainConfig, exists := rbac.config.DomainOperations[operation]
	if !exists {
		return fmt.Errorf("operation %s not allowed", operation)
	}

	if !domainConfig.Allowed {
		return fmt.Errorf("operation %s not allowed", operation)
	}

	if len(domainConfig.RequiredRoles) > 0 {
		if !rbac.hasAnyRole(userRoles, domainConfig.RequiredRoles) {
			return fmt.Errorf("insufficient permissions for operation %s", operation)
		}
	}

	return nil
}

// hasAnyRole checks if user has any of the required roles
func (rbac *RoleBasedAccessControl) hasAnyRole(userRoles []string, requiredRoles []string) bool {
	for _, userRole := range userRoles {
		for _, requiredRole := range requiredRoles {
			if strings.EqualFold(userRole, requiredRole) {
				return true
			}
		}
	}
	return false
}

// AccessControlConfig defines the configuration for access control
type AccessControlConfig struct {
	// Field-level access control
	FieldAccess map[string]FieldAccessConfig `json:"field_access,omitempty"`

	// Domain-level operation access
	DomainOperations map[string]OperationConfig `json:"domain_operations,omitempty"`

	// Filter configuration
	AllowedOperators []string `json:"allowed_operators,omitempty"`
	FilterRoles      []string `json:"filter_roles,omitempty"`
	MaxFilters       int      `json:"max_filters,omitempty"`

	// Sort configuration
	SortRoles []string `json:"sort_roles,omitempty"`
	MaxSorts  int      `json:"max_sorts,omitempty"`

	// AllowedAccessLevels bounds the values core.OwnershipInfo.AccessLevel may take before
	// they are concatenated into a DomainOperations key.
	//
	// The access level is a free-form string the application's own entity returns, and
	// canAccessEntity turns it into a rule name — `read` + `_` + level. Without a bound, which
	// rule an entity selects is decided by whatever that method happens to return. Empty means
	// no bound, which is the pre-2.2 behaviour.
	AllowedAccessLevels []string `json:"allowed_access_levels,omitempty"`

	// General configuration
	AllowGuestAccess bool     `json:"allow_guest_access,omitempty"`
	DefaultRoles     []string `json:"default_roles,omitempty"`

	// Ownership configuration
	OwnershipField string   `json:"ownership_field,omitempty"`
	DefaultFields  []string `json:"default_fields,omitempty"`

	// DB-level RBAC configuration
	DBRBAC *DBRBACConfig `json:"db_rbac,omitempty"`
}

// FieldAccessConfig defines access control for a specific field
type FieldAccessConfig struct {
	Operations map[string]OperationConfig `json:"operations,omitempty"`
}

// OperationConfig defines access control for a specific operation
type OperationConfig struct {
	Allowed       bool     `json:"allowed,omitempty"`
	RequiredRoles []string `json:"required_roles,omitempty"`

	// Deny makes this rule a refusal: when true, Allowed and RequiredRoles are ignored and a
	// match ends the decision with "no".
	//
	// It is the only way an ownership or access-level rule can *narrow* access rather than
	// widen it. Without it every rule is grant-only and the first match wins, so an
	// application had no way to say "an entity reporting AccessLevel: viewer must be refused
	// even though the general read rule allows it" — and since the access level is a free-form
	// string the application itself produces, the mechanism was otherwise pure surface with no
	// way to bound it.
	//
	// The zero value is the previous behaviour, so no existing configuration changes meaning.
	Deny bool `json:"deny,omitempty"`
}

// NewAccessControlConfig creates a new access control configuration
func NewAccessControlConfig() *AccessControlConfig {
	return &AccessControlConfig{
		FieldAccess:      make(map[string]FieldAccessConfig),
		DomainOperations: make(map[string]OperationConfig),
		AllowedOperators: []string{"=", "!=", "like", "not like", "in", "not in", ">", ">=", "<", "<="},
		FilterRoles:      []string{},
		MaxFilters:       10,
		SortRoles:        []string{},
		MaxSorts:         5,
		AllowGuestAccess: false,
		DefaultRoles:     []string{},
		OwnershipField:   "",
		DefaultFields:    []string{},
	}
}

// IsOperatorAllowed checks if an operator is allowed
func (acc *AccessControlConfig) IsOperatorAllowed(operator string) bool {
	for _, allowed := range acc.AllowedOperators {
		if allowed == operator {
			return true
		}
	}
	return false
}

// AddFieldAccess adds field access configuration
func (acc *AccessControlConfig) AddFieldAccess(field string, operations map[string]OperationConfig) {
	acc.FieldAccess[strings.ToLower(field)] = FieldAccessConfig{
		Operations: operations,
	}
}

// AddDomainOperation adds domain-level operation configuration
func (acc *AccessControlConfig) AddDomainOperation(operation string, config OperationConfig) {
	acc.DomainOperations[operation] = config
}

// AccessControlBuilder provides a fluent interface for building access control configurations
type AccessControlBuilder struct {
	config *AccessControlConfig
}

// NewAccessControlBuilder creates a new access control builder
func NewAccessControlBuilder() *AccessControlBuilder {
	return &AccessControlBuilder{
		config: NewAccessControlConfig(),
	}
}

// AllowGuestAccess enables guest access
func (b *AccessControlBuilder) AllowGuestAccess() *AccessControlBuilder {
	b.config.AllowGuestAccess = true
	return b
}

// SetDefaultRoles sets default roles for users.
//
// Deprecated: it has never had an effect. AccessControlConfig.DefaultRoles is written here and
// read nowhere — a caller's roles come from the user model's GetRoles, and an anonymous caller
// gets the guest role or none. It is kept so existing configurations compile, and warns at
// build time rather than being wired in, because wiring it in would *grant* roles that are
// absent today: a widening change, which is the wrong direction to make silently in a security
// release. Set the roles on your user model instead.
func (b *AccessControlBuilder) SetDefaultRoles(roles ...string) *AccessControlBuilder {
	if len(roles) > 0 {
		log.Log().Warnf(
			"access control: SetDefaultRoles(%v) has no effect and never has — roles come from "+
				"the user model's GetRoles. Remove the call or move the roles onto the model.",
			roles)
	}
	b.config.DefaultRoles = roles
	return b
}

// SetFilterConfig sets filter configuration
func (b *AccessControlBuilder) SetFilterConfig(operators []string, roles []string, maxFilters int) *AccessControlBuilder {
	b.config.AllowedOperators = operators
	b.config.FilterRoles = roles
	b.config.MaxFilters = maxFilters
	return b
}

// SetSortConfig sets sort configuration
func (b *AccessControlBuilder) SetSortConfig(roles []string, maxSorts int) *AccessControlBuilder {
	b.config.SortRoles = roles
	b.config.MaxSorts = maxSorts
	return b
}

// SetDBRBACConfig sets DB-level RBAC configuration
func (b *AccessControlBuilder) SetDBRBACConfig(dbRbacConfig *DBRBACConfig) *AccessControlBuilder {
	b.config.DBRBAC = dbRbacConfig
	return b
}

// AddFieldAccess adds field access configuration
func (b *AccessControlBuilder) AddFieldAccess(field string, operations map[string]OperationConfig) *AccessControlBuilder {
	b.config.AddFieldAccess(field, operations)
	return b
}

// AddDomainOperation adds domain-level operation configuration
func (b *AccessControlBuilder) AddDomainOperation(operation string, allowed bool, roles ...string) *AccessControlBuilder {
	b.config.AddDomainOperation(operation, OperationConfig{
		Allowed:       allowed,
		RequiredRoles: roles,
	})
	return b
}

// Build creates the final access control configuration
func (b *AccessControlBuilder) Build() *AccessControlConfig {
	return b.config
}

// CanReadField checks if the current user can read a specific field
func (rbac *RoleBasedAccessControl) CanReadField(ctx context.Context, field string, entity any) bool {
	return rbac.canReadField(ctx, rbac.resolveUserContext(ctx), field, entity)
}

func (rbac *RoleBasedAccessControl) canReadField(ctx context.Context, userCache *UserContextCache, field string, entity any) bool {
	if !rbac.admitsBool(userCache) {
		return false
	}

	// Entity-level rules are authoritative when provided.
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok {
		return accessibleEntity.CanAccessField(ctx, userCache.user, field, "read")
	}

	// Get field configuration
	fieldConfig, exists := rbac.config.FieldAccess[strings.ToLower(field)]
	if !exists {
		// If no specific field config, check domain-level access
		domainConfig, exists := rbac.config.DomainOperations["read"]
		if !exists || !domainConfig.Allowed {
			return false
		}

		// Check role requirements for domain-level read access
		if len(domainConfig.RequiredRoles) > 0 {
			return rbac.hasAnyRole(userCache.roles, domainConfig.RequiredRoles)
		}
		return true
	}

	// Check operation-specific access
	operationConfig, exists := fieldConfig.Operations["read"]
	if !exists {
		return false
	}

	// Check if operation is allowed
	if !operationConfig.Allowed {
		return false
	}

	// Check role requirements
	if len(operationConfig.RequiredRoles) > 0 {
		if !rbac.hasAnyRole(userCache.roles, operationConfig.RequiredRoles) {
			return false
		}
	}

	// Ownership, where a field declares it, is a requirement and not a bonus.
	//
	// Both arms of this used to return true, so AccessControlConfig.OwnershipField had no
	// effect on a field-read decision at all: control only reaches here once Allowed and
	// RequiredRoles have already passed, so the "grant" it could give was one the caller
	// already had. An application that set OwnershipField and declared an `owner` operation
	// on a field was configuring a restriction and getting nothing. A field that declares no
	// `owner` operation is unaffected.
	if rbac.config.OwnershipField != "" {
		if ownerRule, declared := rbac.ownerRuleFor(field); declared {
			if !ownerRule.Allowed || ownerRule.Deny {
				return false
			}
			return rbac.isOwner(ctx, entity, userCache.user)
		}
	}

	return true
}

// ownerRuleFor returns the `owner` operation a field declares, if it declares one.
func (rbac *RoleBasedAccessControl) ownerRuleFor(field string) (OperationConfig, bool) {
	fieldConfig, exists := rbac.config.FieldAccess[strings.ToLower(field)]
	if !exists {
		return OperationConfig{}, false
	}
	ownerRule, declared := fieldConfig.Operations["owner"]
	return ownerRule, declared
}

// GetReadableFields returns the list of fields the current user can read
func (rbac *RoleBasedAccessControl) GetReadableFields(ctx context.Context, entity any) []string {
	userCache := rbac.resolveUserContext(ctx)

	// The global gate comes first, before anything is delegated. It used to come after: the
	// entity's own GetAccessibleFields was called with a nil user for a caller the
	// configuration refused outright, and whether that leaked depended on how carefully the
	// application nil-checked. canReadField already gets this order right.
	if !rbac.admitsBool(userCache) {
		return nil
	}

	// If domain implements AccessibleEntity interface, use its custom logic
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok {
		return accessibleEntity.GetAccessibleFields(ctx, userCache.user, "read")
	}

	var readableFields []string

	// Get all available fields from configuration
	allFields := make([]string, 0)
	for fieldName := range rbac.config.FieldAccess {
		allFields = append(allFields, fieldName)
	}

	// If no specific field configuration, use default fields
	if len(allFields) == 0 {
		allFields = rbac.config.DefaultFields
	}

	// Check each field for read access, reusing the identity resolved above rather
	// than resolving it again for every field
	for _, field := range allFields {
		canRead := rbac.canReadField(ctx, userCache, field, entity)
		if canRead {
			readableFields = append(readableFields, field)
		}
	}

	return readableFields
}

// accessLevelPermitted reports whether an entity-supplied access level may select a rule.
//
// An empty level selects nothing and is always fine. A non-empty one is checked against
// AllowedAccessLevels when the configuration declares any; an empty allowlist means the
// application has not bounded it, which is the pre-2.2 behaviour.
func (rbac *RoleBasedAccessControl) accessLevelPermitted(level string) bool {
	if level == "" || len(rbac.config.AllowedAccessLevels) == 0 {
		return true
	}
	return containsFold(rbac.config.AllowedAccessLevels, level)
}

// isOwner checks if the current user owns the domain.
//
// ctx is threaded through rather than replaced with context.Background(), which is what this
// used to hand to application code — discarding the request's deadline, its cancellation and
// everything else on it, at the one point where the application gets to make a decision.
func (rbac *RoleBasedAccessControl) isOwner(ctx context.Context, entity any, currentUser core.Authenticable) bool {
	if currentUser == nil || entity == nil {
		return false
	}

	// Check if domain implements AccessibleEntity interface for custom ownership logic
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok {
		ownershipInfo := accessibleEntity.GetOwnershipInfo(ctx, currentUser)
		return ownershipInfo.IsOwner
	}

	// Check if the domain has an owner field
	if ownerProvider, ok := entity.(core.OwnerProvider); ok {
		return ownerProvider.GetOwnerId() == currentUser.GetId()
	}

	// There used to be a third fallback here: an entity whose own GetId() equalled the user's
	// id was treated as owned by them. For any entity type that shares an id space with users —
	// anything with sequential integer keys, which is most things — that is a grant handed out
	// on a coincidence. An entity that wants to express ownership implements OwnerProvider or
	// AccessibleEntity; one that does neither has not expressed it.
	return false
}

// CanAccessEntity checks if the current user can access the domain for a specific operation
func (rbac *RoleBasedAccessControl) CanAccessEntity(ctx context.Context, entity any, operation string) bool {
	return rbac.canAccessEntity(ctx, rbac.resolveUserContext(ctx), entity, operation)
}

// canAccessEntity decides one entity, in a fixed order. First rule that matches wins:
//
//	0. identity unresolved                                     → DENY
//	1. anonymous and !AllowGuestAccess                          → DENY
//	2. AllowedAccessLevels set and the level is not in it        → DENY
//	3. IsOwner and `<op>_owner` exists and it denies             → DENY
//	4. IsOwner and `<op>_owner` exists and it allows             → ALLOW
//	5. AccessLevel set and `<op>_<level>` exists and it denies    → DENY
//	6. AccessLevel set and `<op>_<level>` exists and it allows    → ALLOW
//	7. `<op>` missing, or it denies                              → DENY
//	8. `<op>` allows                                             → ALLOW
//	9. fallthrough                                               → DENY
//
// Two things changed. Rows 4 and 6 now read RequiredRoles: they used to check only `Allowed`
// and return true, so an entity reporting AccessLevel "admin" selected `read_admin` and was
// granted regardless of the roles that rule declared — and the access level is a string the
// application's own entity returns. Row 2 is new, and bounds which rule a level may select at
// all. Rows 3 and 5 are new capability: ownership could previously only ever grant.
//
// The `userCache.user != nil` guard is gone from the ownership branches. GetOwnershipInfo's
// contract already accommodates a nil user, and dropping the guard is what lets an
// access-level rule *deny* an anonymous caller rather than skipping straight past it.
func (rbac *RoleBasedAccessControl) canAccessEntity(ctx context.Context, userCache *UserContextCache, entity any, operation string) bool {
	if !rbac.admitsBool(userCache) {
		return false
	}

	// If domain implements AccessibleEntity interface, use its custom logic
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok {
		ownershipInfo := accessibleEntity.GetOwnershipInfo(ctx, userCache.user)

		if !rbac.accessLevelPermitted(ownershipInfo.AccessLevel) {
			log.Log().Warnf(
				"access control: entity reported access level %q, which is not in "+
					"AllowedAccessLevels; refusing rather than letting it choose a rule",
				ownershipInfo.AccessLevel)
			return false
		}

		if ownershipInfo.IsOwner {
			rule, exists := rbac.config.DomainOperations[operation+"_owner"]
			if allowed, matched := evaluateOperationRule(userCache, rule, exists); matched {
				if allowed {
					return true
				}
				// An owner rule that exists and refuses is terminal only when it says so
				// explicitly; otherwise fall through to the general rule, which is the
				// smaller change and cannot revoke access anybody has today.
				if rule.Deny {
					return false
				}
			}
		}

		if ownershipInfo.AccessLevel != "" {
			rule, exists := rbac.config.DomainOperations[operation+"_"+ownershipInfo.AccessLevel]
			if allowed, matched := evaluateOperationRule(userCache, rule, exists); matched {
				if allowed {
					return true
				}
				if rule.Deny {
					return false
				}
			}
		}
	}

	// Check domain-level operation access
	domainConfig, exists := rbac.config.DomainOperations[operation]
	if !exists || !domainConfig.Allowed || domainConfig.Deny {
		return false
	}

	// Check role requirements.
	//
	// A guest is not exempt. This used to return true for one as soon as AllowGuestAccess was
	// on, so `AddDomainOperation("read", true, "admin")` granted read to an anonymous visitor
	// while correctly denying an authenticated non-admin — an inversion, not a loosening. An
	// admitted guest already carries the "guest" role, so a configuration that means to let
	// guests in lists it and passes the check below; the short-circuit only ever fired when
	// "guest" was absent, which is exactly the case the author wrote the list to exclude.
	if len(domainConfig.RequiredRoles) > 0 {
		return rbac.hasAnyRole(userCache.roles, domainConfig.RequiredRoles)
	}

	return true
}

// GetAccessibleEntities filters a list of entities based on access permissions
func (rbac *RoleBasedAccessControl) GetAccessibleEntities(ctx context.Context, entities []any, operation string) []any {
	userCache := rbac.resolveUserContext(ctx)

	var accessibleEntities []any

	for _, entity := range entities {
		if rbac.canAccessEntity(ctx, userCache, entity, operation) {
			accessibleEntities = append(accessibleEntities, entity)
		}
	}

	return accessibleEntities
}

// GetReadableFieldsForCollection returns fields readable for a collection (optimized for collections)
func (rbac *RoleBasedAccessControl) GetReadableFieldsForCollection(ctx context.Context, entityType any) []string {
	userCache := rbac.resolveUserContext(ctx)

	// The global gate first — see GetReadableFields.
	if !rbac.admitsBool(userCache) {
		return nil
	}

	// If entity type implements AccessibleEntity interface, use its custom logic
	if accessibleEntity, ok := entityType.(core.AccessibleEntity); ok {
		return accessibleEntity.GetAccessibleFields(ctx, userCache.user, "read")
	}

	var readableFields []string

	// Get all available fields from configuration
	allFields := make([]string, 0)
	for fieldName := range rbac.config.FieldAccess {
		allFields = append(allFields, fieldName)
	}

	// If no specific field configuration, use default fields
	if len(allFields) == 0 {
		allFields = rbac.config.DefaultFields
	}

	// For collections, we can optimize by checking field access once per field type
	// rather than per entity, since field-level permissions are typically consistent
	for _, field := range allFields {
		// Check if the field is generally readable by this user
		fieldConfig, exists := rbac.config.FieldAccess[strings.ToLower(field)]
		if !exists {
			// If no specific field config, check domain-level access
			domainConfig, exists := rbac.config.DomainOperations["read"]
			if exists && domainConfig.Allowed {
				// Check role requirements for domain-level read access
				if len(domainConfig.RequiredRoles) == 0 || rbac.hasAnyRole(userCache.roles, domainConfig.RequiredRoles) {
					readableFields = append(readableFields, field)
				}
			}
			continue
		}

		// Check operation-specific access
		operationConfig, exists := fieldConfig.Operations["read"]
		if !exists {
			continue
		}

		// Check if operation is allowed
		if !operationConfig.Allowed {
			continue
		}

		// Check role requirements
		if len(operationConfig.RequiredRoles) == 0 || rbac.hasAnyRole(userCache.roles, operationConfig.RequiredRoles) {
			readableFields = append(readableFields, field)
		}
	}

	return readableFields
}

// GetAccessibleEntitiesWithFields filters entities and returns optimized field list for collections
// GetAccessibleEntitiesWithFields filters entities and returns the field list for the
// collection.
//
// It used to make one type-level check and then return the input slice verbatim — the comment
// said "If entity type access is granted, all entities are accessible" — so every per-entity
// ownership decision was skipped. Its sibling GetAccessibleEntities, which callers reasonably
// treat as the same function plus a field list, decided each entity properly. Two exported
// methods over the same collection applied materially different policies.
//
// The half of the optimisation that was real is kept: the field list is computed once for the
// collection rather than once per row. Access is decided per entity, by the same predicate the
// sibling uses.
//
// The type-level pre-check is gone rather than kept as a short-circuit, because it is not a
// valid one: canAccessEntity can grant through the `_owner` and `_<level>` rules that
// canAccessEntityType knows nothing about, so an owner whose only grant is `read_owner` would
// have been refused before the loop ever ran.
func (rbac *RoleBasedAccessControl) GetAccessibleEntitiesWithFields(ctx context.Context, entities []any, operation string) ([]any, []string) {
	userCache := rbac.resolveUserContext(ctx)

	if !rbac.admitsBool(userCache) {
		return []any{}, []string{}
	}

	accessible := make([]any, 0, len(entities))
	for _, entity := range entities {
		if rbac.canAccessEntity(ctx, userCache, entity, operation) {
			accessible = append(accessible, entity)
		}
	}

	if len(accessible) == 0 {
		return []any{}, []string{}
	}

	return accessible, rbac.inheritedFieldPermissions(userCache, operation)
}

// CanAccessEntityType checks if user can access the entity type (simplified check)
func (rbac *RoleBasedAccessControl) CanAccessEntityType(ctx context.Context, operation string) bool {
	return rbac.canAccessEntityType(rbac.resolveUserContext(ctx), operation)
}

func (rbac *RoleBasedAccessControl) canAccessEntityType(userCache *UserContextCache, operation string) bool {
	if !rbac.admitsBool(userCache) {
		return false
	}

	// Check domain-level operation access
	domainConfig, exists := rbac.config.DomainOperations[operation]
	if !exists || !domainConfig.Allowed {
		return false
	}

	// A guest is not exempt from the role requirement — see canAccessEntity.
	if len(domainConfig.RequiredRoles) > 0 {
		return rbac.hasAnyRole(userCache.roles, domainConfig.RequiredRoles)
	}
	return true
}

// GetInheritedFieldPermissions returns field permissions that inherit from entity roles
func (rbac *RoleBasedAccessControl) GetInheritedFieldPermissions(ctx context.Context, operation string) []string {
	return rbac.inheritedFieldPermissions(rbac.resolveUserContext(ctx), operation)
}

func (rbac *RoleBasedAccessControl) inheritedFieldPermissions(userCache *UserContextCache, operation string) []string {
	// Reachable from the exported GetInheritedFieldPermissions, which had no gate at all.
	if !rbac.admitsBool(userCache) {
		return nil
	}

	// Get entity roles for this operation
	entityRoles := rbac.getEntityRolesForOperation(operation)

	// Get all available fields from configuration
	allFields := make([]string, 0)
	for fieldName := range rbac.config.FieldAccess {
		allFields = append(allFields, fieldName)
	}

	// If no specific field configuration, use default fields
	if len(allFields) == 0 {
		allFields = rbac.config.DefaultFields
	}

	var accessibleFields []string

	// Check each field for read access with inheritance
	for _, field := range allFields {
		fieldConfig, exists := rbac.config.FieldAccess[strings.ToLower(field)]
		if !exists {
			// No field config - inherit from entity roles
			if rbac.hasAnyRole(userCache.roles, entityRoles) {
				accessibleFields = append(accessibleFields, field)
			}
			continue
		}

		// Check operation-specific access
		operationConfig, exists := fieldConfig.Operations[operation]
		if !exists {
			// No operation config - inherit from entity roles
			if rbac.hasAnyRole(userCache.roles, entityRoles) {
				accessibleFields = append(accessibleFields, field)
			}
			continue
		}

		// Check if operation is allowed
		if !operationConfig.Allowed {
			continue
		}

		// Get effective field roles (inherit from entity if not specified)
		fieldRoles := operationConfig.RequiredRoles
		if len(fieldRoles) == 0 {
			fieldRoles = entityRoles // Inherit from entity
		} else {
			// Field roles can only restrict, not expand entity access
			fieldRoles = rbac.intersectRoles(entityRoles, fieldRoles)
		}

		// Check if user has any of the effective roles
		if len(fieldRoles) == 0 || rbac.hasAnyRole(userCache.roles, fieldRoles) {
			accessibleFields = append(accessibleFields, field)
		}
	}

	return accessibleFields
}

// getEntityRolesForOperation returns the roles required for entity-level access
func (rbac *RoleBasedAccessControl) getEntityRolesForOperation(operation string) []string {
	domainConfig, exists := rbac.config.DomainOperations[operation]
	if !exists {
		return []string{}
	}
	return domainConfig.RequiredRoles
}

// intersectRoles returns the intersection of two role slices (roles that exist in both)
func (rbac *RoleBasedAccessControl) intersectRoles(entityRoles []string, fieldRoles []string) []string {
	var intersection []string

	for _, entityRole := range entityRoles {
		for _, fieldRole := range fieldRoles {
			if strings.EqualFold(entityRole, fieldRole) {
				intersection = append(intersection, entityRole)
				break
			}
		}
	}

	return intersection
}

// CanUseDBLevelRBAC checks if RBAC can be handled at DB level
func (rbac *RoleBasedAccessControl) CanUseDBLevelRBAC(ctx context.Context, operation string) bool {
	return rbac.canUseDBLevelRBAC(rbac.resolveUserContext(ctx), operation)
}

func (rbac *RoleBasedAccessControl) canUseDBLevelRBAC(userCache *UserContextCache, operation string) bool {
	// Check if DB-level RBAC is enabled
	if rbac.config.DBRBAC == nil || !rbac.config.DBRBAC.EnableDBLevelRBAC {
		return false
	}

	// A caller whose identity could not be resolved gets no filters at all. Generating them
	// would mean deciding what a request may read on the strength of an identity nobody
	// established.
	if userCache.unresolved() {
		return false
	}

	// A guest may use DB-level RBAC only when the configuration admits guests *and* declares
	// filters for the guest role specifically.
	//
	// This used to scan every role's filters looking for a literal `1 = 1`, iterating the
	// CustomFilters map by value and discarding the role key — so a full-access filter written
	// under CustomFilters["admin"] was adopted by anonymous callers. The role a filter is
	// configured under is the whole of its meaning; looking for a shape instead of a key threw
	// that away.
	if userCache.isGuest() {
		if !rbac.config.AllowGuestAccess {
			return false
		}
		if guestFilters, exists := rbac.customFiltersFor(GuestRole); !exists || len(guestFilters) == 0 {
			return false
		}
	}

	// Can't use DB-level RBAC if user is nil (and not a guest)
	if userCache.user == nil && !userCache.isGuest() {
		return false
	}

	// Check if we have the necessary configuration for DB-level filtering
	if rbac.config.DBRBAC.OwnershipField == "" && len(rbac.config.DBRBAC.CustomFilters) == 0 {
		return false
	}
	return true
}

// GenerateDBFilters generates database-level filters for RBAC
func (rbac *RoleBasedAccessControl) GenerateDBFilters(ctx context.Context, operation string) ([]DBFilter, error) {
	userCache := rbac.resolveUserContext(ctx)

	if !rbac.canUseDBLevelRBAC(userCache, operation) {
		return nil, fmt.Errorf("DB-level RBAC not available for this context")
	}

	var filters []DBFilter

	// Generate ownership-based filters (only if not skipped for this user's roles)
	shouldSkipOwnership := false
	for _, userRole := range userCache.roles {
		for _, skipRole := range rbac.config.DBRBAC.SkipOwnershipForRoles {
			if strings.EqualFold(userRole, skipRole) {
				shouldSkipOwnership = true
				break
			}
		}
		if shouldSkipOwnership {
			break
		}
	}

	if rbac.config.DBRBAC.OwnershipField != "" && !shouldSkipOwnership {
		// An ownership filter with nobody to own anything used to be silently omitted, which
		// turns "you may see only your own rows" into "you may see every row". If ownership
		// is configured and this caller is not exempt from it, a missing identity is a
		// refusal, not a relaxation.
		if userCache.user == nil {
			return nil, fmt.Errorf(
				"ownership filtering on %q is configured for this operation but the caller has "+
					"no identity to filter by", rbac.config.DBRBAC.OwnershipField)
		}
		ownershipFilter := DBFilter{
			Field:    rbac.config.DBRBAC.OwnershipField,
			Operator: "=",
			Value:    userCache.user.GetId(),
			Logic:    "AND", // Ownership should be AND logic, not OR
		}
		filters = append(filters, ownershipFilter)
	}

	// Generate role-based custom filters. A guest's filters come from the guest role's own
	// bucket and nowhere else — see canUseDBLevelRBAC.
	if len(rbac.config.DBRBAC.CustomFilters) > 0 {
		for _, userRole := range userCache.roles {
			roleFilters, exists := rbac.customFiltersFor(userRole)
			if !exists {
				continue
			}
			for _, filter := range roleFilters {
				// A processor is named by Processor. It used to be looked up by Field, which
				// meant a caller had to put a processor name into the column slot — and every
				// shipped example did, so those filters were also unvalidatable as columns.
				// Field is still accepted as a fallback so an existing configuration keeps
				// working, but only when it names a registered processor.
				processorName := filter.Processor
				if processorName == "" {
					if _, isProcessor := rbac.config.DBRBAC.CustomFilterProcessors[filter.Field]; isProcessor {
						processorName = filter.Field
					}
				}

				if processorName != "" {
					processor, hasProcessor := rbac.config.DBRBAC.CustomFilterProcessors[processorName]
					if !hasProcessor {
						return nil, fmt.Errorf(
							"filter for role %s names processor %q, which is not registered",
							userRole, processorName)
					}
					processedFilters, err := processor(ctx, userCache.user, filter)
					if err != nil {
						// This used to print a warning and continue. A filter that vanishes is
						// not a filter that allows nothing — it is one that restricts nothing,
						// so dropping a restrictive filter silently widens the query. Refuse
						// the whole set instead.
						return nil, fmt.Errorf(
							"custom filter processor %q failed, so no filters can be generated "+
								"for role %s: %w", processorName, userRole, err)
					}
					filters = append(filters, processedFilters...)
					continue
				}

				processedFilter, err := rbac.processFilterWithUserContext(filter, userCache.user)
				if err != nil {
					return nil, fmt.Errorf("cannot build the %s filter on %q: %w",
						userRole, filter.Field, err)
				}
				filters = append(filters, processedFilter)
			}
		}
	}

	// If no filters were generated, decide explicitly rather than falling through.
	//
	// The guest branch that used to be here granted full access — a literal `1 = 1` — to any
	// anonymous caller the configuration admitted. It was unreachable in practice, because
	// canUseDBLevelRBAC had already refused a guest with no guest-role filters, but it encoded
	// "guest implies full access" and would have come alive the moment that gate was relaxed.
	// A guest with no filters configured for it now gets the same answer as anybody else with
	// none: nothing.
	if len(filters) == 0 {
		hasFullAccessRole := false
		for _, userRole := range userCache.roles {
			for _, skipRole := range rbac.config.DBRBAC.SkipOwnershipForRoles {
				if strings.EqualFold(userRole, skipRole) {
					hasFullAccessRole = true
					break
				}
			}
			if hasFullAccessRole {
				break
			}
		}

		if hasFullAccessRole {
			filters = append(filters, fullAccessDBFilter())
		} else {
			filters = append(filters, noAccessDBFilter())
		}
	}

	// Every filter is validated before it leaves, and one bad filter fails the whole call.
	//
	// Not "skip the bad one": these are restrictions, so a filter that is dropped does not
	// deny, it stops denying. The caller has to be prevented from running the query at all
	// rather than running a wider one than the policy describes.
	for _, filter := range filters {
		if err := filter.Validate(rbac.config.AllowedOperators); err != nil {
			return nil, fmt.Errorf("refusing to build an authorization predicate: %w", err)
		}
	}

	return filters, nil
}

// customFiltersFor looks up a role's filters without caring about case.
//
// The lookup used to be `CustomFilters[strings.ToLower(userRole)]` against a map whose keys
// come straight from YAML or JSON — and every example this framework ships writes them
// upper-case (`ADMIN:`, `TEAM_MANAGER:`, `USER:`). So the lookup missed, the filter set came
// back empty, and the request fell through to the no-filters branch: full access for a role in
// SkipOwnershipForRoles, none for anybody else. Either way the configured restrictions were
// not the ones applied.
//
// Matching case-insensitively fixes the deployed configurations rather than requiring them to
// be rewritten, and it only ever makes a *restriction* start applying, which is the safe
// direction for that to change in.
func (rbac *RoleBasedAccessControl) customFiltersFor(userRole string) ([]DBFilter, bool) {
	if filters, exists := rbac.config.DBRBAC.CustomFilters[userRole]; exists {
		return filters, true
	}
	if filters, exists := rbac.config.DBRBAC.CustomFilters[strings.ToLower(userRole)]; exists {
		return filters, true
	}
	for role, filters := range rbac.config.DBRBAC.CustomFilters {
		if strings.EqualFold(role, userRole) {
			return filters, true
		}
	}
	return nil, false
}

// fullAccessDBFilter is the predicate that restricts nothing.
//
// It is a RawSQL filter rather than Field:"1", Operator:"=", Value:1, because Field is a
// column reference now and "1" is not one. Expressing "no restriction" as a value comparison
// also meant every consumer had to recognise that exact shape to know what it meant, which is
// how a guest came to inherit an admin's.
func fullAccessDBFilter() DBFilter {
	return DBFilter{RawSQL: "1 = 1", Logic: "AND"}
}

// noAccessDBFilter is the predicate that admits nothing.
//
// Previously `id = -1`, which assumes the table has a column called id and that -1 is not a
// valid value in it. `1 = 0` assumes neither and is portable to both engines.
func noAccessDBFilter() DBFilter {
	return DBFilter{RawSQL: "1 = 0", Logic: "AND"}
}

// processFilterWithUserContext substitutes the caller's attributes into a filter's *values*.
//
// Field is deliberately untouched. It used to be substituted into as well, under a comment
// saying it "may contain raw SQL conditions" — and since Field is emitted as SQL syntax, that
// made every user attribute a way to rewrite the authorization predicate. `author_name =
// '{{user_username}}'` with a username of `x' OR 'a'='a` becomes `author_name = 'x' OR
// 'a'='a'`, which is true for every row, and the unparenthesised WHERE clause carries the OR
// across its siblings as well. Nothing about that needed a race or a special configuration:
// the username is chosen at registration.
//
// So substitution applies to Value, which is bound as a query parameter, and to each element of
// RawArgs, which are bound too. There is no longer any path from an attribute into SQL text.
func (rbac *RoleBasedAccessControl) processFilterWithUserContext(
	filter DBFilter, user core.Authenticable) (DBFilter, error) {

	processedFilter := filter

	if strValue, ok := filter.Value.(string); ok {
		substituted, err := rbac.substituteUserContext(strValue, user)
		if err != nil {
			return DBFilter{}, err
		}
		processedFilter.Value = substituted
	}

	if len(filter.RawArgs) > 0 {
		args := make([]interface{}, len(filter.RawArgs))
		for i, arg := range filter.RawArgs {
			if strArg, ok := arg.(string); ok {
				substituted, err := rbac.substituteUserContext(strArg, user)
				if err != nil {
					return DBFilter{}, err
				}
				args[i] = substituted
				continue
			}
			args[i] = arg
		}
		processedFilter.RawArgs = args
	}

	return processedFilter, nil
}

// substituteUserContext replaces the placeholders in one value with the caller's attributes.
func (rbac *RoleBasedAccessControl) substituteUserContext(
	template string, user core.Authenticable) (string, error) {

	if !strings.Contains(template, "{{") {
		return template, nil
	}
	if user == nil {
		return "", fmt.Errorf(
			"%q references the caller's attributes but there is no caller to read them from",
			template)
	}

	result := template
	result = strings.ReplaceAll(result, "{{user_id}}", fmt.Sprintf("%v", user.GetId()))
	result = strings.ReplaceAll(result, "{{.UserID}}", fmt.Sprintf("%v", user.GetId()))

	if roleProvider, ok := user.(core.RoleProvider); ok {
		roles := roleProvider.GetRoles()
		if len(roles) > 0 {
			result = strings.ReplaceAll(result, "{{user_role}}", roles[0])
			result = strings.ReplaceAll(result, "{{.UserRole}}", roles[0])
		}
	}

	for fieldName, placeholder := range rbac.config.DBRBAC.UserContextFields {
		oldPlaceholder := fmt.Sprintf("{{user_%s}}", fieldName)
		if !strings.Contains(result, placeholder) && !strings.Contains(result, oldPlaceholder) {
			continue
		}

		value, err := rbac.getUserContextValue(user, fieldName)
		if err != nil {
			return "", err
		}
		result = strings.ReplaceAll(result, placeholder, value)
		result = strings.ReplaceAll(result, oldPlaceholder, value)
	}

	// A placeholder that survived names an attribute UserContextFields does not declare.
	// Leaving it would put the literal braces into a bound value and silently match nothing —
	// or, before Field stopped being substituted into, into the SQL itself.
	if match := unexpandedPlaceholder.FindString(result); match != "" {
		return "", fmt.Errorf(
			"%s is not a declared user context field; add it to DBRBAC.UserContextFields", match)
	}

	return result, nil
}

// getUserContextValue extracts user context values based on field name.
//
// An unrecognised name is an error. It used to fall through to the caller's user id, which is
// the most dangerous available default for the fields this is actually used for: a mistyped
// `tennant_id` compared the tenant column against a user id, matched nothing on the caller's
// own tenant, and — depending on how the app used the result — either hid their data or
// scoped them to whichever tenant happened to share the id.
//
// An empty scoping attribute is likewise refused rather than emitted. `tenant_id = ''` is not
// "this caller's tenant"; it is a predicate whose meaning nobody chose.
func (rbac *RoleBasedAccessControl) getUserContextValue(
	user core.Authenticable, fieldName string) (string, error) {

	switch fieldName {
	case "user_id":
		return fmt.Sprintf("%v", user.GetId()), nil
	case "user_role":
		if roleProvider, ok := user.(core.RoleProvider); ok {
			roles := roleProvider.GetRoles()
			if len(roles) > 0 {
				return roles[0], nil
			}
		}
		return "user", nil
	case "department_id":
		if deptProvider, ok := user.(interface{ GetDepartment() string }); ok {
			return requireScopingValue(fieldName, deptProvider.GetDepartment())
		}
		return "", fmt.Errorf(
			"user context field %q needs the user model to implement GetDepartment() string",
			fieldName)
	case "tenant_id":
		if tenantProvider, ok := user.(interface{ GetTenantID() string }); ok {
			return requireScopingValue(fieldName, tenantProvider.GetTenantID())
		}
		return "", fmt.Errorf(
			"user context field %q needs the user model to implement GetTenantID() string",
			fieldName)
	case "manager_id":
		if managerProvider, ok := user.(interface{ GetManagerID() string }); ok {
			return requireScopingValue(fieldName, managerProvider.GetManagerID())
		}
		return "", fmt.Errorf(
			"user context field %q needs the user model to implement GetManagerID() string",
			fieldName)
	case "username":
		return user.GetUsername(), nil
	default:
		return "", fmt.Errorf(
			"user context field %q is not one this package knows how to read; the supported "+
				"names are user_id, user_role, username, department_id, tenant_id and manager_id",
			fieldName)
	}
}

// requireScopingValue refuses an empty value for an attribute a query is being scoped by.
func requireScopingValue(fieldName, value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf(
			"the caller's %s is empty, so the rows this operation may touch cannot be "+
				"determined", fieldName)
	}
	return value, nil
}

// GetAccessibleEntitiesGeneric filters a list of entities based on access permissions with type safety
func GetAccessibleEntitiesGeneric[T any](accessControl AccessControl, ctx context.Context, entities []T, operation string) []T {
	// Convert to []any for the existing method
	anyEntities := make([]any, len(entities))
	for i, entity := range entities {
		anyEntities[i] = entity
	}

	// Use the existing method
	accessibleAnyEntities := accessControl.GetAccessibleEntities(ctx, anyEntities, operation)

	// Convert back to []T
	accessibleEntities := make([]T, len(accessibleAnyEntities))
	for i, entity := range accessibleAnyEntities {
		accessibleEntities[i] = entity.(T)
	}

	return accessibleEntities
}

// GetAccessibleEntitiesWithFieldsGeneric filters entities and returns optimized field list for collections with type safety
func GetAccessibleEntitiesWithFieldsGeneric[T any](accessControl AccessControl, ctx context.Context, entities []T, operation string) ([]T, []string) {
	// Convert to []any for the existing method
	anyEntities := make([]any, len(entities))
	for i, entity := range entities {
		anyEntities[i] = entity
	}

	// Use the existing method
	accessibleAnyEntities, optimizedFields := accessControl.GetAccessibleEntitiesWithFields(ctx, anyEntities, operation)

	// Convert back to []T
	accessibleEntities := make([]T, len(accessibleAnyEntities))
	for i, entity := range accessibleAnyEntities {
		accessibleEntities[i] = entity.(T)
	}

	return accessibleEntities, optimizedFields
}
