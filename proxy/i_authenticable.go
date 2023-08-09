package proxy

type Authenticable interface {
	GetUsername() string
	GetPassword() string
	GetRole() UserRole
}

type UserRole string
