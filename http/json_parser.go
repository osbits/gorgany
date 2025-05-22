package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/service/cache"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/spf13/viper"
)

const (
	// Maximum allowed size of JSON input in bytes
	maxJSONSize = 10 * 1024 * 1024 // 10MB
	// Maximum allowed nesting depth of JSON structures
	maxJSONDepth = 32
)

// typeCache stores reflection information for types to avoid repeated lookups
var typeCache = struct {
	sync.RWMutex
	m map[reflect.Type]*typeInfo
}{
	m: make(map[reflect.Type]*typeInfo),
}

// typeInfo stores cached reflection information for a type
type typeInfo struct {
	fields      map[string]reflect.StructField
	bindMethods map[string]reflect.Method
}

// getTypeInfo returns cached reflection information for a type
func getTypeInfo(t reflect.Type) *typeInfo {
	indirectT := util.IndirectType(t)

	typeCache.RLock()
	if info, ok := typeCache.m[t]; ok {
		typeCache.RUnlock()
		return info
	}
	typeCache.RUnlock()

	typeCache.Lock()
	defer typeCache.Unlock()

	// Double-check after acquiring write lock
	if info, ok := typeCache.m[t]; ok {
		return info
	}

	info := &typeInfo{
		fields:      make(map[string]reflect.StructField),
		bindMethods: make(map[string]reflect.Method),
	}

	if indirectT.Kind() == reflect.Struct {
		for i := 0; i < indirectT.NumField(); i++ {
			field := indirectT.Field(i)
			if tag := field.Tag.Get("json"); tag != "" {
				info.fields[tag] = field
			} else {
				info.fields[util.CamelCase(field.Name)] = field
			}
		}

		for i := 0; i < t.NumMethod(); i++ {
			method := t.Method(i)
			if len(method.Name) > 4 && method.Name[:4] == "Bind" {
				fieldName := method.Name[4:]
				info.bindMethods[fieldName] = method
			}
		}
	}

	typeCache.m[t] = info
	return info
}

// newValidationError creates a ValidationErrors with a single error entry
func newValidationError(field string, format string, args ...interface{}) *error2.ValidationErrors {
	return &error2.ValidationErrors{
		error2.ValidationError{
			Field: field,
			Err:   fmt.Sprintf(format, args...),
		},
	}
}

// JsonParser handles custom JSON unmarshaling with support for special types and behaviors
// such as custom field binding methods and domain object loading.
type JsonParser struct {
	message core.HttpMessage
}

func (p *JsonParser) Parse(dest interface{}) error {
	body, err := p.message.Request().Body()
	if err != nil {
		return fmt.Errorf("failed to read request body: %v", err)
	}

	if len(body) == 0 {
		return nil
	}

	// Check input size
	if len(body) > maxJSONSize {
		return &error2.ValidationErrors{
			error2.ValidationError{
				Field: "body",
				Err:   fmt.Sprintf("Input size exceeds maximum allowed size of %d bytes", maxJSONSize),
			},
		}
	}

	// Check JSON depth
	if err := p.checkJSONDepth(body); err != nil {
		return err
	}

	inputMap := make(map[string]interface{})
	err = json.Unmarshal(body, &inputMap)
	if err != nil {
		validationErrors := make(error2.ValidationErrors, 0)
		switch {
		case errors.Is(err, &json.UnmarshalTypeError{}):
			typeError := err.(*json.UnmarshalTypeError)
			validationErrors.AddValidationError(error2.ValidationError{
				Field: typeError.Field,
				Err:   fmt.Sprintf("Invalid type for field: expected %s, got %s", typeError.Type, typeError.Value),
			})
		case errors.Is(err, &json.SyntaxError{}):
			syntaxError := err.(*json.SyntaxError)
			validationErrors.AddValidationError(error2.ValidationError{
				Field: "body",
				Err:   fmt.Sprintf("Invalid JSON syntax at position %d", syntaxError.Offset),
			})
		default:
			checkAndAddIfValidationError(err, &validationErrors)
		}
		return &validationErrors
	}

	return p.initStruct(dest, inputMap)
}

func (p *JsonParser) initStruct(dest any, inputMap map[string]any) error {
	for key, value := range inputMap {
		err := p.processValue(dest, value, key)
		if err != nil {
			return err
		}
	}
	return nil
}

// processValue handles mapping of JSON values to Go struct fields with support for
// custom binding methods and validation
func (p *JsonParser) processValue(dest any, value interface{}, key string) error {
	rvDest := reflect.ValueOf(dest)
	if rvDest.Kind() != reflect.Ptr || !rvDest.IsValid() {
		return fmt.Errorf("gorgany.http.JsonParser: Destination type must be a pointer")
	}
	rvDest = rvDest.Elem()

	field := rvDest
	if key != "" {
		// Try to find and call a custom binding method first
		found, err := p.callBindMethodIfExists(dest, key, value)
		if err != nil {
			return err
		}
		if found {
			return nil
		}

		// Otherwise try to find a field with a matching JSON tag
		if util.IndirectValue(rvDest).Kind() == reflect.Struct {
			info := getTypeInfo(rvDest.Type())
			if structField, ok := info.fields[key]; ok {
				field = rvDest.FieldByName(structField.Name)
			} else {
				return nil
			}
		}
	}

	return p.setFieldValue(field, key, value)
}

// setFieldValue dispatches to the appropriate handler based on the field's kind
func (p *JsonParser) setFieldValue(field reflect.Value, key string, value interface{}) error {
	switch field.Kind() {
	case reflect.Ptr:
		return p.setPointer(field, key, value)
	case reflect.Slice:
		return p.setSlice(field, key, value)
	case reflect.Struct:
		return p.setStruct(field, key, value)
	case reflect.Map:
		return p.setMap(field, key, value)
	default:
		return p.setPrimitive(field, key, value)
	}
}

// setPointer handles pointer fields by either setting them to nil for null values
// or initializing and populating the pointed-to value
func (p *JsonParser) setPointer(field reflect.Value, key string, value interface{}) error {
	if !field.IsValid() || !field.CanSet() {
		if viper.GetBool("http.input.parser.debug") {
			log.Log().Warnf("DEBUG: JsonParser: Field '%s' (type: %s, kind: Ptr) is not settable. Skipping.\n",
				key, field.Type().String())
		}
		return nil // Skip setting this field
	}

	// If the value is nil (JSON null), leave the pointer as nil
	if value == nil {
		field.Set(reflect.Zero(field.Type())) // Explicitly set to nil pointer
		return nil
	}

	// Create a new instance of the type that this pointer points to
	indirectType := field.Type().Elem()
	newValue := reflect.New(indirectType)

	// Set the pointer field to point to this new instance
	field.Set(newValue)

	// Recursively set the value of what the pointer points to
	return p.setFieldValue(field.Elem(), key, value)
}

// setSlice handles slice fields by creating and populating elements
func (p *JsonParser) setSlice(field reflect.Value, key string, value interface{}) error {
	if !field.IsValid() || !field.CanSet() {
		if viper.GetBool("http.input.parser.debug") {
			log.Log().Warnf("DEBUG: JsonParser: Field '%s' (type: %s, kind: Slice) is not settable. Skipping.\n",
				key, field.Type().String())
		}
		return nil
	}

	reflectedElement := util.GetReflectedElementOfSlice(field.Interface())

	reflectedValue := reflect.ValueOf(value)
	if reflectedValue.Kind() != reflect.Slice {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: key, Err: "Value must be a slice"},
		}
	}

	sliceLen := reflectedValue.Len()
	// Create a new slice with the correct capacity
	newSlice := reflect.MakeSlice(field.Type(), 0, sliceLen)

	for i := 0; i < sliceLen; i++ {
		// Create a new element of the correct type
		rv := reflect.New(reflectedElement.Type()).Elem()
		if err := p.processValue(rv.Addr().Interface(), reflectedValue.Index(i).Interface(), ""); err != nil {
			return fmt.Errorf("failed to process slice element: %w", err)
		}
		newSlice = reflect.Append(newSlice, rv)
	}

	// Set the complete slice at once
	field.Set(newSlice)
	return nil
}

// setMap handles map fields by creating a new map and populating its entries
func (p *JsonParser) setMap(field reflect.Value, key string, value interface{}) error {
	if !field.IsValid() || !field.CanSet() {
		if viper.GetBool("http.input.parser.debug") {
			log.Log().Warnf("DEBUG: JsonParser: Field '%s' (type: %s, kind: Map) is not settable. Skipping.\n",
				key, field.Type().String())
		}
		return nil // Skip setting this field
	}

	// Check that the input value is actually a map
	m, ok := value.(map[string]any)
	if !ok {
		return newValidationError(key, "Value must be map")
	}

	// Create a new map of the appropriate type
	newMap := reflect.MakeMap(field.Type())

	// Get the value type for map entries
	mapValueType := field.Type().Elem()

	// For each key-value pair in the input map
	for k, v := range m {
		// Create a new value of the appropriate type
		newValue := reflect.New(mapValueType).Elem()

		// Process the value using our custom logic
		err := p.processValue(newValue.Addr().Interface(), v, "")
		if err != nil {
			return err
		}

		// Set the key-value pair in the map
		newMap.SetMapIndex(reflect.ValueOf(k), newValue)
	}

	// Set the complete map at once
	field.Set(newMap)
	return nil
}

// setStruct handles struct fields, with special handling for domain objects
func (p *JsonParser) setStruct(field reflect.Value, key string, value interface{}) error {
	// First try to initialize as a domain object if applicable
	found, err := p.initDomain(field, key, value)
	if err != nil {
		return err
	}
	if found {
		// If it's a domain and was loaded successfully, we're done
		return nil
	}

	// For regular structs or if domain loading failed, process it as a nested structure
	if nestedValue, ok := value.(map[string]any); ok {
		return p.initStruct(field.Addr().Interface(), nestedValue)
	}

	// If value is not a map (which would be invalid for a struct), return a validation error
	if value != nil {
		return &error2.ValidationErrors{
			error2.ValidationError{Field: key, Err: "Cannot set non-object value to struct field"},
		}
	}

	return nil
}

// setPrimitive handles primitive values like strings, numbers, and booleans
func (p *JsonParser) setPrimitive(field reflect.Value, key string, value interface{}) error {
	if !field.IsValid() || !field.CanSet() {
		if viper.GetBool("http.input.parser.debug") {
			log.Log().Warnf("DEBUG: JsonParser: Field '%s' (type: %s, kind: %s) is not settable. Skipping.\n",
				key, field.Type().String(), field.Kind().String())
		}
		return nil // Skip setting this field
	}

	// If the value is nil (JSON null), leave the field with its zero value
	if value == nil {
		field.Set(reflect.Zero(field.Type()))
		return nil
	}

	// Try to convert the input value to the appropriate primitive type
	primitive, err := util.ResolvePrimitive(field.Kind(), fmt.Sprintf("%v", value))
	if err != nil {
		return newValidationError(key, "Cannot convert '%v' to type %s: %v", value, field.Type().String(), err)
	}

	// Set the field to the converted value
	field.Set(reflect.ValueOf(primitive).Convert(field.Type()))
	return nil
}

// callBindMethodIfExists checks if a struct has a special bind method for a field
// and calls it if available. The bind method must be named Bind<FieldName>.
// Returns true if a method was found and called successfully.
func (p *JsonParser) callBindMethodIfExists(command any, fieldName string, value any) (bool, error) {
	rvArg := reflect.ValueOf(command)
	t := rvArg.Type()

	// Only struct types can have bind methods
	if util.IndirectValue(rvArg).Kind() != reflect.Struct {
		return false, nil
	}

	info := getTypeInfo(t)

	// Find the field with the matching JSON tag
	if _, ok := info.fields[fieldName]; !ok {
		return false, nil
	}

	// Look for the Transient<FieldName> method
	method, ok := info.bindMethods[util.StudlyCase(fieldName)]
	if !ok {
		return false, nil
	}

	// Get the method value from the receiver
	methodValue := rvArg.MethodByName(method.Name)
	if !methodValue.IsValid() {
		return false, nil
	}

	// Prepare the argument for the method call
	var processedValue any
	if complexValue, ok := value.(map[string]any); ok {
		// For complex values, pass the whole map to the method
		processedValue = complexValue
	} else if value != nil {
		// For primitive values, convert to the expected type
		var err error
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

	// Call the method
	var outputs []reflect.Value
	if processedValue != nil {
		outputs = methodValue.Call([]reflect.Value{reflect.ValueOf(processedValue)})
	} else {
		outputs = methodValue.Call(nil)
	}

	// Handle the return value(s)
	if len(outputs) > 0 {
		// If the method returned nil/zero, it's successful
		if outputs[0].IsZero() {
			return true, nil
		}

		// The method must return an error or nothing
		output, ok := outputs[0].Interface().(error)
		if !ok {
			return false, fmt.Errorf("Transient method %s must return error or nothing", method.Name)
		}

		// Check if it's already a ValidationErrors type
		var validationErrors *error2.ValidationErrors
		if errors.As(output, &validationErrors) {
			return false, output
		}

		// Wrap other errors in ValidationErrors
		return false, &error2.ValidationErrors{error2.ValidationError{
			Field: fieldName,
			Err:   output.Error(),
		}}
	}

	return true, nil
}

// initDomain attempts to load a domain object from the database using the provided value as a primary key.
// Returns true if the field is a domain object and was successfully loaded.
func (p *JsonParser) initDomain(field reflect.Value, fieldName string, value any) (bool, error) {
	// Only struct types can be domain objects
	if field.Kind() != reflect.Struct {
		return false, nil
	}

	// Create a new instance of the struct type to check if it implements IDomain
	newInstance := reflect.New(field.Type()).Interface()
	if !util.IsGenericImplemented(newInstance, (*core.IDomain[any])(nil)) {
		// Not a domain object, normal struct processing will handle it
		return false, nil
	}

	// Get information about the domain's database schema
	scheme := cache.GetDomainSchemeCache().ParseDomain(newInstance)
	primaryField := scheme.PrioritizedPrimaryField
	if primaryField == nil {
		return false, fmt.Errorf("domain type %s has no primary field defined", field.Type().Name())
	}

	// Create a destination object to receive the loaded domain
	dest := reflect.New(field.Type()).Interface()

	// Check that the value provided matches the type needed for the primary key
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

	// Attempt to load the domain object from the database
	err := db.Builder().WhereEqual(primaryField.DBName, value).Get(dest)
	if err != nil {
		return false, fmt.Errorf("database error when loading domain object: %w", err)
	}

	// Check if a record was actually found
	domainMeta, ok := dest.(core.IDomainMeta)
	if !ok || !domainMeta.GetLoaded() {
		return false, &error2.ValidationErrors{
			error2.ValidationError{
				Field: fieldName,
				Err:   fmt.Sprintf("Record with specified primary key (%v) was not found", value),
			},
		}
	}

	// Set the field to the loaded domain object
	if !field.IsValid() || !field.CanSet() {
		if viper.GetBool("http.input.parser.debug") {
			log.Log().Warnf("DEBUG: JsonParser: Field '%s' (type: %s, kind: Struct, intended for Domain) is not settable. Skipping.\n",
				fieldName, field.Type().String())
		}
		return false, newValidationError(fieldName, "Domain field is not settable")
	}
	field.Set(reflect.ValueOf(dest).Elem())

	return true, nil
}

// checkJSONDepth verifies that the JSON input doesn't exceed the maximum allowed nesting depth
func (p *JsonParser) checkJSONDepth(body []byte) error {
	var depth int
	var inString bool
	var escape bool

	for i := 0; i < len(body); i++ {
		c := body[i]
		if escape {
			escape = false
			continue
		}

		if c == '\\' {
			escape = true
			continue
		}

		if c == '"' && !escape {
			inString = !inString
			continue
		}

		if !inString {
			switch c {
			case '{', '[':
				depth++
				if depth > maxJSONDepth {
					return &error2.ValidationErrors{
						error2.ValidationError{
							Field: "body",
							Err:   fmt.Sprintf("JSON structure exceeds maximum allowed depth of %d", maxJSONDepth),
						},
					}
				}
			case '}', ']':
				depth--
			}
		}
	}

	return nil
}
