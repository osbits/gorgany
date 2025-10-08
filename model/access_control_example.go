package model

import (
	"context"
	"fmt"

	"git.qix.sx/gorgany/gorgany.git/app/core"
)

// ExampleUser represents a user domain that implements Authenticable
type ExampleUser struct {
	ID       string   `json:"id"`
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
}

func (u *ExampleUser) GetId() string {
	return u.ID
}

func (u *ExampleUser) GetUsername() string {
	return u.Username
}

func (u *ExampleUser) GetRole() core.UserRole {
	if len(u.Roles) > 0 {
		return core.UserRole(u.Roles[0]) // Return first role as primary role
	}
	return core.UserRole("user") // Default role
}

// GetRoles implements RoleProvider interface
func (u *ExampleUser) GetRoles() []string {
	return u.Roles
}

// ExampleDocument represents a document domain with flexible access control
type ExampleDocument struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Content        string `json:"content"`
	OwnerID        string `json:"owner_id"`
	CreatedBy      string `json:"created_by"`
	Department     string `json:"department"`
	IsPublic       bool   `json:"is_public"`
	IsConfidential bool   `json:"is_confidential"`
}

// GetId returns the document ID
func (d *ExampleDocument) GetId() string {
	return d.ID
}

// GetOwnerId implements OwnerProvider interface for basic ownership
func (d *ExampleDocument) GetOwnerId() string {
	return d.OwnerID
}

// GetAccessibleFields implements AccessibleEntity interface for flexible access control
func (d *ExampleDocument) GetAccessibleFields(ctx context.Context, user core.Authenticable, operation string) []string {
	var accessibleFields []string

	// Always accessible fields
	accessibleFields = append(accessibleFields, "id", "title")

	// Get ownership info
	ownershipInfo := d.GetOwnershipInfo(ctx, user)

	// Owner can access all fields
	if ownershipInfo.IsOwner {
		accessibleFields = append(accessibleFields, "content", "owner_id", "created_by", "department", "is_public", "is_confidential")
		return accessibleFields
	}

	// Department members can access non-confidential content
	if ownershipInfo.AccessLevel == "department_member" {
		accessibleFields = append(accessibleFields, "content", "created_by", "department", "is_public")
		if !d.IsConfidential {
			accessibleFields = append(accessibleFields, "is_confidential")
		}
		return accessibleFields
	}

	// Public documents are accessible to everyone
	if d.IsPublic {
		accessibleFields = append(accessibleFields, "content", "is_public")
	}

	// Admins can access everything
	if ownershipInfo.AccessLevel == "admin" {
		accessibleFields = append(accessibleFields, "content", "owner_id", "created_by", "department", "is_public", "is_confidential")
	}

	return accessibleFields
}

// CanAccessField implements AccessibleEntity interface for field-level access control
func (d *ExampleDocument) CanAccessField(ctx context.Context, user core.Authenticable, field string, operation string) bool {
	accessibleFields := d.GetAccessibleFields(ctx, user, operation)

	for _, accessibleField := range accessibleFields {
		if accessibleField == field {
			return true
		}
	}

	return false
}

// GetOwnershipInfo implements AccessibleEntity interface for flexible ownership
func (d *ExampleDocument) GetOwnershipInfo(ctx context.Context, user core.Authenticable) core.OwnershipInfo {
	if user == nil {
		return core.OwnershipInfo{
			IsOwner:     false,
			AccessLevel: "guest",
		}
	}

	userID := user.GetId()

	// Get user roles if user implements RoleProvider
	var userRoles []string
	if roleProvider, ok := user.(core.RoleProvider); ok {
		userRoles = roleProvider.GetRoles()
	}

	// Check if user is owner
	if d.OwnerID == userID {
		return core.OwnershipInfo{
			IsOwner:     true,
			OwnerId:     d.OwnerID,
			AccessLevel: "owner",
			CustomAccessData: map[string]any{
				"can_edit":   true,
				"can_delete": true,
			},
		}
	}

	// Check if user is admin
	for _, role := range userRoles {
		if role == "admin" {
			return core.OwnershipInfo{
				IsOwner:     false,
				OwnerId:     d.OwnerID,
				AccessLevel: "admin",
				CustomAccessData: map[string]any{
					"can_edit":               true,
					"can_delete":             true,
					"can_manage_permissions": true,
				},
			}
		}
	}

	// Check if user is in the same department
	// This is a simplified example - in real implementation, you'd check user's department
	for _, role := range userRoles {
		if role == "department_member" {
			return core.OwnershipInfo{
				IsOwner:     false,
				OwnerId:     d.OwnerID,
				AccessLevel: "department_member",
				CustomAccessData: map[string]any{
					"can_edit":   false,
					"can_delete": false,
					"department": d.Department,
				},
			}
		}
	}

	// Default: no special access
	return core.OwnershipInfo{
		IsOwner:     false,
		OwnerId:     d.OwnerID,
		AccessLevel: "viewer",
		CustomAccessData: map[string]any{
			"can_edit":   false,
			"can_delete": false,
		},
	}
}

// ExampleAccessControlUsage demonstrates how to use the new flexible access control
func ExampleAccessControlUsage() {
	// Create a user
	user := &ExampleUser{
		ID:       "user123",
		Username: "john_doe",
		Email:    "john@example.com",
		Roles:    []string{"user", "department_member"},
	}

	// Create a document
	document := &ExampleDocument{
		ID:             "doc456",
		Title:          "Important Document",
		Content:        "This is confidential content",
		OwnerID:        "user789", // Different from current user
		CreatedBy:      "user789",
		Department:     "engineering",
		IsPublic:       false,
		IsConfidential: true,
	}

	// Create access control configuration
	config := NewAccessControlBuilder().
		AllowGuestAccess().
		SetDefaultRoles("user").
		AddDomainOperation("read", true, "user", "admin").
		AddDomainOperation("read_owner", true, "owner").
		AddDomainOperation("read_department_member", true, "department_member").
		AddFieldAccess("content", map[string]OperationConfig{
			"read": {Allowed: true, RequiredRoles: []string{"owner", "admin", "department_member"}},
		}).
		Build()

	// Create mock auth context for example
	mockAuthContext := &MockAuthContext{}
	accessControl := NewRoleBasedAccessControl(config, mockAuthContext)

	// Create context with user
	ctx := context.WithValue(context.Background(), "current_user", user)

	// Test field access
	canReadContent := accessControl.CanReadField(ctx, "content", document)
	fmt.Printf("Can read content: %v\n", canReadContent) // Should be true (department_member)

	canReadConfidential := accessControl.CanReadField(ctx, "is_confidential", document)
	fmt.Printf("Can read confidential flag: %v\n", canReadConfidential) // Should be false (confidential + not owner)

	// Test domain access
	canAccessEntity := accessControl.CanAccessEntity(ctx, document, "read")
	fmt.Printf("Can access domain: %v\n", canAccessEntity) // Should be true

	// Test readable fields
	readableFields := accessControl.GetReadableFields(ctx, document)
	fmt.Printf("Readable fields: %v\n", readableFields)

	// Test with owner user
	ownerUser := &ExampleUser{
		ID:       "user789",
		Username: "owner_user",
		Email:    "owner@example.com",
		Roles:    []string{"user"},
	}

	ownerCtx := context.WithValue(context.Background(), "current_user", ownerUser)

	ownerCanReadConfidential := accessControl.CanReadField(ownerCtx, "is_confidential", document)
	fmt.Printf("Owner can read confidential flag: %v\n", ownerCanReadConfidential) // Should be true

	ownerReadableFields := accessControl.GetReadableFields(ownerCtx, document)
	fmt.Printf("Owner readable fields: %v\n", ownerReadableFields)
}
