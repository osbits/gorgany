package model

import (
	"context"
	"fmt"
	"strings"

	"git.qix.sx/gorgany/gorgany.git/app/core"
)

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
}

// UserContextCache holds cached user information to avoid repeated lookups
type UserContextCache struct {
	user    core.Authenticable
	roles   []string
	isGuest bool
	isValid bool
}

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
	fieldConfig, exists := rbac.config.FieldAccess[field]
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
	if user, err := rbac.authContext.ResolveAuthStrategyByContext(ctx).CurrentUser(ctx); err == nil {
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
		cache.roles = []string{}
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
	acc.FieldAccess[field] = FieldAccessConfig{
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
	fieldConfig, exists := rbac.config.FieldAccess[field]
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

	// Check ownership-based access if configured
	if rbac.config.OwnershipField != "" && entity != nil {
		if rbac.canAccessOwnedField(ctx, field, entity, userCache.user) {
			return true
		}
	}

	// Check if domain implements AccessibleEntity interface for custom access control
	if accessibleEntity, ok := entity.(core.AccessibleEntity); ok && userCache.user != nil {
		if accessibleEntity.CanAccessField(ctx, userCache.user, field, "read") {
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
		if rbac.CanReadField(ctx, field, entity) {
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
		fieldConfig, exists := rbac.config.FieldAccess[field]
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
		fieldConfig, exists := rbac.config.FieldAccess[field]
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
		fieldConfig, exists := rbac.config.FieldAccess[field]
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
