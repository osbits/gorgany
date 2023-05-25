package model

import "gorgany"

type Authenticable interface {
	GetUsername() string
	GetPassword() string
	GetRole() gorgany.UserRole
}
