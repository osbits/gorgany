package model

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/iancoleman/strcase"
	"reflect"
	"strings"
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
		return strings.ToLower(strcase.ToLowerCamel(strings.ToLower(name))) == strings.ToLower(strcase.ToLowerCamel(field))
	})

	if dbField, ok := value.(core.NullableValueGetter); ok {
		value = dbField.GetValue()
	}

	if dbField, ok := rvField.Addr().Interface().(core.NullableValueSetter); ok {
		dbField.SetValue(value)
	} else {
		if value != nil {
			rvField.Set(reflect.ValueOf(value))
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
		return strings.ToLower(strcase.ToLowerCamel(strings.ToLower(name))) == strings.ToLower(strcase.ToLowerCamel(field))
	})

	if protectedFieldsModel, ok := model.(core.LimitedFieldsMarshaller); ok {
		isProtectedAllowed := util.InArrayFunc(protectedFieldsModel.AllowedProtectedFields(), func(el string) bool {
			return strings.ToLower(field) == strings.ToLower(el)
		})

		if !isProtectedAllowed {
			rvField.Set(reflect.Zero(rvField.Type()))
			return nil
		}
	} else {
		return nil
	}

	if dbField, ok := value.(core.NullableValueGetter); ok {
		value = dbField.GetValue()
	}

	if dbField, ok := rvField.Interface().(core.NullableValueSetter); ok {
		dbField.SetValue(value)
	} else {
		rvField.Set(reflect.ValueOf(value))
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
		return strings.ToLower(strcase.ToLowerCamel(strings.ToLower(name))) == strings.ToLower(strcase.ToLowerCamel(field))
	})

	value := returnedValues[0]

	if dbField, ok := value.Interface().(core.NullableValueGetter); ok {
		value = reflect.ValueOf(dbField.GetValue())
	}

	if dbField, ok := rvField.Interface().(core.NullableValueSetter); ok {
		dbField.SetValue(value.Interface())
	} else {
		rvField.Set(value)
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
			return strings.ToLower(strcase.ToLowerCamel(strings.ToLower(name))) == strings.ToLower(strcase.ToLowerCamel(field))
		})

		err := thiz.BindField(model, field, donorField.Interface())
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
		return strings.ToLower(strcase.ToLowerCamel(strings.ToLower(el))) == strings.ToLower(strcase.ToLowerCamel(field))
	})
}
