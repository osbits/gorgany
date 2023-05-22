package model

type Authenticable interface {
	GetUsername() string
	GetPassword() string
	GetRole() string
}
