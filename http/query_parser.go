package http

import (
	"errors"
	"fmt"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/service/cache"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/gorilla/schema"
	"reflect"
)

type queryParser struct {
	message *Message
}

func (thiz queryParser) parse(arg interface{}) error {
	queryParams := thiz.message.GetQuery()

	err := thiz.initStruct(arg, queryParams)
	if err != nil {
		validationErrors := make(error2.ValidationErrors, 0)
		if errors.As(err, &schema.MultiError{}) {
			multiError := err.(schema.MultiError)
			for key, err := range multiError {
				validationErrors.AddValidationError(error2.ValidationError{Field: key, Err: err.Error()})
			}
		} else {
			checkAndAddIfValidationError(err, &validationErrors)
		}
		return &validationErrors
	}

	return nil
}

func (thiz queryParser) initStruct(dest interface{}, inputMap map[string]any) error { // todo: LocolizedString from map does not work, need to fix it on the front side
	for key, value := range inputMap {
		err := thiz.processValue(dest, value, key)
		if err != nil {
			return err
		}
	}
	return nil
}

func (thiz queryParser) processValue(dest any, value interface{}, key string) error {
	rvDest := reflect.ValueOf(dest)
	if rvDest.Kind() != reflect.Ptr || !rvDest.IsValid() {
		return fmt.Errorf("gorgany.http.jsonParser: Destination type must be a pointer")
	}
	rvDest = rvDest.Elem()

	field := rvDest
	if key != "" {
		found, err := thiz.callBindMethodIfExists(dest, key, value)
		if err != nil {
			return err
		}
		if found {
			return nil
		}

		if util.IndirectValue(rvDest).Kind() == reflect.Struct {
			found, field, _ = util.FindFieldByTag(rvDest, "scheme", key, true)
			if !found {
				return nil
			}
		}
	}

	return thiz.setFieldValue(field, key, value)
}

func (thiz queryParser) setFieldValue(field reflect.Value, key string, value interface{}) error {
	switch field.Kind() {
	case reflect.Ptr:
		return thiz.setPointer(field, key, value)
	case reflect.Slice:
		return thiz.setSlice(field, key, value)
	case reflect.Struct:
		return thiz.setStruct(field, key, value)
	case reflect.Map:
		return thiz.setMap(field, key, value)
	default:
		return thiz.setPrimitive(field, value)
	}
}

func (thiz queryParser) setPointer(field reflect.Value, key string, value interface{}) error {
	if value == nil && field.Kind() == reflect.Ptr {
		return nil
	}
	indirectType := field.Type().Elem()
	field.Set(reflect.New(indirectType).Elem().Addr().Convert(reflect.PointerTo(indirectType)))
	return thiz.setFieldValue(field.Elem(), key, value)
}

func (thiz queryParser) setSlice(field reflect.Value, key string, value interface{}) error {
	reflectedElement := util.GetReflectedElementOfSlice(field.Interface()) // fix the GetReflectElementOfSlice method, becouse it always returns a pointer, but we need a raw value
	s, ok := value.([]any)
	if !ok {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: key, Err: "Value must be slice"},
		}
	}

	for _, v := range s {
		rv := reflect.New(reflectedElement.Type()).Elem().Addr().Convert(reflect.PointerTo(reflectedElement.Type())).Elem()
		err := thiz.processValue(rv.Addr().Interface(), v, "")
		if err != nil {
			return err
		}

		field.Set(reflect.Append(field, rv)) // todo fix loading of domains
	}

	return nil
}

func (thiz queryParser) setMap(field reflect.Value, key string, value interface{}) error {
	m, ok := value.(map[string]any)
	if !ok {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: key, Err: "Value must be map"},
		}
	}

	field.Set(reflect.MakeMap(field.Type()))
	for k, v := range m {
		mapValueRType := field.Type().Elem()
		rv := reflect.New(mapValueRType).Elem().Addr().Convert(reflect.PointerTo(mapValueRType)).Elem()
		err := thiz.processValue(rv.Addr().Interface(), v, "")
		if err != nil {
			return err
		}
		field.SetMapIndex(reflect.ValueOf(k), rv)
	}
	return nil
}

func (thiz queryParser) setStruct(field reflect.Value, key string, value interface{}) error {
	found, err := thiz.initDomain(field, key, value)
	if err != nil {
		return err
	}
	if found {
		return nil
	}

	if nestedValue, ok := value.(map[string]any); ok {
		err = thiz.initStruct(field.Addr().Interface(), nestedValue)
		if err != nil {
			return err
		}
		return nil
	}
	return nil
}

func (thiz queryParser) setPrimitive(field reflect.Value, value interface{}) error {
	if value == nil {
		return nil
	}

	primitive, err := util.ResolvePrimitive(field.Kind(), fmt.Sprintf("%v", value))
	if err != nil {
		return err
	}
	field.Set(reflect.ValueOf(primitive).Convert(field.Type()))
	return nil
}

func (thiz queryParser) callBindMethodIfExists(command any, fieldName string, value any) (bool, error) {
	rvArg := reflect.ValueOf(command)
	if util.IndirectValue(rvArg).Kind() != reflect.Struct {
		return false, nil
	}

	found, _, structField := util.FindFieldByTag(rvArg, "scheme", fieldName, true)
	if !found {
		return false, nil
	}

	method := rvArg.MethodByName(fmt.Sprintf("Bind%s", structField.Name))
	if !method.IsValid() {
		return false, nil
	}

	if _, ok := value.(map[string]any); ok {
		method.Call([]reflect.Value{reflect.ValueOf(value)})
	} else {
		castedValue, err := util.ResolvePrimitive(method.Type().In(0).Kind(), fmt.Sprintf("%v", value))
		if err != nil {
			return false, &error2.ValidationErrors{
				error2.ValidationError{Field: structField.Name, Err: err.Error()},
			}
		}
		method.Call([]reflect.Value{reflect.ValueOf(castedValue)})
	}
	return true, nil
}

func (thiz queryParser) initDomain(field reflect.Value, fieldName string, value any) (bool, error) {
	if field.Kind() != reflect.Struct {
		return false, nil
	}

	s := reflect.New(field.Type()).Interface()
	if !util.IsGenericImplemented(s, (*core.IDomain[any])(nil)) {
		return false, nil
	}

	scheme := cache.GetDomainSchemeCache().ParseDomain(s)
	primaryField := scheme.PrioritizedPrimaryField

	dest := reflect.New(field.Type()).Interface()

	id, err := util.ResolvePrimitive(primaryField.FieldType.Kind(), fmt.Sprintf("%v", value))
	if err != nil {
		return false, &error2.ValidationErrors{
			error2.ValidationError{Field: fieldName, Err: err.Error()},
		}
	}

	neededGeneralType := core.GeneralDataTypeOf(util.IndirectType(primaryField.FieldType).Kind())
	if neededGeneralType != core.GeneralDataTypeOf(reflect.TypeOf(id).Kind()) {
		return false, &error2.ValidationErrors{
			error2.ValidationError{Field: fieldName, Err: fmt.Sprintf("Input value must be %s", neededGeneralType)},
		}
	}

	err = db.Builder().WhereEqual(primaryField.DBName, value).Get(dest)
	if err != nil {
		return false, err
	}

	if !dest.(core.IDomainMeta).GetLoaded() {
		return false, &error2.ValidationErrors{
			error2.ValidationError{Field: fieldName, Err: fmt.Sprintf("Record with specified primary key (%v) was not found", value)},
		}
	}
	field.Set(reflect.ValueOf(dest).Elem())

	return true, nil
}
