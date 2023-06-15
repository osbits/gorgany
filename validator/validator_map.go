package validator

import (
	"encoding/json"
	goValidator "github.com/go-playground/validator/v10"
	"reflect"
)

func validateMapStringString(field reflect.Value) interface{} {
	if m, ok := field.Interface().(map[string]string); ok {
		emptyText := 0
		for _, text := range m {
			if text == "" {
				emptyText++
			}
		}

		if emptyText == len(m) {
			return nil
		}

		entries, err := json.Marshal(m)
		if err != nil {
			return nil
		}
		return entries
	}
	return nil
}

func validateRequiredMapStringString(fl goValidator.FieldLevel) bool {
	mRaw, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
	}
	if mRaw == nil {
		return false
	}

	m := make(map[string]string)
	err := json.Unmarshal(mRaw, &m)
	if err != nil {
		return false
	}

	for _, entry := range m {
		if entry == "" {
			return false
		}
	}

	return true
}
