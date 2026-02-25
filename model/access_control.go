package model

import (
	"context"
	"fmt"
	"strings"

	"github.com/osbits/gorgany/app/core"
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

// UserContextCache holds cached user information to avoid repeated lookups
type UserContextCache struct {
	user    core.Authenticable
	roles   []string
	isGuest bool
	isValid bool
}

// DBFilter represents a database-level filter for RBAC
type DBFilter struct {
	Field    string      `json:"field"`
	Operator string      `json:"operator"`
	Value    interface{} `json:"value"`
	Logic    string      `json:"logic"` // "AND" or "OR"

	// For complex queries
	Subquery *DBSubquery `json:"subquery,omitempty"`
	Join     *DBJoin     `json:"join,omitempty"`
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

// RoleBasedAccessControl implements AccessControl with role-based validation
type RoleBasedAccessControl struct {
	config      *AccessControlConfig
	authContext core.IAuthContext
	userCache   map[context.Context]*UserContextCache
}

// NewRoleBasedAccessControl creates a new role-based access control instance
func NewRoleBasedAccessControl(config *AccessControlConfig, authContext core.IAuthContext) *RoleBasedAccessControl {
	return &RoleBasedAccessControl{
		config:      config,
		authContext: authContext,
		userCache:   make(map[context.Context]*UserContextCache),
	}
}

// ValidateFieldAccess validates field access based on user roles
func (rbac *RoleBasedAccessControl) ValidateFieldAccess(ctx context.Context, field string, operation string) error {
	userRoles := rbac.GetUserRoles(ctx)
	isGuest := rbac.IsGuest(ctx)

	// Check if guest access is allowed
	if isGuest && !rbac.config.AllowGuestAccess {
		return fmt.Errorf("guest access not allowed for field %s", field)
	}

	// Get field configuration
	fieldConfig, exists := rbac.config.FieldAccess[strings.ToLower(field)]
	if !exists {
		// If no specific field config, check domain-level access
		return rbac.validateDomainLevelAccess(ctx, operation, userRoles, isGuest)
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
	userRoles := rbac.GetUserRoles(ctx)
	isGuest := rbac.IsGuest(ctx)

	// Check if guest access is allowed
	if isGuest && !rbac.config.AllowGuestAccess {
		return fmt.Errorf("guest access not allowed for filtering")
	}

	// Check if field is allowed for filtering
	if err := rbac.ValidateFieldAccess(ctx, field, "filter"); err != nil {
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
	userRoles := rbac.GetUserRoles(ctx)
	isGuest := rbac.IsGuest(ctx)

	// Check if guest access is allowed
	if isGuest && !rbac.config.AllowGuestAccess {
		return fmt.Errorf("guest access not allowed for sorting")
	}

	// Check if field is allowed for sorting
	if err := rbac.ValidateFieldAccess(ctx, field, "sort"); err != nil {
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

// GetUserRoles returns the roles for the current user (now uses cache)
func (rbac *RoleBasedAccessControl) GetUserRoles(ctx context.Context) []string {
	cache := rbac.getUserContextCache(ctx)
	return cache.roles
}

// IsGuest checks if the current user is a guest (now uses cache)
func (rbac *RoleBasedAccessControl) IsGuest(ctx context.Context) bool {
	cache := rbac.getUserContextCache(ctx)
	return cache.isGuest
}

// GetCurrentUser retrieves the current user from context
func (rbac *RoleBasedAccessControl) GetCurrentUser(ctx context.Context) core.Authenticable {
	return rbac.getCurrentUser(ctx)
}

// GetCachedUserContext returns the cached user context for better performance
func (rbac *RoleBasedAccessControl) GetCachedUserContext(ctx context.Context) *UserContextCache {
	return rbac.getUserContextCache(ctx)
}

// ClearUserCache clears the user cache for a specific context
func (rbac *RoleBasedAccessControl) ClearUserCache(ctx context.Context) {
	delete(rbac.userCache, ctx)
}

// ClearAllUserCache clears all user cache entries
func (rbac *RoleBasedAccessControl) ClearAllUserCache() {
	rbac.userCache = make(map[context.Context]*UserContextCache)
}

// getUserContextCache retrieves or creates cached user context
func (rbac *RoleBasedAccessControl) getUserContextCache(ctx context.Context) *UserContextCache {
	// Check if we already have cached data for this context
	if cache, exists := rbac.userCache[ctx]; exists && cache.isValid {
		return cache
	}

	// Create new cache entry
	cache := &UserContextCache{}

	// Get user from auth context
	if user, err := rbac.authContext.ResolveAuthStrategyByContext(ctx).CurrentUser(ctx); err == nil && user != nil {
		cache.user = user
		cache.isGuest = false

		// Get user roles
		if roleProvider, ok := user.(core.RoleProvider); ok {
			cache.roles = roleProvider.GetRoles()
		} else {
			cache.roles = []string{}
		}
	} else {
		cache.user = nil
		cache.isGuest = true
		// Assign guest role if guest access is allowed
		if rbac.config.AllowGuestAccess {
			cache.roles = []string{"guest"}
		} else {
			cache.roles = []string{}
		}
	}

	cache.isValid = true
	rbac.userCache[ctx] = cache
	return cache
}

// getCurrentUser retrieves the current user from context (now uses cache)
func (rbac *RoleBasedAccessControl) getCurrentUser(ctx context.Context) core.Authenticable {
	cache := rbac.getUserContextCache(ctx)
	return cache.user
}

// validateDomainLevelAccess validates domain-level access
func (rbac *RoleBasedAccessControl) validateDomainLevelAccess(ctx context.Context, operation string, userRoles []string, isGuest bool) error {
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

// SetDefaultRoles sets default roles for users
func (b *AccessControlBuilder) SetDefaultRoles(roles ...string) *AccessControlBuilder {
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
	// Use cached user context for better performance
	userCache := rbac.getUserContextCache(ctx)

	// Check if guest access is allowed
	if userCache.isGuest && !rbac.config.AllowGuestAccess {
		return false
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

	// Check if domain implements AccessibleEntity interface for custom access control first
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok && userCache.user != nil {
		if accessibleEntity.CanAccessField(ctx, userCache.user, field, "read") {
			return true
		}
	}

	// Check role requirements
	if len(operationConfig.RequiredRoles) > 0 {
		if !rbac.hasAnyRole(userCache.roles, operationConfig.RequiredRoles) {
			return false
		}
	}

	// Check ownership-based access if configured
	if rbac.config.OwnershipField != "" && entity != nil {
		if rbac.canAccessOwnedField(ctx, field, entity, userCache.user) {
			return true
		}
	}

	return true
}

// GetReadableFields returns the list of fields the current user can read
func (rbac *RoleBasedAccessControl) GetReadableFields(ctx context.Context, entity any) []string {
	// Use cached user context for better performance
	userCache := rbac.getUserContextCache(ctx)

	// If domain implements AccessibleEntity interface, use its custom logic
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok && userCache.user != nil {
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

	// Check each field for read access
	for _, field := range allFields {
		canRead := rbac.CanReadField(ctx, field, entity)
		if canRead {
			readableFields = append(readableFields, field)
		}
	}

	return readableFields
}

// canAccessOwnedField checks if the current user can access a field based on ownership
func (rbac *RoleBasedAccessControl) canAccessOwnedField(ctx context.Context, field string, entity any, currentUser core.Authenticable) bool {
	if currentUser == nil || entity == nil {
		return false
	}

	// Check if the current user owns the domain
	if rbac.isOwner(entity, currentUser) {
		// Check if the field allows owner access
		fieldConfig, exists := rbac.config.FieldAccess[strings.ToLower(field)]
		if exists {
			if ownerConfig, exists := fieldConfig.Operations["owner"]; exists {
				return ownerConfig.Allowed
			}
		}
	}

	return false
}

// isOwner checks if the current user owns the domain
func (rbac *RoleBasedAccessControl) isOwner(entity any, currentUser core.Authenticable) bool {
	// Check if domain implements AccessibleEntity interface for custom ownership logic
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok {
		ownershipInfo := accessibleEntity.GetOwnershipInfo(context.Background(), currentUser)
		return ownershipInfo.IsOwner
	}

	// Check if the domain has an owner field
	if ownerProvider, ok := entity.(core.OwnerProvider); ok {
		return ownerProvider.GetOwnerId() == currentUser.GetId()
	}

	// Fallback: check if the domain ID matches the current user ID
	if entityWithId, ok := entity.(interface{ GetId() string }); ok {
		return entityWithId.GetId() == currentUser.GetId()
	}

	return false
}

// CanAccessEntity checks if the current user can access the domain for a specific operation
func (rbac *RoleBasedAccessControl) CanAccessEntity(ctx context.Context, entity any, operation string) bool {
	// Use cached user context for better performance
	userCache := rbac.getUserContextCache(ctx)

	// Check if guest access is allowed
	if userCache.isGuest && !rbac.config.AllowGuestAccess {
		return false
	}

	// If domain implements AccessibleEntity interface, use its custom logic
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok && userCache.user != nil {
		ownershipInfo := accessibleEntity.GetOwnershipInfo(ctx, userCache.user)

		// Check if user has access based on ownership
		if ownershipInfo.IsOwner {
			// Check domain-level operation access for owners
			domainConfig, exists := rbac.config.DomainOperations[operation+"_owner"]
			if exists && domainConfig.Allowed {
				return true
			}
		}

		// Check access level
		if ownershipInfo.AccessLevel != "" {
			domainConfig, exists := rbac.config.DomainOperations[operation+"_"+ownershipInfo.AccessLevel]
			if exists && domainConfig.Allowed {
				return true
			}
		}
	}

	// Check domain-level operation access
	domainConfig, exists := rbac.config.DomainOperations[operation]
	if !exists || !domainConfig.Allowed {
		return false
	}

	// Check role requirements
	if len(domainConfig.RequiredRoles) > 0 {
		// If guest access is allowed, guests can access even without required roles
		if userCache.isGuest && rbac.config.AllowGuestAccess {
			return true
		}
		return rbac.hasAnyRole(userCache.roles, domainConfig.RequiredRoles)
	}

	return true
}

// GetAccessibleEntities filters a list of entities based on access permissions
func (rbac *RoleBasedAccessControl) GetAccessibleEntities(ctx context.Context, entities []any, operation string) []any {
	var accessibleEntities []any

	for _, entity := range entities {
		if rbac.CanAccessEntity(ctx, entity, operation) {
			accessibleEntities = append(accessibleEntities, entity)
		}
	}

	return accessibleEntities
}

// GetReadableFieldsForCollection returns fields readable for a collection (optimized for collections)
func (rbac *RoleBasedAccessControl) GetReadableFieldsForCollection(ctx context.Context, entityType any) []string {
	// Use cached user context for better performance
	userCache := rbac.getUserContextCache(ctx)

	// If entity type implements AccessibleEntity interface, use its custom logic
	if accessibleEntity, ok := entityType.(core.AccessibleEntity); ok && userCache.user != nil {
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
func (rbac *RoleBasedAccessControl) GetAccessibleEntitiesWithFields(ctx context.Context, entities []any, operation string) ([]any, []string) {
	// Simplified approach: Check entity type access first
	if !rbac.CanAccessEntityType(ctx, operation) {
		return []any{}, []string{}
	}

	// If entity type access is granted, all entities are accessible
	// Get inherited field permissions
	readableFields := rbac.GetInheritedFieldPermissions(ctx, operation)

	return entities, readableFields
}

// CanAccessEntityType checks if user can access the entity type (simplified check)
func (rbac *RoleBasedAccessControl) CanAccessEntityType(ctx context.Context, operation string) bool {
	userCache := rbac.getUserContextCache(ctx)

	// Check if guest access is allowed
	if userCache.isGuest && !rbac.config.AllowGuestAccess {
		return false
	}

	// Check domain-level operation access
	domainConfig, exists := rbac.config.DomainOperations[operation]
	if !exists || !domainConfig.Allowed {
		return false
	}

	// Check role requirements
	if len(domainConfig.RequiredRoles) > 0 {
		// If guest access is allowed, guests can access even without required roles
		if userCache.isGuest && rbac.config.AllowGuestAccess {
			return true
		}
		return rbac.hasAnyRole(userCache.roles, domainConfig.RequiredRoles)
	}
	return true
}

// GetInheritedFieldPermissions returns field permissions that inherit from entity roles
func (rbac *RoleBasedAccessControl) GetInheritedFieldPermissions(ctx context.Context, operation string) []string {
	userCache := rbac.getUserContextCache(ctx)

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
	// Check if DB-level RBAC is enabled
	if rbac.config.DBRBAC == nil || !rbac.config.DBRBAC.EnableDBLevelRBAC {
		return false
	}

	// Check if user context allows DB-level filtering
	userCache := rbac.getUserContextCache(ctx)

	// For guests, we can only use DB-level RBAC if:
	// 1. Guest access is allowed, AND
	// 2. We have custom filters that don't require user context (like "1=1")
	if userCache.isGuest {
		// Check if guest access is allowed
		if !rbac.config.AllowGuestAccess {
			return false
		}

		// Check if we have custom filters that work for guests
		hasGuestCompatibleFilters := false
		for _, roleFilters := range rbac.config.DBRBAC.CustomFilters {
			for _, filter := range roleFilters {
				// Check if this filter works for guests (no user context placeholders)
				if filter.Field == "1" && filter.Operator == "=" && (filter.Value == "1" || filter.Value == 1) {
					hasGuestCompatibleFilters = true
					break
				}
			}
			if hasGuestCompatibleFilters {
				break
			}
		}

		// If no guest-compatible filters, can't use DB-level RBAC
		if !hasGuestCompatibleFilters {
			return false
		}
	}

	// Can't use DB-level RBAC if user is nil (and not a guest)
	if userCache.user == nil && !userCache.isGuest {
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
	if !rbac.CanUseDBLevelRBAC(ctx, operation) {
		return nil, fmt.Errorf("DB-level RBAC not available for this context")
	}

	userCache := rbac.getUserContextCache(ctx)
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

	if rbac.config.DBRBAC.OwnershipField != "" && !shouldSkipOwnership && userCache.user != nil {
		ownershipFilter := DBFilter{
			Field:    rbac.config.DBRBAC.OwnershipField,
			Operator: "=",
			Value:    userCache.user.GetId(),
			Logic:    "AND", // Ownership should be AND logic, not OR
		}
		filters = append(filters, ownershipFilter)
	}

	// Generate role-based custom filters
	if len(rbac.config.DBRBAC.CustomFilters) > 0 {
		// For guests, look for filters that don't require specific roles
		if userCache.isGuest {
			// Look for filters that work for guests (like "1=1" conditions)
			for _, roleFilters := range rbac.config.DBRBAC.CustomFilters {
				for _, filter := range roleFilters {
					// Check if this filter works for guests (no user context placeholders)
					if filter.Field == "1" && filter.Operator == "=" && (filter.Value == "1" || filter.Value == 1) {
						filters = append(filters, filter)
					}
				}
			}
		} else {
			// For authenticated users, use role-based filtering
			for _, userRole := range userCache.roles {
				userRole = strings.ToLower(userRole)
				if roleFilters, exists := rbac.config.DBRBAC.CustomFilters[userRole]; exists {
					for _, filter := range roleFilters {
						// Check if this filter has a custom processor
						if processor, hasProcessor := rbac.config.DBRBAC.CustomFilterProcessors[filter.Field]; hasProcessor {
							// Use custom processor for complex logic
							processedFilters, err := processor(ctx, userCache.user, filter)
							if err != nil {
								// Log error but continue processing other filters
								fmt.Printf("Warning: Custom filter processor failed for field %s: %v\n", filter.Field, err)
								continue
							}
							filters = append(filters, processedFilters...)
						} else {
							// Process filter values to replace user context placeholders
							processedFilter := rbac.processFilterWithUserContext(filter, userCache.user)
							// Keep the original logic from the filter configuration
							filters = append(filters, processedFilter)
						}
					}
				}
			}
		}
	}

	// If no filters generated, check if user should have full access
	if len(filters) == 0 {
		// For guests, if guest access is allowed, give them full access
		if userCache.isGuest && rbac.config.AllowGuestAccess {
			// Guest users with no specific filters should have full access
			fullAccessFilter := DBFilter{
				Field:    "1",
				Operator: "=",
				Value:    1,
				Logic:    "AND",
			}
			filters = append(filters, fullAccessFilter)
		} else {
			// Check if user has admin roles that should have full access
			hasAdminAccess := false
			for _, userRole := range userCache.roles {
				for _, skipRole := range rbac.config.DBRBAC.SkipOwnershipForRoles {
					if strings.EqualFold(userRole, skipRole) {
						hasAdminAccess = true
						break
					}
				}
				if hasAdminAccess {
					break
				}
			}

			if hasAdminAccess {
				// Admin users with no specific filters should have full access
				// Create a filter that allows everything (1=1)
				fullAccessFilter := DBFilter{
					Field:    "1",
					Operator: "=",
					Value:    1,
					Logic:    "AND",
				}
				filters = append(filters, fullAccessFilter)
			} else {
				// Non-admin users with no filters should have no access
				noAccessFilter := DBFilter{
					Field:    "id",
					Operator: "=",
					Value:    -1, // Non-existent ID
					Logic:    "AND",
				}
				filters = append(filters, noAccessFilter)
			}
		}
	}

	return filters, nil
}

// processFilterWithUserContext processes filter values to replace user context placeholders
func (rbac *RoleBasedAccessControl) processFilterWithUserContext(filter DBFilter, user core.Authenticable) DBFilter {
	processedFilter := filter

	// Process the Field (which may contain raw SQL conditions)
	if filter.Field != "" {
		processedField := filter.Field

		// Replace {{user_id}} with actual user ID
		processedField = strings.ReplaceAll(processedField, "{{user_id}}", fmt.Sprintf("%v", user.GetId()))
		processedField = strings.ReplaceAll(processedField, "{{.UserID}}", fmt.Sprintf("%v", user.GetId()))

		// Replace {{user_role}} with user's primary role (if available)
		if roleProvider, ok := user.(core.RoleProvider); ok {
			roles := roleProvider.GetRoles()
			if len(roles) > 0 {
				processedField = strings.ReplaceAll(processedField, "{{user_role}}", roles[0])
				processedField = strings.ReplaceAll(processedField, "{{.UserRole}}", roles[0])
			}
		}

		// Replace other user context fields
		for fieldName, placeholder := range rbac.config.DBRBAC.UserContextFields {
			// Replace new format {{.FieldName}}
			processedField = strings.ReplaceAll(processedField, placeholder, rbac.getUserContextValue(user, fieldName))

			// Replace old format {{user_fieldName}}
			oldPlaceholder := fmt.Sprintf("{{user_%s}}", fieldName)
			processedField = strings.ReplaceAll(processedField, oldPlaceholder, rbac.getUserContextValue(user, fieldName))
		}

		processedFilter.Field = processedField
	}

	// Process string values for placeholders (legacy support)
	if strValue, ok := filter.Value.(string); ok {
		processedValue := strValue

		// Replace {{user_id}} with actual user ID
		processedValue = strings.ReplaceAll(processedValue, "{{user_id}}", fmt.Sprintf("%v", user.GetId()))
		processedValue = strings.ReplaceAll(processedValue, "{{.UserID}}", fmt.Sprintf("%v", user.GetId()))

		// Replace {{user_role}} with user's primary role (if available)
		if roleProvider, ok := user.(core.RoleProvider); ok {
			roles := roleProvider.GetRoles()
			if len(roles) > 0 {
				processedValue = strings.ReplaceAll(processedValue, "{{user_role}}", roles[0])
				processedValue = strings.ReplaceAll(processedValue, "{{.UserRole}}", roles[0])
			}
		}

		// Replace other user context fields
		for fieldName, placeholder := range rbac.config.DBRBAC.UserContextFields {
			// Replace new format {{.FieldName}}
			processedValue = strings.ReplaceAll(processedValue, placeholder, rbac.getUserContextValue(user, fieldName))

			// Replace old format {{user_fieldName}}
			oldPlaceholder := fmt.Sprintf("{{user_%s}}", fieldName)
			processedValue = strings.ReplaceAll(processedValue, oldPlaceholder, rbac.getUserContextValue(user, fieldName))
		}

		processedFilter.Value = processedValue
	}

	return processedFilter
}

// getUserContextValue extracts user context values based on field name
func (rbac *RoleBasedAccessControl) getUserContextValue(user core.Authenticable, fieldName string) string {
	switch fieldName {
	case "user_id":
		return fmt.Sprintf("%v", user.GetId())
	case "user_role":
		if roleProvider, ok := user.(core.RoleProvider); ok {
			roles := roleProvider.GetRoles()
			if len(roles) > 0 {
				return roles[0]
			}
		}
		return "user"
	case "department_id":
		// Try to get department from user if it implements a department interface
		if deptProvider, ok := user.(interface{ GetDepartment() string }); ok {
			return deptProvider.GetDepartment()
		}
		// For our test user, we'll use reflection to get the Department field
		if testUser, ok := user.(*DBTestUser); ok {
			return testUser.Department
		}
		return ""
	case "tenant_id":
		// Try to get tenant from user if it implements a tenant interface
		if tenantProvider, ok := user.(interface{ GetTenantID() string }); ok {
			return tenantProvider.GetTenantID()
		}
		// For our test user, we'll use reflection to get the TenantID field
		if testUser, ok := user.(*DBTestUser); ok {
			return testUser.TenantID
		}
		return ""
	case "manager_id":
		// Try to get manager from user if it implements a manager interface
		if managerProvider, ok := user.(interface{ GetManagerID() string }); ok {
			return managerProvider.GetManagerID()
		}
		return ""
	case "username":
		// Get username from user
		return user.GetUsername()
	default:
		// Default to user ID for unknown fields
		return fmt.Sprintf("%v", user.GetId())
	}
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
