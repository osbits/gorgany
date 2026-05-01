package fixturedomain

import (
	"time"

	"github.com/osbits/gorgany/db/orm"
)

type Tag struct {
	orm.BaseEntity `json:"-"`
	ID             string    `json:"id" gorm:"primaryKey;column:id"`
	Name           string    `json:"name" gorm:"column:name;uniqueIndex;not null"`
	CreatedAt      time.Time `json:"createdAt" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time `json:"updatedAt" gorm:"column:updated_at;autoUpdateTime"`
}

func (Tag) TableName() string {
	return "fixture_tags"
}
