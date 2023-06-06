package validator

import (
	"encoding/json"
	goValidator "github.com/go-playground/validator/v10"
	"gorgany/model"
	"reflect"
)

func validateLocalizedString(field reflect.Value) interface{} {
	if localizedString, ok := field.Interface().(model.LocalizedString); ok {
		localizedEntries := localizedString.Map()
		emptyText := 0
		for _, text := range localizedString.Map() {
			if text == "" {
				emptyText++
			}
		}

		if emptyText == len(localizedEntries) {
			return nil
		}

		jsonLocalizedEntries, err := json.Marshal(localizedString.Data)
		if err != nil {
			return nil
		}
		return jsonLocalizedEntries
	}
	return nil
}

func validateRequiredLocalizedString(fl goValidator.FieldLevel) bool {
	localizedEntriesRaw, ok := fl.Field().Interface().([]byte)
	if !ok {
		return false
	}
	if localizedEntriesRaw == nil {
		return false
	}

	localizedEntries := make([]*model.LocalizedStringEntry, 0)
	err := json.Unmarshal(localizedEntriesRaw, &localizedEntries)
	if err != nil {
		return false
	}

	for _, localizedEntry := range localizedEntries {
		if localizedEntry.Text == "" {
			return false
		}
	}

	return true
}
