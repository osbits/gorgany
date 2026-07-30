package model

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"

	"github.com/iancoleman/strcase"
)

type FieldBinder struct {
	Fields []string // if you pass `model` argument like core.LimitedFieldsMarshaller instance, the allowed fields will be obtained from instance. In another case you should to specify this field.
}

func (thiz FieldBinder) BindField(model any, field string, value any) error {
	rvModel := reflect.ValueOf(model)
	if rvModel.Kind() != reflect.Ptr {
		return fmt.Errorf("LimitedFieldsBinder: Model must be a pointer")
	}
	rvModel = util.IndirectValue(rvModel)
	if rvModel.Kind() != reflect.Struct {
		return fmt.Errorf("LimitedFieldsBinder: Model must be a struct")
	}

	if !thiz.isPublicFieldAllowed(field, model) {
		return nil
	}

	rvField := rvModel.FieldByNameFunc(func(name string) bool {
		return thiz.matchFieldName(name, field)
	})

	// If field is not found or not settable, do nothing to avoid panic
	if !rvField.IsValid() || !rvField.CanSet() {
		return nil
	}

	if dbField, ok := value.(core.NullableValueGetter); ok {
		value = dbField.GetValue()
	}

	// Try NullableValueSetter on addressable value first
	if rvField.CanAddr() {
		if dbField, ok := rvField.Addr().Interface().(core.NullableValueSetter); ok {
			dbField.SetValue(value)
			return nil
		}
	}
	// Fallback to direct interface check
	if dbField, ok := rvField.Interface().(core.NullableValueSetter); ok {
		dbField.SetValue(value)
		return nil
	}

	if value != nil {
		v := reflect.ValueOf(value)
		// Only set if assignable to field type to avoid panic
		if v.IsValid() && v.Type().AssignableTo(rvField.Type()) {
			rvField.Set(v)
		}
	}

	return nil
}

func (thiz FieldBinder) BindProtectedField(model any, field string, value any) error {
	rvModel := reflect.ValueOf(model)
	if rvModel.Kind() != reflect.Ptr {
		return fmt.Errorf("LimitedFieldsBinder: Model must be a pointer")
	}
	rvModel = util.IndirectValue(rvModel)
	if rvModel.Kind() != reflect.Struct {
		return fmt.Errorf("LimitedFieldsBinder: Model must be a struct")
	}

	rvField := rvModel.FieldByNameFunc(func(name string) bool {
		return thiz.matchFieldName(name, field)
	})

	if protectedFieldsModel, ok := model.(core.LimitedFieldsMarshaller); ok {
		isProtectedAllowed := util.InArrayFunc(protectedFieldsModel.AllowedProtectedFields(), func(el string) bool {
			return strings.ToLower(field) == strings.ToLower(el)
		})

		if !isProtectedAllowed {
			if rvField.IsValid() && rvField.CanSet() {
				rvField.Set(reflect.Zero(rvField.Type()))
			}
			return nil
		}
	} else {
		return nil
	}

	// If field is not found or not settable, no-op
	if !rvField.IsValid() || !rvField.CanSet() {
		return nil
	}

	if dbField, ok := value.(core.NullableValueGetter); ok {
		value = dbField.GetValue()
	}

	// Prefer addressable receiver for NullableValueSetter
	if rvField.CanAddr() {
		if dbField, ok := rvField.Addr().Interface().(core.NullableValueSetter); ok {
			dbField.SetValue(value)
			return nil
		}
	}
	if dbField, ok := rvField.Interface().(core.NullableValueSetter); ok {
		dbField.SetValue(value)
		return nil
	}

	v := reflect.ValueOf(value)
	if v.IsValid() && v.Type().AssignableTo(rvField.Type()) {
		rvField.Set(v)
	}

	return nil
}

func (thiz FieldBinder) BindFieldClosure(model any, field string, closure any) error {
	rvModel := reflect.ValueOf(model)
	if rvModel.Kind() != reflect.Ptr {
		return fmt.Errorf("LimitedFieldsBinder: Model must be a pointer")
	}

	rvModel = util.IndirectValue(rvModel)
	if rvModel.Kind() != reflect.Struct {
		return fmt.Errorf("LimitedFieldsBinder: Model must be a struct")
	}

	rtClosure := reflect.ValueOf(closure)
	if rtClosure.Kind() != reflect.Func {
		return fmt.Errorf("LimitedFieldsBinder: Closure must be a function")
	}

	if !thiz.isPublicFieldAllowed(field, model) {
		return nil
	}

	returnedValues := rtClosure.Call(nil)
	if len(returnedValues) == 0 {
		return fmt.Errorf("LimitedFieldsBinder: Closure must return value")
	}

	rvField := rvModel.FieldByNameFunc(func(name string) bool {
		return thiz.matchFieldName(name, field)
	})

	// If field is not found or not settable, no-op
	if !rvField.IsValid() || !rvField.CanSet() {
		return nil
	}

	value := returnedValues[0]

	if dbField, ok := value.Interface().(core.NullableValueGetter); ok {
		value = reflect.ValueOf(dbField.GetValue())
	}

	// Prefer addressable receiver for NullableValueSetter
	if rvField.CanAddr() {
		if dbField, ok := rvField.Addr().Interface().(core.NullableValueSetter); ok {
			dbField.SetValue(value.Interface())
			return nil
		}
	}

	if dbField, ok := rvField.Interface().(core.NullableValueSetter); ok {
		dbField.SetValue(value.Interface())
	} else {
		// Only set if assignable to field type to avoid panic
		if value.IsValid() && value.Type().AssignableTo(rvField.Type()) {
			rvField.Set(value)
		}
	}

	return nil

}

func (thiz FieldBinder) BindFields(model any, donor any, fields []string) error {
	rvDonor := reflect.ValueOf(donor)

	if rvDonor.Kind() != reflect.Ptr {
		return fmt.Errorf("LimitedFieldsBinder: Model must be a pointer")
	}

	rvDonor = util.IndirectValue(rvDonor)
	if rvDonor.Kind() != reflect.Struct {
		return fmt.Errorf("LimitedFieldsBinder: Model must be a struct")
	}

	for _, field := range fields {
		donorField := rvDonor.FieldByNameFunc(func(name string) bool {
			return thiz.matchFieldName(name, field)
		})

		var donorVal any
		if donorField.IsValid() {
			donorVal = donorField.Interface()
		}
		err := thiz.BindField(model, field, donorVal)
		if err != nil {
			return err
		}
	}
	return nil
}

func (thiz FieldBinder) isPublicFieldAllowed(field string, model any) bool {
	allowedFields := make([]string, 0)
	if limitedFields, ok := model.(core.LimitedFieldsMarshaller); ok {
		allowedFields = limitedFields.AllowedFields()
	} else {
		allowedFields = thiz.Fields
	}

	if len(allowedFields) == 1 && allowedFields[0] == "*" {
		return true
	}
	return util.InArrayFunc(allowedFields, func(el string) bool {
		return thiz.matchFieldName(el, field)
	})
}

// matchFieldName provides robust case-insensitive field matching
func (thiz FieldBinder) matchFieldName(structFieldName, requestedField string) bool {
	// Try multiple matching strategies for better compatibility

	// 1. Direct case-insensitive match
	if strings.EqualFold(structFieldName, requestedField) {
		return true
	}

	// 2. Case-insensitive match with underscores removed
	normalizedStruct := strings.ToLower(strings.ReplaceAll(structFieldName, "_", ""))
	normalizedRequested := strings.ToLower(strings.ReplaceAll(requestedField, "_", ""))
	if normalizedStruct == normalizedRequested {
		return true
	}

	// 3. Original camelCase conversion logic (for backward compatibility)
	structCamel := strings.ToLower(strcase.ToLowerCamel(strings.ToLower(structFieldName)))
	requestedCamel := strings.ToLower(strcase.ToLowerCamel(requestedField))
	if structCamel == requestedCamel {
		return true
	}

	return false
}
