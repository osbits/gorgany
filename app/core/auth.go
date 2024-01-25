package core

import (
	"context"
	"time"
)

type IAuthStrategy interface {
	NewSessionWithoutUser(ctx context.Context) (ISession, error)
	Login(user Authenticable, ctx context.Context) (ISession, error)
	IsLoggedIn(ctx context.Context) bool
	Logout(ctx context.Context)
	CurrentUser(ctx context.Context) (Authenticable, error)
	ResolveSessionId(ctx context.Context) string
	IsRequestMadeWithStrategy(ctx context.Context) bool
	CurrentSession(ctx context.Context) ISession
}

type ISessionStorage interface {
	ClearExpiredSessions()
	AddSession(session ISession)
	DeleteSession(session ISession)
	DeleteSessionById(id string)
	GetSessionById(id string) ISession
}

type ISession interface {
	ISimpleStorage
	GetId() string
	GetExpiry() time.Time
	GetUsername() string
	SetUsername(user string)
	IsExpired() bool
	SetExpiry(t time.Time)
}

type Authenticable interface {
	GetId() string
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

type Policy[T any] interface {
	AddFilter(ctx context.Context, builder IOrm[T]) (bool, IOrm[T])
	AddFilterForBuilder(ctx context.Context, builder IQueryBuilder) (bool, IQueryBuilder)
	Create(ctx context.Context) bool
	ShowAny(ctx context.Context) bool
	Show(ctx context.Context, model any) bool
	Update(ctx context.Context, model any) bool
	Delete(ctx context.Context, model any) bool
}
