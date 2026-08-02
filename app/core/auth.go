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
	// Logout terminates the current session.
	//
	// It returns an error when the server-side session could not be revoked. The
	// caller must treat that as a failed logout and say so: the previous signature
	// returned nothing, so a strategy that could not delete the session had no way to
	// report it and expired the browser's cookie anyway — which tells the user they
	// are logged out while the session, and any copy of the cookie, keeps working.
	Logout(ctx context.Context) error
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

// ISessionStorage defines the interface for session storage management.
//
// Every method that changes stored state reports failure, and the read distinguishes
// "no such session" from "the store could not answer". None of them used to: a store
// that could not delete, could not persist or could not be reached looked exactly like
// one that had done the work, so login reported success for a session nothing held and
// logout reported success for a session that was still live. A caller that has nowhere
// to propagate the error must still fail closed — refuse the request, not trust the
// session — and report through err.HandleError rather than discarding it.
type ISessionStorage interface {
	// ClearExpiredSessions removes all expired sessions
	ClearExpiredSessions() error
	// AddSession adds a new session to storage, or persists the current state of one
	// the store already holds. It must refuse to recreate a session the store no
	// longer has: an already-revoked identifier that can be written back is a
	// revocation bypass.
	AddSession(session ISession) error
	// DeleteSession removes a session from storage
	DeleteSession(session ISession) error
	// DeleteSessionById removes a session by its ID. Removing a session the store does
	// not hold is not an error — revocation is idempotent.
	DeleteSessionById(id string) error
	// GetSessionById retrieves a session by its ID. It returns (nil, nil) when there is
	// no such session and (nil, err) when the lookup itself failed, which a caller
	// must not read as "not logged in" without saying so.
	GetSessionById(id string) (ISession, error)
	// SetSessionLifetime sets the lifetime duration for sessions
	SetSessionLifetime(lifetime time.Duration)
	// GetSessionLifetime returns the current session lifetime duration
	GetSessionLifetime() time.Duration
	// GetSessionRotationInterval returns the duration after which session rotation should occur.
	GetSessionRotationInterval() time.Duration
	// GetSessionActivityTimeout returns the duration of inactivity after which a session is considered expired.
	GetSessionActivityTimeout() time.Duration
}

// ISessionRevoker is an optional interface a session storage may implement to report
// whether a revocation removed anything.
//
// It exists for session rotation. Rotation carries the old session's user id over to a
// new identifier, and it must only do that while the old session is still live: if the
// user logged out between the moment the request loaded the session and the moment it
// rotated, an unconditional rotation mints a brand new, durable session carrying the
// logged-out user's identity and hands its cookie to whoever made the request. Plain
// DeleteSessionById cannot express the difference, because deleting an absent session
// is deliberately not an error.
//
// Optional rather than part of ISessionStorage so a storage that cannot answer the
// question keeps compiling; both storages the framework ships implement it.
type ISessionRevoker interface {
	// RevokeSession deletes the session with this id and reports whether the store
	// held it. (false, nil) means there was nothing to revoke.
	RevokeSession(id string) (bool, error)
}

// ISession defines the interface for session management.
//
// The setters are void even though a session backed by a database persists on every
// write. That is a deliberate boundary, not an oversight: giving them errors would
// break every request and view scope and every test double in every downstream app for
// a signal almost no caller is in a position to act on. The consequence is that an
// implementation whose write-through can fail must remember the failure and surface it
// at the next operation that *can* report one — the storage call, or Login/Logout — and
// callers of those must fail closed rather than assume the write landed. Every field an
// implementation shares between concurrent requests must also be synchronised;
// GetUserId in particular feeds authorization decisions.
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
	// GetId returns the domain's unique identifier
	GetId() string
	// GetUsername returns the domain's username
	GetUsername() string
	// GetPassword returns the domain's password
	GetPassword() string
	// GetRole returns the domain's user role
	GetRole() UserRole
}

// RoleProvider defines the interface for entities that can provide roles
type RoleProvider interface {
	// GetRoles returns the roles for this domain
	GetRoles() []string
}

// OwnerProvider defines the interface for entities that can provide ownership information
type OwnerProvider interface {
	// GetOwnerId returns the ID of the domain's owner
	GetOwnerId() string
}

// AccessibleEntity defines the interface for entities that can provide access control information
type AccessibleEntity interface {
	// GetAccessibleFields returns the fields that the given user can access for this domain
	GetAccessibleFields(ctx context.Context, user Authenticable, operation string) []string

	// CanAccessField checks if the given user can access a specific field for this domain
	CanAccessField(ctx context.Context, user Authenticable, field string, operation string) bool

	// GetOwnershipInfo returns ownership information for access control decisions
	GetOwnershipInfo(ctx context.Context, user Authenticable) OwnershipInfo
}

// OwnershipInfo provides flexible ownership information for access control
type OwnershipInfo struct {
	// IsOwner indicates if the user owns this domain
	IsOwner bool

	// OwnerId is the ID of the domain owner (if applicable)
	OwnerId string

	// AccessLevel indicates the level of access (e.g., "owner", "admin", "viewer")
	AccessLevel string

	// CustomAccessData provides additional context for access decisions
	CustomAccessData map[string]any
}

// UserRole represents a user's role in the system
type UserRole string

// IUserService defines the interface for user management
type IUserService interface {
	// Get retrieves a user by ID
	Get(id any) (Authenticable, error)
	// GetByUsername retrieves a user by username
	GetByUsername(username string) (Authenticable, error)
	// Save persists a user domain
	Save(authEntity Authenticable) error
}
