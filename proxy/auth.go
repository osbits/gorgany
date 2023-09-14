package proxy

import (
	"context"
	"time"
)

type ISessionStorage interface {
	NewSession(user Authenticable) (string, time.Time, error)
	IsLoggedIn(sessionToken string) bool
	Logout(sessionToken string)
	CurrentUser(ctx context.Context) (Authenticable, error)
	ClearExpiredSessions()
}

type Authenticable interface {
	GetUsername() string
	GetPassword() string
	GetRole() UserRole
}

type UserRole string

type IUserService interface {
	Get(id uint64) (Authenticable, error)
	GetByUsername(username string) (Authenticable, error)
	Save(authEntity Authenticable) error
}

type AuthService interface {
	CurrentUser(ctx context.Context) (Authenticable, error)
}
