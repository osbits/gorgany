package fixturedomain

import (
	"time"

	"github.com/osbits/gorgany/app/core"
	"github.com/osbits/gorgany/db/orm"
)

type User struct {
	orm.BaseEntity `json:"-"`
	ID             string        `json:"id" gorm:"primaryKey;column:id"`
	Username       string        `json:"username" gorm:"column:username;uniqueIndex;not null"`
	PasswordHash   string        `json:"-" gorm:"column:password_hash;not null"`
	Role           core.UserRole `json:"role" gorm:"column:role;not null"`
	CreatedAt      time.Time     `json:"createdAt" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time     `json:"updatedAt" gorm:"column:updated_at;autoUpdateTime"`
}

func (User) TableName() string {
	return "fixture_users"
}

func (u *User) GetId() string {
	return u.ID
}

func (u *User) GetUsername() string {
	return u.Username
}

func (u *User) GetPassword() string {
	return u.PasswordHash
}

func (u *User) GetRole() core.UserRole {
	return u.Role
}
