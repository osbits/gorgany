package http

import (
	"fmt"
	"github.com/osbits/gorgany/v2/app/core"
	"reflect"
	"strings"
	"time"
)

// sanitizeFieldName removes potentially dangerous characters from field names
func sanitizeFieldName(name string) string {
	var result []rune
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			result = append(result, r)
		}
	}
	return string(result)
}

// isTimeLikeType reports whether t is time.Time or a struct that directly contains/embeds a time.Time field
func isTimeLikeType(t reflect.Type) (bool, int, bool) {
	// returns: ok, fieldIndex, directTime
	if t == reflect.TypeOf(time.Time{}) {
		return true, -1, true
	}
	if t.Kind() != reflect.Struct {
		return false, -1, false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		ft := f.Type
		if ft == reflect.TypeOf(time.Time{}) {
			// consider either embedded or named field
			return true, i, false
		}
	}
	return false, -1, false
}

// parseTimeString tries multiple formats commonly used in the project and RFC standards
func parseTimeString(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	var t time.Time
	var err error
	// Try project-wide formats first
	layouts := []string{
		core.GlobalDateTimeFormat,
		core.GlobalDateFormat,
		time.RFC3339,
		time.RFC3339Nano,
	}
	for _, layout := range layouts {
		t, err = time.Parse(layout, s)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}

// setTimeLikeFromValue attempts to set a struct field of time-like type from a string value
// Returns true if the field was recognized as time-like and set (or cleared), false otherwise.
func setTimeLikeFromValue(field reflect.Value, value any) (bool, error) {
	// Only applicable to struct kinds (including time.Time which is also a struct)
	if field.Kind() != reflect.Struct {
		return false, nil
	}

	ok, idx, direct := isTimeLikeType(field.Type())
	if !ok {
		return false, nil
	}

	// Accept only string inputs for time parsing; nil or empty string resets to zero time
	if value == nil {
		// zero out the time-like field
		if direct {
			field.Set(reflect.Zero(field.Type()))
			return true, nil
		}
		// wrapper with inner time.Time
		inner := field.Field(idx)
		if inner.CanSet() {
			inner.Set(reflect.Zero(inner.Type()))
		}
		return true, nil
	}

	strVal, okCast := value.(string)
	if !okCast {
		// In JSON maps may provide non-string; attempt to format
		strVal = strings.TrimSpace(strings.Trim(fmt.Sprintf("%v", value), `"`))
	}
	if strVal == "" {
		if direct {
			field.Set(reflect.Zero(field.Type()))
		} else {
			inner := field.Field(idx)
			if inner.CanSet() {
				inner.Set(reflect.Zero(inner.Type()))
			}
		}
		return true, nil
	}

	t, err := parseTimeString(strVal)
	if err != nil {
		return true, err
	}

	if direct {
		field.Set(reflect.ValueOf(t))
		return true, nil
	}
	inner := field.Field(idx)
	if inner.CanSet() {
		inner.Set(reflect.ValueOf(t))
	}
	return true, nil
}
