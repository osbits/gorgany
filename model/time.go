package model

import (
	"fmt"
	"gorgany/app/core"
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
	return []byte(fmt.Sprintf(`"%s"`, c.Time.Format(core.GlobalDateTimeFormat))), nil
}

func (c *FormDateTimeLocal) UnmarshalJSON(b []byte) (err error) {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		return
	}
	c.Time, err = time.Parse(core.GlobalDateTimeFormat, s)
	return
}

type FormDateLocal struct {
	Time time.Time
}

func (c FormDateLocal) MarshalJSON() ([]byte, error) {
	if c.Time.IsZero() {
		return []byte("null"), nil
	}
	return []byte(fmt.Sprintf(`"%s"`, c.Time.Format(core.GlobalDateFormat))), nil
}

func (c *FormDateLocal) UnmarshalJSON(b []byte) (err error) {
	s := strings.Trim(string(b), `"`)
	if s == "null" {
		return
	}
	c.Time, err = time.Parse(core.GlobalDateFormat, s)
	return
}
