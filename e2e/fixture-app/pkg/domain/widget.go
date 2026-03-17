package fixturedomain

import (
	"time"

	"github.com/osbits/gorgany/db/orm"
)

type Widget struct {
	orm.BaseEntity `json:"-"`
	ID             string    `json:"id" gorm:"primaryKey;column:id"`
	Name           string    `json:"name" gorm:"column:name;not null"`
	Description    string    `json:"description" gorm:"column:description;not null"`
	CreatedBy      string    `json:"createdBy" gorm:"column:created_by;not null"`
	Tags           []*Tag    `json:"tags,omitempty" gorm:"many2many:fixture_widget_tags;"`
	CreatedAt      time.Time `json:"createdAt" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt      time.Time `json:"updatedAt" gorm:"column:updated_at;autoUpdateTime"`
}

func (Widget) TableName() string {
	return "fixture_widgets"
}
