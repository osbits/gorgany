package proxy

import "context"

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
