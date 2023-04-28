package db

import "time"

type Migration struct {
	Name string `gorm:"unique"`
	Date time.Time
}
