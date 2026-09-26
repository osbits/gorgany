package domain

import (
	"time"

	"github.com/osbits/gorgany/v2/db/orm"
)

type Note struct {
	orm.BaseEntity `json:"-"`
	ID             string    `json:"id" gorm:"primaryKey;column:id"`
	Title          string    `json:"title" gorm:"column:title;not null"`
	CreatedAt      time.Time `json:"createdAt" gorm:"column:created_at;autoCreateTime"`
}

func (Note) TableName() string { return "notes" }
