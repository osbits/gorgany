package db

import "time"

type Seeder struct {
	Name string `gorm:"unique"`
	Date time.Time
}
