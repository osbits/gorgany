# Access Control System Improvements

## Overview

The access control system has been significantly improved to address the limitations in the original implementation. The main issues that were fixed:

1. **Entity Type Constraint**: Entities were previously limited to `core.Authenticable`, but now any entity type can be used
2. **Rigid Ownership Model**: The `OwnerProvider` interface was too restrictive, only supporting a single `OwnerId` field

## Key Improvements

### 1. Flexible Entity Types

**Before:**
```go
// Limited to core.Authenticable entities only
CanReadField(ctx context.Context, field string, entity core.Authenticable) bool
GetReadableFields(ctx context.Context, entity core.Authenticable) []string
```

**After:**
```go
// Works with any domain type
CanReadField(ctx context.Context, field string, entity any) bool
GetReadableFields(ctx context.Context, entity any) []string
```

### 2. Flexible Ownership System

**Before:**
```go
// Only supported single OwnerId field
type OwnerProvider interface {
    GetOwnerId() string
}
```

**After:**
```go
// New flexible ownership interface
type AccessibleEntity interface {
    GetAccessibleFields(ctx context.Context, user Authenticable, operation string) []string
    CanAccessField(ctx context.Context, user Authenticable, field string, operation string) bool
    GetOwnershipInfo(ctx context.Context, user Authenticable) OwnershipInfo
}

type OwnershipInfo struct {
    IsOwner bool
    OwnerId string
    AccessLevel string  // e.g., "owner", "admin", "viewer", "department_member"
    CustomAccessData map[string]any
}
```

### 3. Enhanced Access Control Interface

The `AccessControl` interface now includes additional methods for more comprehensive access control:

```go
type AccessControl interface {
    // Existing methods...
    ValidateFieldAccess(ctx context.Context, field string, operation string) error
    ValidateFilterAccess(ctx context.Context, field string, operator string) error
    ValidateSortAccess(ctx context.Context, field string) error
    GetUserRoles(ctx context.Context) []string
    IsGuest(ctx context.Context) bool
    GetCurrentUser(ctx context.Context) core.Authenticable
    CanReadField(ctx context.Context, field string, entity any) bool
    GetReadableFields(ctx context.Context, entity any) []string
    
    // New methods for enhanced access control
    CanAccessEntity(ctx context.Context, entity any, operation string) bool
    GetAccessibleEntities(ctx context.Context, entities []any, operation string) []any
}
```

## Usage Examples

### 1. Basic Entity with Flexible Access Control

```go
type Document struct {
    ID          string `json:"id"`
    Title       string `json:"title"`
    Content     string `json:"content"`
    OwnerID     string `json:"owner_id"`
    Department  string `json:"department"`
    IsPublic    bool   `json:"is_public"`
    IsConfidential bool `json:"is_confidential"`
}

// Implement AccessibleEntity for flexible access control
func (d *Document) GetAccessibleFields(ctx context.Context, user core.Authenticable, operation string) []string {
    var accessibleFields []string
    
    // Always accessible fields
    accessibleFields = append(accessibleFields, "id", "title")
    
    ownershipInfo := d.GetOwnershipInfo(ctx, user)
    
    // Owner can access all fields
    if ownershipInfo.IsOwner {
        accessibleFields = append(accessibleFields, "content", "owner_id", "department", "is_public", "is_confidential")
        return accessibleFields
    }
    
    // Department members can access non-confidential content
    if ownershipInfo.AccessLevel == "department_member" {
        accessibleFields = append(accessibleFields, "content", "department", "is_public")
        if !d.IsConfidential {
            accessibleFields = append(accessibleFields, "is_confidential")
        }
        return accessibleFields
    }
    
    // Public documents are accessible to everyone
    if d.IsPublic {
        accessibleFields = append(accessibleFields, "content", "is_public")
    }
    
    return accessibleFields
}

func (d *Document) GetOwnershipInfo(ctx context.Context, user core.Authenticable) core.OwnershipInfo {
    if user == nil {
        return core.OwnershipInfo{IsOwner: false, AccessLevel: "guest"}
    }
    
    userRoles := user.GetRoles()
    userID := user.GetId()
    
    // Check if user is owner
    if d.OwnerID == userID {
        return core.OwnershipInfo{
            IsOwner: true,
            OwnerId: d.OwnerID,
            AccessLevel: "owner",
            CustomAccessData: map[string]any{
                "can_edit": true,
                "can_delete": true,
            },
        }
    }
    
    // Check if user is admin
    for _, role := range userRoles {
        if role == "admin" {
            return core.OwnershipInfo{
                IsOwner: false,
                OwnerId: d.OwnerID,
                AccessLevel: "admin",
                CustomAccessData: map[string]any{
                    "can_edit": true,
                    "can_delete": true,
                },
            }
        }
    }
    
    // Check if user is in the same department
    for _, role := range userRoles {
        if role == "department_member" {
            return core.OwnershipInfo{
                IsOwner: false,
                OwnerId: d.OwnerID,
                AccessLevel: "department_member",
                CustomAccessData: map[string]any{
                    "can_edit": false,
                    "can_delete": false,
                    "department": d.Department,
                },
            }
        }
    }
    
    return core.OwnershipInfo{
        IsOwner: false,
        OwnerId: d.OwnerID,
        AccessLevel: "viewer",
    }
}
```

### 2. Using the Enhanced Access Control

```go
// Create access control configuration
config := model.NewAccessControlBuilder().
    AllowGuestAccess().
    SetDefaultRoles("user").
    AddDomainOperation("read", true, "user", "admin").
    AddDomainOperation("read_owner", true, "owner").
    AddDomainOperation("read_department_member", true, "department_member").
    AddFieldAccess("content", map[string]model.OperationConfig{
        "read": {Allowed: true, RequiredRoles: []string{"owner", "admin", "department_member"}},
    }).
    Build()

// Note: In real usage, authContext would be injected via IoC container
authContext := &YourAuthContext{} // This would be injected
accessControl := model.NewRoleBasedAccessControl(config, authContext)

// Test field access
canReadContent := accessControl.CanReadField(ctx, "content", document)
canAccessEntity := accessControl.CanAccessEntity(ctx, document, "read")
readableFields := accessControl.GetReadableFields(ctx, document)

// Filter entities based on access
allDocuments := []any{document1, document2, document3}
accessibleDocuments := accessControl.GetAccessibleEntities(ctx, allDocuments, "read")
```

## Benefits

### 1. **Flexibility**
- Entities can implement custom access control logic
- Support for complex ownership patterns (department-based, role-based, etc.)
- Custom access levels and permissions

### 2. **Extensibility**
- Easy to add new access patterns without changing core interfaces
- Support for custom access data and context
- Backward compatible with existing `OwnerProvider` interface

### 3. **Performance**
- Efficient field filtering using map lookups
- Minimal overhead on response generation
- Caches access control configuration

### 4. **Codegen Friendly**
- Works seamlessly with generated code
- No need to pass `core.Authenticable` parameters
- Context-based user retrieval

## Migration Guide

### For Existing Entities

If you have existing entities that use the old `OwnerProvider` interface, they will continue to work without changes. The new system falls back to the old interface when the new `AccessibleEntity` interface is not implemented.

### For New Entities

For new entities that need flexible access control, implement the `AccessibleEntity` interface:

```go
type MyEntity struct {
    // Your fields...
}

func (e *MyEntity) GetAccessibleFields(ctx context.Context, user core.Authenticable, operation string) []string {
    // Your custom logic...
}

func (e *MyEntity) CanAccessField(ctx context.Context, user core.Authenticable, field string, operation string) bool {
    // Your custom logic...
}

func (e *MyEntity) GetOwnershipInfo(ctx context.Context, user core.Authenticable) core.OwnershipInfo {
    // Your custom logic...
}
```

### For Generated Code

The codegen templates have been updated to use the new interface. No changes are needed in your existing generated code, but new generations will benefit from the improved flexibility.

## Configuration

The access control configuration remains the same, but now supports additional operation types for different access levels:

```yaml
api:
  access:
    domains:
      document:
        forGuest: false
        roles: ["user", "admin"]
        
        operations:
          read:
            allowed: true
            roles: ["user", "admin"]
          read_owner:
            allowed: true
            roles: ["owner"]
          read_department_member:
            allowed: true
            roles: ["department_member"]
          read_admin:
            allowed: true
            roles: ["admin"]
        
        fields:
          content:
            read: ["owner", "admin", "department_member"]
            write: ["owner", "admin"]
          is_confidential:
            read: ["owner", "admin"]
            write: ["owner", "admin"]
```

This configuration allows for fine-grained control over different access levels and operations.
