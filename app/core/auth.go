package core

import (
	"context"
	"time"
)

// IAuthContext defines the interface for authentication context management
type IAuthContext interface {
	// RegisterAuthStrategy registers a new authentication strategy
	RegisterAuthStrategy(name string, strategy IAuthStrategy)
	// GetAuthStrategy retrieves an authentication strategy by name
	GetAuthStrategy(strategyName ...string) IAuthStrategy
	// Strategy is an alias for GetAuthStrategy
	Strategy(strategyName ...string) IAuthStrategy
	// ResolveAuthStrategyByContext determines the appropriate auth strategy from context
	ResolveAuthStrategyByContext(ctx context.Context) IAuthStrategy
}

// IAuthStrategy defines the interface for authentication strategies
type IAuthStrategy interface {
	// NewSessionWithoutUser creates a new session without an associated user
	NewSessionWithoutUser(ctx context.Context) (ISession, error)
	// Login authenticates a user and creates a new session
	Login(user Authenticable, ctx context.Context) (ISession, error)
	// IsLoggedIn checks if there is an active session in the context
	IsLoggedIn(ctx context.Context) bool
	// Logout terminates the current session
	Logout(ctx context.Context)
	// CurrentUser retrieves the authenticated user from the context
	CurrentUser(ctx context.Context) (Authenticable, error)
	// ResolveSessionId extracts the session ID from the context
	ResolveSessionId(ctx context.Context) string
	// IsRequestMadeWithStrategy checks if the request was made using this strategy
	IsRequestMadeWithStrategy(ctx context.Context) bool
	// CurrentSession retrieves the current session from the context
	CurrentSession(ctx context.Context) ISession
	// ShouldRotateSession checks if the session should be rotated
	ShouldRotateSession(session ISession) bool
	// RotateSession creates a new session and deletes the old one
	RotateSession(ctx context.Context, oldSession ISession) (ISession, error)
}

// ISessionStorage defines the interface for session storage management
type ISessionStorage interface {
	// ClearExpiredSessions removes all expired sessions
	ClearExpiredSessions()
	// AddSession adds a new session to storage
	AddSession(session ISession)
	// DeleteSession removes a session from storage
	DeleteSession(session ISession)
	// DeleteSessionById removes a session by its ID
	DeleteSessionById(id string)
	// GetSessionById retrieves a session by its ID
	GetSessionById(id string) ISession
	// SetSessionLifetime sets the lifetime duration for sessions
	SetSessionLifetime(lifetime time.Duration)
	// GetSessionLifetime returns the current session lifetime duration
	GetSessionLifetime() time.Duration
}

// ISession defines the interface for session management
type ISession interface {
	ISimpleStorage
	// GetId returns the session identifier
	GetId() string
	// GetExpiry returns the session expiration time
	GetExpiry() time.Time
	// GetUserId returns the ID of the user associated with this session
	GetUserId() string
	// SetUserId sets the user ID for this session
	SetUserId(id string)
	// IsExpired checks if the session has expired
	IsExpired() bool
	// SetExpiry sets the expiration time for this session
	SetExpiry(t time.Time)
	// GetCreatedAt returns when the session was created
	GetCreatedAt() time.Time
	// GetLastActivity returns when the session was last active
	GetLastActivity() time.Time
	// SetLastActivity sets the session's last activity time
	SetLastActivity(t time.Time)
}

// Authenticable defines the interface for authenticatable entities
type Authenticable interface {
	// GetId returns the entity's unique identifier
	GetId() string
	// GetUsername returns the entity's username
	GetUsername() string
	// GetPassword returns the entity's password
	GetPassword() string
	// GetRole returns the entity's user role
	GetRole() UserRole
}

// RoleProvider defines the interface for entities that can provide roles
type RoleProvider interface {
	// GetRoles returns the roles for this entity
	GetRoles() []string
}

// OwnerProvider defines the interface for entities that can provide ownership information
type OwnerProvider interface {
	// GetOwnerId returns the ID of the entity's owner
	GetOwnerId() string
}

// UserRole represents a user's role in the system
type UserRole string

// IUserService defines the interface for user management
type IUserService interface {
	// Get retrieves a user by ID
	Get(id any) (Authenticable, error)
	// GetByUsername retrieves a user by username
	GetByUsername(username string) (Authenticable, error)
	// Save persists a user entity
	Save(authEntity Authenticable) error
}

// Policy defines the interface for access control policies
type Policy[T any] interface {
	// AddFilter adds a filter to the ORM query based on policy rules
	AddFilter(ctx context.Context, builder IOrm[T]) (bool, IOrm[T])
	// AddFilterForBuilder adds a filter to the query builder based on policy rules
	AddFilterForBuilder(ctx context.Context, builder IQueryBuilder) (bool, IQueryBuilder)
	// Create checks if the user can create new instances
	Create(ctx context.Context) bool
	// ShowAny checks if the user can view any instances
	ShowAny(ctx context.Context) bool
	// Show checks if the user can view a specific instance
	Show(ctx context.Context, model any) bool
	// Update checks if the user can update a specific instance
	Update(ctx context.Context, model any) bool
	// Delete checks if the user can delete a specific instance
	Delete(ctx context.Context, model any) bool
}
