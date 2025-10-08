package model

import (
	"context"
	"fmt"
	"testing"

	"git.qix.sx/gorgany/gorgany.git/app/core"
)

// Test entities for validation
type TestUser struct {
	ID         string   `json:"id"`
	Username   string   `json:"username"`
	Email      string   `json:"email"`
	Roles      []string `json:"roles"`
	Department string   `json:"department"`
}

func (u *TestUser) GetId() string {
	return u.ID
}

func (u *TestUser) GetUsername() string {
	return u.Username
}

func (u *TestUser) GetPassword() string {
	return "hashed_password"
}

func (u *TestUser) GetRole() core.UserRole {
	if len(u.Roles) > 0 {
		return core.UserRole(u.Roles[0])
	}
	return core.UserRole("user")
}

func (u *TestUser) GetRoles() []string {
	return u.Roles
}

// Employee record with sensitive information
type Employee struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Email       string  `json:"email"`
	Department  string  `json:"department"`
	ManagerID   string  `json:"manager_id"`
	Salary      float64 `json:"salary"`
	SSN         string  `json:"ssn"`
	Performance string  `json:"performance"`
	IsActive    bool    `json:"is_active"`
}

func (e *Employee) GetId() string {
	return e.ID
}

// Implement AccessibleEntity for flexible access control
func (e *Employee) GetAccessibleFields(ctx context.Context, user core.Authenticable, operation string) []string {
	var accessibleFields []string

	// Always accessible fields
	accessibleFields = append(accessibleFields, "id", "name", "is_active")

	if user == nil {
		return accessibleFields
	}

	userID := user.GetId()
	userRoles := []string{}
	if roleProvider, ok := user.(core.RoleProvider); ok {
		userRoles = roleProvider.GetRoles()
	}

	// Check if user is the employee themselves
	if e.ID == userID {
		// Employee can see their own basic info and performance
		accessibleFields = append(accessibleFields, "email", "department", "performance")
		return accessibleFields
	}

	// Check if user is the manager
	if e.ManagerID == userID {
		// Manager can see most fields except sensitive ones
		accessibleFields = append(accessibleFields, "email", "department", "performance")
		return accessibleFields
	}

	// Check if user is HR or admin
	for _, role := range userRoles {
		if role == "hr" || role == "admin" {
			// HR and admin can see everything
			accessibleFields = append(accessibleFields, "email", "department", "manager_id", "salary", "ssn", "performance")
			return accessibleFields
		}
	}

	// Check if user is senior manager (can see salary but not SSN)
	for _, role := range userRoles {
		if role == "senior_manager" {
			accessibleFields = append(accessibleFields, "email", "department", "manager_id", "salary", "performance")
			return accessibleFields
		}
	}

	// Default: only basic info
	return accessibleFields
}

func (e *Employee) CanAccessField(ctx context.Context, user core.Authenticable, field string, operation string) bool {
	accessibleFields := e.GetAccessibleFields(ctx, user, operation)

	for _, accessibleField := range accessibleFields {
		if accessibleField == field {
			return true
		}
	}

	return false
}

func (e *Employee) GetOwnershipInfo(ctx context.Context, user core.Authenticable) core.OwnershipInfo {
	if user == nil {
		return core.OwnershipInfo{IsOwner: false, AccessLevel: "guest"}
	}

	userID := user.GetId()
	userRoles := []string{}
	if roleProvider, ok := user.(core.RoleProvider); ok {
		userRoles = roleProvider.GetRoles()
	}

	// Check if user is the employee themselves
	if e.ID == userID {
		return core.OwnershipInfo{
			IsOwner:     true,
			OwnerId:     e.ID,
			AccessLevel: "self",
			CustomAccessData: map[string]any{
				"can_edit":   true,
				"can_delete": false,
			},
		}
	}

	// Check if user is the manager
	if e.ManagerID == userID {
		return core.OwnershipInfo{
			IsOwner:     false,
			OwnerId:     e.ID,
			AccessLevel: "manager",
			CustomAccessData: map[string]any{
				"can_edit":            true,
				"can_delete":          false,
				"can_see_performance": true,
			},
		}
	}

	// Check roles
	for _, role := range userRoles {
		switch role {
		case "admin":
			return core.OwnershipInfo{
				IsOwner:     false,
				OwnerId:     e.ID,
				AccessLevel: "admin",
				CustomAccessData: map[string]any{
					"can_edit":       true,
					"can_delete":     true,
					"can_see_salary": true,
					"can_see_ssn":    true,
				},
			}
		case "hr":
			return core.OwnershipInfo{
				IsOwner:     false,
				OwnerId:     e.ID,
				AccessLevel: "hr",
				CustomAccessData: map[string]any{
					"can_edit":       true,
					"can_delete":     false,
					"can_see_salary": true,
					"can_see_ssn":    true,
				},
			}
		case "senior_manager":
			return core.OwnershipInfo{
				IsOwner:     false,
				OwnerId:     e.ID,
				AccessLevel: "senior_manager",
				CustomAccessData: map[string]any{
					"can_edit":       false,
					"can_delete":     false,
					"can_see_salary": true,
					"can_see_ssn":    false,
				},
			}
		}
	}

	return core.OwnershipInfo{
		IsOwner:     false,
		OwnerId:     e.ID,
		AccessLevel: "viewer",
		CustomAccessData: map[string]any{
			"can_edit":   false,
			"can_delete": false,
		},
	}
}

// Test function to validate access control scenarios
func TestAccessControlScenarios(t *testing.T) {
	// Create test users
	employee := &TestUser{
		ID:         "emp123",
		Username:   "john_employee",
		Email:      "john@company.com",
		Roles:      []string{"employee"},
		Department: "engineering",
	}

	manager := &TestUser{
		ID:         "mgr456",
		Username:   "jane_manager",
		Email:      "jane@company.com",
		Roles:      []string{"manager"},
		Department: "engineering",
	}

	seniorManager := &TestUser{
		ID:         "smgr789",
		Username:   "bob_senior",
		Email:      "bob@company.com",
		Roles:      []string{"senior_manager"},
		Department: "engineering",
	}

	hrUser := &TestUser{
		ID:         "hr001",
		Username:   "alice_hr",
		Email:      "alice@company.com",
		Roles:      []string{"hr"},
		Department: "hr",
	}

	admin := &TestUser{
		ID:         "admin001",
		Username:   "admin_user",
		Email:      "admin@company.com",
		Roles:      []string{"admin"},
		Department: "it",
	}

	// Create employee record
	employeeRecord := &Employee{
		ID:          "emp123",
		Name:        "John Employee",
		Email:       "john@company.com",
		Department:  "engineering",
		ManagerID:   "mgr456",
		Salary:      75000.0,
		SSN:         "123-45-6789",
		Performance: "Good",
		IsActive:    true,
	}

	// Create access control configuration
	config := NewAccessControlBuilder().
		AllowGuestAccess().
		SetDefaultRoles("employee").
		AddDomainOperation("read", true, "employee", "manager", "hr", "admin", "senior_manager").
		AddDomainOperation("read_self", true, "self").
		AddDomainOperation("read_manager", true, "manager").
		AddDomainOperation("read_hr", true, "hr").
		AddDomainOperation("read_admin", true, "admin").
		AddDomainOperation("read_senior_manager", true, "senior_manager").
		AddFieldAccess("salary", map[string]OperationConfig{
			"read": {Allowed: true, RequiredRoles: []string{"hr", "admin", "senior_manager"}},
		}).
		AddFieldAccess("ssn", map[string]OperationConfig{
			"read": {Allowed: true, RequiredRoles: []string{"hr", "admin"}},
		}).
		AddFieldAccess("performance", map[string]OperationConfig{
			"read": {Allowed: true, RequiredRoles: []string{"self", "manager", "hr", "admin", "senior_manager"}},
		}).
		Build()

	mockAuthContext := &MockAuthContext{}
	accessControl := NewRoleBasedAccessControl(config, mockAuthContext)

	// Test scenarios
	testScenarios := []struct {
		name     string
		user     *TestUser
		expected map[string]bool
	}{
		{
			name: "Employee viewing their own record",
			user: employee,
			expected: map[string]bool{
				"id": true, "name": true, "email": true, "department": true,
				"performance": true, "is_active": true,
				"salary": false, "ssn": false, "manager_id": false,
			},
		},
		{
			name: "Manager viewing employee record",
			user: manager,
			expected: map[string]bool{
				"id": true, "name": true, "email": true, "department": true,
				"performance": true, "is_active": true,
				"salary": false, "ssn": false, "manager_id": false,
			},
		},
		{
			name: "Senior Manager viewing employee record",
			user: seniorManager,
			expected: map[string]bool{
				"id": true, "name": true, "email": true, "department": true,
				"performance": true, "is_active": true, "salary": true,
				"ssn": false, "manager_id": true,
			},
		},
		{
			name: "HR viewing employee record",
			user: hrUser,
			expected: map[string]bool{
				"id": true, "name": true, "email": true, "department": true,
				"performance": true, "is_active": true, "salary": true,
				"ssn": true, "manager_id": true,
			},
		},
		{
			name: "Admin viewing employee record",
			user: admin,
			expected: map[string]bool{
				"id": true, "name": true, "email": true, "department": true,
				"performance": true, "is_active": true, "salary": true,
				"ssn": true, "manager_id": true,
			},
		},
	}

	// Run tests
	for _, scenario := range testScenarios {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), "current_user", scenario.user)

			// Test field access
			for field, expected := range scenario.expected {
				actual := accessControl.CanReadField(ctx, field, employeeRecord)
				if actual != expected {
					t.Errorf("Field %s: expected %v, got %v", field, expected, actual)
				}
			}

			// Test domain access
			canAccess := accessControl.CanAccessEntity(ctx, employeeRecord, "read")
			if !canAccess {
				t.Errorf("Expected user to be able to access domain, but got false")
			}

			// Test readable fields
			readableFields := accessControl.GetReadableFields(ctx, employeeRecord)
			fmt.Printf("\n%s can read fields: %v\n", scenario.name, readableFields)
		})
	}
}

// Test function to demonstrate filtering records based on access
func TestRecordFiltering(t *testing.T) {
	// Create multiple employee records
	employees := []*Employee{
		{
			ID: "emp001", Name: "Alice", Department: "engineering", ManagerID: "mgr001",
			Salary: 80000, SSN: "111-11-1111", Performance: "Excellent", IsActive: true,
		},
		{
			ID: "emp002", Name: "Bob", Department: "marketing", ManagerID: "mgr002",
			Salary: 70000, SSN: "222-22-2222", Performance: "Good", IsActive: true,
		},
		{
			ID: "emp003", Name: "Charlie", Department: "engineering", ManagerID: "mgr001",
			Salary: 90000, SSN: "333-33-3333", Performance: "Outstanding", IsActive: false,
		},
	}

	// Create manager who can only see their own employees
	manager := &TestUser{
		ID:       "mgr001",
		Username: "manager1",
		Roles:    []string{"manager"},
	}

	// Create access control
	config := NewAccessControlBuilder().
		AllowGuestAccess().
		SetDefaultRoles("employee").
		AddDomainOperation("read", true, "employee", "manager", "hr", "admin").
		Build()

	mockAuthContext := &MockAuthContext{}
	accessControl := NewRoleBasedAccessControl(config, mockAuthContext)
	ctx := context.WithValue(context.Background(), "current_user", manager)

	// Convert to []any for testing
	entities := make([]any, len(employees))
	for i, emp := range employees {
		entities[i] = emp
	}

	// Test filtering
	accessibleEntities := accessControl.GetAccessibleEntities(ctx, entities, "read")

	fmt.Printf("\nManager can access %d out of %d employee records\n", len(accessibleEntities), len(entities))

	// The manager should only be able to access employees they manage
	// (This would depend on the specific implementation of CanAccessEntity)
}

// PublicDocument represents a public document with flexible access control
type PublicDocument struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
	AuthorID    string `json:"author_id"`
	IsPublic    bool   `json:"is_public"`
	IsDraft     bool   `json:"is_draft"`
	ViewCount   int    `json:"view_count"`
	AuthorEmail string `json:"author_email"`
}

// Implement AccessibleEntity for public documents
func (d *PublicDocument) GetAccessibleFields(ctx context.Context, user core.Authenticable, operation string) []string {
	var accessibleFields []string

	// Everyone can see basic info if document is public
	if d.IsPublic {
		accessibleFields = append(accessibleFields, "id", "title", "content", "view_count")
	}

	if user == nil {
		return accessibleFields
	}

	userID := user.GetId()
	userRoles := []string{}
	if roleProvider, ok := user.(core.RoleProvider); ok {
		userRoles = roleProvider.GetRoles()
	}

	// Author can see everything
	if d.AuthorID == userID {
		accessibleFields = append(accessibleFields, "author_id", "author_email", "is_public", "is_draft")
		return accessibleFields
	}

	// Admins can see everything
	for _, role := range userRoles {
		if role == "admin" {
			accessibleFields = append(accessibleFields, "author_id", "author_email", "is_public", "is_draft")
			return accessibleFields
		}
	}

	return accessibleFields
}

func (d *PublicDocument) CanAccessField(ctx context.Context, user core.Authenticable, field string, operation string) bool {
	accessibleFields := d.GetAccessibleFields(ctx, user, operation)
	for _, accessibleField := range accessibleFields {
		if accessibleField == field {
			return true
		}
	}
	return false
}

func (d *PublicDocument) GetOwnershipInfo(ctx context.Context, user core.Authenticable) core.OwnershipInfo {
	if user == nil {
		return core.OwnershipInfo{IsOwner: false, AccessLevel: "guest"}
	}

	userID := user.GetId()
	userRoles := []string{}
	if roleProvider, ok := user.(core.RoleProvider); ok {
		userRoles = roleProvider.GetRoles()
	}

	if d.AuthorID == userID {
		return core.OwnershipInfo{
			IsOwner:     true,
			OwnerId:     d.AuthorID,
			AccessLevel: "author",
		}
	}

	for _, role := range userRoles {
		if role == "admin" {
			return core.OwnershipInfo{
				IsOwner:     false,
				OwnerId:     d.AuthorID,
				AccessLevel: "admin",
			}
		}
	}

	return core.OwnershipInfo{
		IsOwner:     false,
		OwnerId:     d.AuthorID,
		AccessLevel: "viewer",
	}
}

// TestPublicRecordsWithAuthorization demonstrates public records with authorization
func TestPublicRecordsWithAuthorization(t *testing.T) {

	// Create a public document
	document := &PublicDocument{
		ID:          "doc001",
		Title:       "Public Article",
		Content:     "This is a public article content",
		AuthorID:    "author123",
		IsPublic:    true,
		IsDraft:     false,
		ViewCount:   150,
		AuthorEmail: "author@example.com",
	}

	// Create access control for public documents
	config := NewAccessControlBuilder().
		AllowGuestAccess(). // Allow guests to see public documents
		SetDefaultRoles("viewer").
		AddDomainOperation("read", true, "guest", "viewer", "author", "admin").
		AddDomainOperation("read_author", true, "author").
		AddDomainOperation("read_admin", true, "admin").
		Build()

	mockAuthContext := &MockAuthContext{}
	accessControl := NewRoleBasedAccessControl(config, mockAuthContext)

	// Test with guest user (no authentication)
	guestCtx := context.Background()
	guestCanRead := accessControl.CanReadField(guestCtx, "title", document)
	fmt.Printf("Guest can read title: %v\n", guestCanRead) // Should be true

	guestCanReadEmail := accessControl.CanReadField(guestCtx, "author_email", document)
	fmt.Printf("Guest can read author email: %v\n", guestCanReadEmail) // Should be false

	// Test with authenticated user
	user := &TestUser{
		ID:       "user456",
		Username: "regular_user",
		Roles:    []string{"viewer"},
	}

	userCtx := context.WithValue(context.Background(), "current_user", user)
	userCanRead := accessControl.CanReadField(userCtx, "content", document)
	fmt.Printf("User can read content: %v\n", userCanRead) // Should be true

	// Test with author
	author := &TestUser{
		ID:       "author123",
		Username: "author_user",
		Roles:    []string{"author"},
	}

	authorCtx := context.WithValue(context.Background(), "current_user", author)
	authorCanReadEmail := accessControl.CanReadField(authorCtx, "author_email", document)
	fmt.Printf("Author can read their email: %v\n", authorCanReadEmail) // Should be true

	authorCanReadDraft := accessControl.CanReadField(authorCtx, "is_draft", document)
	fmt.Printf("Author can read draft status: %v\n", authorCanReadDraft) // Should be true
}
