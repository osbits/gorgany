package http

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/gorilla/schema"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/db"
	error2 "github.com/osbits/gorgany/v2/err"
	"github.com/osbits/gorgany/v2/service/cache"
	"github.com/osbits/gorgany/v2/util"
)

type QueryParser struct {
	message core.HttpMessage
}

func (p *QueryParser) Parse(arg interface{}) error {
	queryParams := p.message.Request().Query()

	err := p.initStruct(arg, queryParams.AsMap())
	if err != nil {
		validationErrors := make(error2.ValidationErrors, 0)
		if errors.As(err, &schema.MultiError{}) {
			multiError := err.(schema.MultiError)
			for key, err := range multiError {
				validationErrors.AddValidationError(error2.ValidationError{
					Field: sanitizeFieldName(key),
					Err:   err.Error(),
				})
			}
		} else {
			checkAndAddIfValidationError(err, &validationErrors)
		}
		return &validationErrors
	}

	return nil
}

func (p *QueryParser) initStruct(dest interface{}, inputMap map[string]any) error {
	if dest == nil {
		return fmt.Errorf("destination cannot be nil")
	}

	rvDest := reflect.ValueOf(dest)
	if rvDest.Kind() != reflect.Ptr {
		return fmt.Errorf("destination must be a pointer")
	}

	for key, value := range inputMap {
		err := p.processValue(dest, value, key)
		if err != nil {
			return fieldParseError(key, err)
		}
	}
	return nil
}

func (p *QueryParser) processValue(dest any, value interface{}, key string) error {
	rvDest := reflect.ValueOf(dest)
	if rvDest.Kind() != reflect.Ptr || !rvDest.IsValid() {
		return fmt.Errorf("destination must be a valid pointer")
	}
	rvDest = rvDest.Elem()

	field := rvDest
	if key != "" {
		found, err := p.callBindMethodIfExists(dest, key, value)
		if err != nil {
			return err
		}
		if found {
			return nil
		}

		if util.IndirectValue(rvDest).Kind() == reflect.Struct {
			info := getTypeInfo(rvDest.Type(), "scheme")
			if structField, ok := info.fields[key]; ok {
				field = rvDest.FieldByName(structField.Name)
			} else {
				return nil
			}
		}
	}

	return p.setFieldValue(field, key, value)
}

func (p *QueryParser) setFieldValue(field reflect.Value, key string, value interface{}) error {
	if !field.IsValid() || !field.CanSet() {
		return fmt.Errorf("field %s is not settable", key)
	}

	switch field.Kind() {
	case reflect.Ptr:
		return p.setPointer(field, key, value)
	case reflect.Slice:
		return p.setSlice(field, key, value)
	case reflect.Struct:
		return p.setStruct(field, key, value)
	case reflect.Map:
		return p.setMap(field, key, value)
	case reflect.Interface:
		return p.setInterface(field, key, value)
	default:
		return p.setPrimitive(field, key, value)
	}
}

func (p *QueryParser) setInterface(field reflect.Value, key string, value interface{}) error {
	if value == nil {
		field.Set(reflect.Zero(field.Type()))
		return nil
	}

	reflectedValue := reflect.ValueOf(value)
	if !reflectedValue.Type().AssignableTo(field.Type()) {
		return newValidationError(key, "Cannot assign value of type %s to type %s", reflectedValue.Type(), field.Type())
	}

	field.Set(reflectedValue)
	return nil
}

func (p *QueryParser) setPointer(field reflect.Value, key string, value interface{}) error {
	if value == nil && field.Kind() == reflect.Ptr {
		return nil
	}
	indirectType := field.Type().Elem()
	field.Set(reflect.New(indirectType).Elem().Addr().Convert(reflect.PointerTo(indirectType)))
	return p.setFieldValue(field.Elem(), key, value)
}

func (p *QueryParser) setSlice(field reflect.Value, key string, value interface{}) error {
	elementType := field.Type().Elem()

	reflectedValue := reflect.ValueOf(value)
	if reflectedValue.Kind() != reflect.Slice {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: key, Err: "Value must be a slice"},
		}
	}

	sliceLen := reflectedValue.Len()
	newSlice := reflect.MakeSlice(field.Type(), 0, sliceLen)

	for i := 0; i < sliceLen; i++ {
		rv := reflect.New(elementType).Elem()
		if err := p.processValue(rv.Addr().Interface(), reflectedValue.Index(i).Interface(), ""); err != nil {
			return fmt.Errorf("failed to process slice element: %w", err)
		}
		newSlice = reflect.Append(newSlice, rv)
	}

	field.Set(newSlice)
	return nil
}

func (p *QueryParser) setMap(field reflect.Value, key string, value interface{}) error {
	m, ok := value.(map[string]any)
	if !ok {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: key, Err: "Value must be map"},
		}
	}

	newMap := reflect.MakeMap(field.Type())
	mapValueType := field.Type().Elem()

	for k, v := range m {
		newValue := reflect.New(mapValueType).Elem()
		err := p.processValue(newValue.Addr().Interface(), v, "")
		if err != nil {
			return err
		}
		newMap.SetMapIndex(reflect.ValueOf(k), newValue)
	}

	field.Set(newMap)
	return nil
}

func (p *QueryParser) setStruct(field reflect.Value, key string, value interface{}) error {
	// Handle time-like structs from string query params
	if ok, err := setTimeLikeFromValue(field, value); ok {
		if err != nil {
			return newValidationError(key, "Cannot parse time value: %v", err)
		}
		return nil
	}

	found, err := p.initDomain(field, key, value)
	if err != nil {
		return err
	}
	if found {
		return nil
	}

	if nestedValue, ok := value.(map[string]any); ok {
		return p.initStruct(field.Addr().Interface(), nestedValue)
	}

	if value != nil {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: key, Err: "Cannot set non-object value to struct field"},
		}
	}

	return nil
}

func (p *QueryParser) setPrimitive(field reflect.Value, key string, value interface{}) error {
	if value == nil {
		field.Set(reflect.Zero(field.Type()))
		return nil
	}

	primitive, err := util.ResolvePrimitive(field.Kind(), fmt.Sprintf("%v", value))
	if err != nil {
		return newValidationError(key, "Cannot convert '%v' to type %s: %v", value, field.Type().String(), err)
	}

	field.Set(reflect.ValueOf(primitive).Convert(field.Type()))
	return nil
}

func (p *QueryParser) callBindMethodIfExists(command any, fieldName string, value any) (bool, error) {
	rvArg := reflect.ValueOf(command)
	t := rvArg.Type()

	if util.IndirectValue(rvArg).Kind() != reflect.Struct {
		return false, nil
	}

	info := getTypeInfo(t, "scheme")

	if _, ok := info.fields[fieldName]; !ok {
		return false, nil
	}

	method, ok := info.bindMethods[util.StudlyCase(fieldName)]
	if !ok {
		return false, nil
	}

	methodValue := rvArg.MethodByName(method.Name)
	if !methodValue.IsValid() {
		return false, nil
	}

	var processedValue any
	if complexValue, ok := value.(map[string]any); ok {
		processedValue = complexValue
	} else if value != nil {
		var err error
		if methodValue.Type().NumIn() > 0 {
			// Check if method expects a parameter
			if methodValue.Type().NumIn() > 0 {
				processedValue, err = util.ResolvePrimitive(methodValue.Type().In(0).Kind(), fmt.Sprintf("%v", value))
				if err != nil {
					return false, &error2.ValidationErrors{
						error2.ValidationError{Field: fieldName, Err: err.Error()},
					}
				}
			}
		}
	}

	var outputs []reflect.Value
	if processedValue != nil {
		outputs = methodValue.Call([]reflect.Value{reflect.ValueOf(processedValue)})
	} else {
		outputs = methodValue.Call(nil)
	}

	if len(outputs) > 0 {
		if outputs[0].IsZero() {
			return true, nil
		}

		output, ok := outputs[0].Interface().(error)
		if !ok {
			return false, fmt.Errorf("Bind method %s must return error or nothing", method.Name)
		}

		var validationErrors *error2.ValidationErrors
		if errors.As(output, &validationErrors) {
			return false, output
		}

		return false, &error2.ValidationErrors{error2.ValidationError{
			Field: fieldName,
			Err:   output.Error(),
		}}
	}

	return true, nil
}

func (p *QueryParser) initDomain(field reflect.Value, fieldName string, value any) (bool, error) {
	if field.Kind() != reflect.Struct {
		return false, nil
	}

	newInstance := reflect.New(field.Type()).Interface()
	if !util.IsGenericImplemented(newInstance, (*core.IDomain[any])(nil)) {
		return false, nil
	}

	scheme := cache.GetDomainSchemeCache().ParseDomain(newInstance)
	primaryField := scheme.PrioritizedPrimaryField
	if primaryField == nil {
		return false, fmt.Errorf("domain type %s has no primary field defined", field.Type().Name())
	}

	dest := reflect.New(field.Type()).Interface()

	neededGeneralType := core.GeneralDataTypeOf(util.IndirectType(primaryField.FieldType).Kind())
	actualType := core.GeneralDataTypeOf(reflect.TypeOf(value).Kind())
	if neededGeneralType != actualType {
		return false, &error2.ValidationErrors{
			error2.ValidationError{
				Field: fieldName,
				Err:   fmt.Sprintf("Input value must be %s, got %s", neededGeneralType, actualType),
			},
		}
	}

	err := db.Builder().WhereEqual(primaryField.DBName, value).Get(dest)
	if err != nil {
		return false, fmt.Errorf("database error when loading domain object: %w", err)
	}

	domainMeta, ok := dest.(core.IDomainMeta)
	if !ok || !domainMeta.GetLoaded() {
		return false, &error2.ValidationErrors{
			error2.ValidationError{
				Field: fieldName,
				Err:   fmt.Sprintf("Record with specified primary key (%v) was not found", value),
			},
		}
	}

	if !field.IsValid() || !field.CanSet() {
		return false, newValidationError(fieldName, "Domain field is not settable")
	}
	field.Set(reflect.ValueOf(dest).Elem())

	return true, nil
}
