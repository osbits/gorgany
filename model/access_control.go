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
	CanReadField(ctx context.Context, field string, entity core.Authenticable) bool

	// GetReadableFields returns the list of fields the current user can read
	GetReadableFields(ctx context.Context, entity core.Authenticable) []string
}

// RoleBasedAccessControl implements AccessControl with role-based validation
type RoleBasedAccessControl struct {
	config *AccessControlConfig
}

// NewRoleBasedAccessControl creates a new role-based access control instance
func NewRoleBasedAccessControl(config *AccessControlConfig) *RoleBasedAccessControl {
	return &RoleBasedAccessControl{
		config: config,
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

// GetUserRoles returns the roles for the current user
func (rbac *RoleBasedAccessControl) GetUserRoles(ctx context.Context) []string {
	// Get user from context
	user := rbac.getCurrentUser(ctx)
	if user == nil {
		return []string{}
	}

	// Try to get roles from the user
	if roleProvider, ok := user.(core.RoleProvider); ok {
		return roleProvider.GetRoles()
	}

	// Fallback to default roles
	return rbac.config.DefaultRoles
}

// IsGuest checks if the current user is a guest
func (rbac *RoleBasedAccessControl) IsGuest(ctx context.Context) bool {
	user := rbac.getCurrentUser(ctx)
	return user == nil
}

// GetCurrentUser retrieves the current user from context
func (rbac *RoleBasedAccessControl) GetCurrentUser(ctx context.Context) core.Authenticable {
	return rbac.getCurrentUser(ctx)
}

// getCurrentUser retrieves the current user from context
func (rbac *RoleBasedAccessControl) getCurrentUser(ctx context.Context) core.Authenticable {
	// Try to get user from message context
	if msgCtx, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext); ok {
		if authContext, ok := msgCtx.(core.IAuthContext); ok {
			if user, err := authContext.ResolveAuthStrategyByContext(ctx).CurrentUser(ctx); err == nil {
				return user
			}
		}
	}

	// Try to get user directly from context
	if user, ok := ctx.Value("current_user").(core.Authenticable); ok {
		return user
	}

	return nil
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
func (rbac *RoleBasedAccessControl) CanReadField(ctx context.Context, field string, entity core.Authenticable) bool {
	currentUser := rbac.GetCurrentUser(ctx)
	userRoles := rbac.GetUserRoles(ctx)
	isGuest := rbac.IsGuest(ctx)

	// Check if guest access is allowed
	if isGuest && !rbac.config.AllowGuestAccess {
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
			return rbac.hasAnyRole(userRoles, domainConfig.RequiredRoles)
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
		if !rbac.hasAnyRole(userRoles, operationConfig.RequiredRoles) {
			return false
		}
	}

	// Check ownership-based access if configured
	if rbac.config.OwnershipField != "" && entity != nil {
		if rbac.canAccessOwnedField(ctx, field, entity, currentUser) {
			return true
		}
	}

	return true
}

// GetReadableFields returns the list of fields the current user can read
func (rbac *RoleBasedAccessControl) GetReadableFields(ctx context.Context, entity core.Authenticable) []string {
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
func (rbac *RoleBasedAccessControl) canAccessOwnedField(ctx context.Context, field string, entity core.Authenticable, currentUser core.Authenticable) bool {
	if currentUser == nil || entity == nil {
		return false
	}

	// Check if the current user owns the entity
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

// isOwner checks if the current user owns the entity
func (rbac *RoleBasedAccessControl) isOwner(entity core.Authenticable, currentUser core.Authenticable) bool {
	// Check if the entity has an owner field
	if ownerProvider, ok := entity.(core.OwnerProvider); ok {
		return ownerProvider.GetOwnerId() == currentUser.GetId()
	}

	// Fallback: check if the entity ID matches the current user ID
	return entity.GetId() == currentUser.GetId()
}
