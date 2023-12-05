package model

import (
	"fmt"
	"gorgany/app/core"
	"gorgany/util"
	"reflect"
)

type FieldBinder struct {
	Fields                 []string
	AllowedProtectedFields []string
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

	if !thiz.isFieldAllowed(field, model) {
		return nil
	}

	rvField := rvModel.FieldByName(field)
	rvField.Set(reflect.ValueOf(value))

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

	if !thiz.isFieldAllowed(field, model) {
		return nil
	}

	returnedValues := rtClosure.Call(nil)
	if len(returnedValues) == 0 {
		return fmt.Errorf("LimitedFieldsBinder: Closure must return value")
	}

	rvField := rvModel.FieldByName(field)
	rvField.Set(returnedValues[0])

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
		donorField := rvDonor.FieldByName(field)
		err := thiz.BindField(model, field, donorField.Interface())
		if err != nil {
			return err
		}
	}
	return nil
}

func (thiz FieldBinder) isFieldAllowed(field string, model any) bool {
	if protectedFieldsModel, ok := model.(core.ProtectedFields); ok {
		isProtected := util.InArray(field, protectedFieldsModel.GetProtectedFields())
		if isProtected {
			isProtectedAllowed := util.InArray(field, thiz.AllowedProtectedFields)
			if !isProtectedAllowed {
				return false
			}
		}
	}

	if len(thiz.Fields) == 1 && thiz.Fields[0] == "*" {
		return true
	}
	return util.InArray(field, thiz.Fields)
}
