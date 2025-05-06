package core

import (
	"context"
	"time"
)

// IAuthContext defines the interface for managing authentication strategies
type IAuthContext interface {
	// RegisterAuthStrategy registers a new authentication strategy with the given name
	RegisterAuthStrategy(name string, strategy IAuthStrategy)

	// GetAuthStrategy retrieves an authentication strategy by name. If no name is provided,
	// returns the default strategy
	GetAuthStrategy(strategyName ...string) IAuthStrategy

	// Strategy is an alias for GetAuthStrategy
	Strategy(strategyName ...string) IAuthStrategy

	// ResolveAuthStrategyByContext determines the appropriate authentication strategy
	// based on the context of the current request
	ResolveAuthStrategyByContext(ctx context.Context) IAuthStrategy
}

// IAuthStrategy defines the interface for authentication strategy implementations
type IAuthStrategy interface {
	// NewSessionWithoutUser creates a new session without an associated user
	NewSessionWithoutUser(ctx context.Context) (ISession, error)

	// Login authenticates a user and creates a new session
	Login(user Authenticable, ctx context.Context) (ISession, error)

	// IsLoggedIn checks if there is an active authenticated session in the context
	IsLoggedIn(ctx context.Context) bool

	// Logout terminates the current session
	Logout(ctx context.Context)

	// CurrentUser retrieves the authenticated user from the context
	CurrentUser(ctx context.Context) (Authenticable, error)

	// ResolveSessionId extracts the session ID from the context
	ResolveSessionId(ctx context.Context) string

	// IsRequestMadeWithStrategy checks if the current request was made using this strategy
	IsRequestMadeWithStrategy(ctx context.Context) bool

	// CurrentSession retrieves the current session from the context
	CurrentSession(ctx context.Context) ISession
}

// ISessionStorage defines the interface for session storage management
type ISessionStorage interface {
	// ClearExpiredSessions removes all expired sessions from storage
	ClearExpiredSessions()

	// AddSession stores a new session
	AddSession(session ISession)

	// DeleteSession removes a specific session from storage
	DeleteSession(session ISession)

	// DeleteSessionById removes a session by its ID
	DeleteSessionById(id string)

	// GetSessionById retrieves a session by its ID
	GetSessionById(id string) ISession

	// SetSessionLifetime configures the duration for which sessions remain valid
	SetSessionLifetime(lifetime time.Duration)

	// GetSessionLifetime returns the current session lifetime duration
	GetSessionLifetime() time.Duration
}

// ISession defines the interface for session management
type ISession interface {
	ISimpleStorage
	// GetId returns the unique identifier of the session
	GetId() string

	// GetExpiry returns the time when the session will expire
	GetExpiry() time.Time

	// GetUserId returns the ID of the user associated with this session
	GetUserId() string

	// SetUserId associates a user with this session
	SetUserId(id string)

	// IsExpired checks if the session has expired
	IsExpired() bool

	// SetExpiry updates the session expiration time
	SetExpiry(t time.Time)
}

// Authenticable defines the interface for user authentication
type Authenticable interface {
	// GetId returns the unique identifier of the user
	GetId() string

	// GetUsername returns the username of the user
	GetUsername() string

	// GetPassword returns the hashed password of the user
	GetPassword() string

	// GetRole returns the role of the user
	GetRole() UserRole
}

type UserRole string

// IUserService defines the interface for user management
type IUserService interface {
	// Get retrieves a user by their ID
	Get(id any) (Authenticable, error)

	// GetByUsername retrieves a user by their username
	GetByUsername(username string) (Authenticable, error)

	// Save persists a user entity
	Save(authEntity Authenticable) error
}

// Policy defines the interface for access control policies
type Policy[T any] interface {
	// AddFilter applies policy filters to an ORM query builder
	AddFilter(ctx context.Context, builder IOrm[T]) (bool, IOrm[T])

	// AddFilterForBuilder applies policy filters to a generic query builder
	AddFilterForBuilder(ctx context.Context, builder IQueryBuilder) (bool, IQueryBuilder)

	// Create determines if the current context can create new instances
	Create(ctx context.Context) bool

	// ShowAny determines if the current context can view any instances
	ShowAny(ctx context.Context) bool

	// Show determines if the current context can view a specific instance
	Show(ctx context.Context, model any) bool

	// Update determines if the current context can update a specific instance
	Update(ctx context.Context, model any) bool

	// Delete determines if the current context can delete a specific instance
	Delete(ctx context.Context, model any) bool
}
