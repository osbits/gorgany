package model

import (
	"fmt"
	"strings"
	"time"
)

type FormDateTimeLocal struct {
	Time time.Time
}

func (c *FormDateTimeLocal) MarshalJSON() ([]byte, error) {
	if c.Time.IsZero() {
		return []byte("null"), nil
	}
	return []byte(fmt.Sprintf(`"%s"`, c.Time.Format("2006-01-02 15:04:05"))), nil
}

func (c *FormDateTimeLocal) UnmarshalJSON(b []byte) (err error) {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		return
	}
	c.Time, err = time.Parse("2006-01-02 15:04:05", s)
	return
}

type FormDateLocal struct {
	Time time.Time
}

func (c *FormDateLocal) MarshalJSON() ([]byte, error) {
	if c.Time.IsZero() {
		return []byte("null"), nil
	}
	return []byte(fmt.Sprintf(`"%s"`, c.Time.Format("2006-01-02"))), nil
}

func (c *FormDateLocal) UnmarshalJSON(b []byte) (err error) {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		return
	}
	c.Time, err = time.Parse("2006-01-02", s)
	return
}
