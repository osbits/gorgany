package http

import (
	"errors"
	"fmt"
	"mime/multipart"
	"reflect"
	"sync"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/db"
	"git.qix.sx/gorgany/gorgany.git/decoder"
	mpart "git.qix.sx/gorgany/gorgany.git/decoder/multipart"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/service/cache"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/gorilla/schema"
)

const (
	// Maximum allowed size for multipart form data in bytes
	maxMultipartSize = 32 * 1024 * 1024 // 32MB
	// Maximum number of files allowed in a single request
	maxFiles = 100
	// Maximum size of a single file in bytes
	maxFileSize = 10 * 1024 * 1024 // 10MB
)

// multipartTypeCache stores reflection information for types to avoid repeated lookups
var multipartTypeCache = struct {
	sync.RWMutex
	m map[reflect.Type]*typeInfo
}{
	m: make(map[reflect.Type]*typeInfo),
}

// MultipartParser handles parsing of multipart form data into Go structures
type MultipartParser struct {
	message core.HttpMessage
}

// Parse parses multipart form data into the provided structure
// It handles both form values and file uploads
func (p *MultipartParser) Parse(arg interface{}) error {
	multipartForm := p.message.Request().GetMultipartFormValues()

	// Validate total form size
	if err := p.validateFormSize(multipartForm); err != nil {
		return err
	}

	// Validate number of files
	if len(multipartForm.File) > maxFiles {
		return &error2.ValidationErrors{
			error2.ValidationError{
				Field: "files",
				Err:   fmt.Sprintf("Too many files. Maximum allowed is %d", maxFiles),
			},
		}
	}

	// Validate individual file sizes
	if err := p.validateFileSizes(multipartForm.File); err != nil {
		return err
	}

	queryParams, err := decoder.ParseUrlValues(multipartForm.Value)
	if err != nil {
		return fmt.Errorf("failed to parse form values: %w", err)
	}

	err = p.initStruct(arg, queryParams)
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

	openedFiles, err := mpart.DecodeFiles(multipartForm.File, arg)
	defer func() {
		if len(openedFiles) > 0 {
			// todo: need to fix the closing (removing) of temp files
			//for _, file := range openedFiles {
			//	file.Close()
			//}
		}
	}()

	if err != nil {
		validationErrors := make(error2.ValidationErrors, 0)
		for key := range multipartForm.File {
			validationErrors.AddValidationError(error2.ValidationError{
				Field: sanitizeFieldName(key),
				Err:   "Incorrect files",
			})
		}
		return &validationErrors
	}

	return nil
}

// validateFormSize checks if the total form size is within limits
func (p *MultipartParser) validateFormSize(form *multipart.Form) error {
	totalSize := 0
	for _, files := range form.File {
		for _, file := range files {
			totalSize += int(file.Size)
		}
	}

	if totalSize > maxMultipartSize {
		return &error2.ValidationErrors{
			error2.ValidationError{
				Field: "form",
				Err:   fmt.Sprintf("Total form size exceeds maximum allowed size of %d bytes", maxMultipartSize),
			},
		}
	}
	return nil
}

// validateFileSizes checks if individual file sizes are within limits
func (p *MultipartParser) validateFileSizes(files map[string][]*multipart.FileHeader) error {
	for fieldName, fileList := range files {
		for _, file := range fileList {
			if file.Size > maxFileSize {
				return &error2.ValidationErrors{
					error2.ValidationError{
						Field: sanitizeFieldName(fieldName),
						Err:   fmt.Sprintf("File size exceeds maximum allowed size of %d bytes", maxFileSize),
					},
				}
			}
		}
	}
	return nil
}

func (p *MultipartParser) initStruct(dest interface{}, inputMap map[string]any) error {
	if dest == nil {
		return fmt.Errorf("destination cannot be nil")
	}

	rvDest := reflect.ValueOf(dest)
	if rvDest.Kind() != reflect.Ptr {
		return fmt.Errorf("destination must be a pointer")
	}

	for key, value := range inputMap {
		if err := p.processValue(dest, value, key); err != nil {
			return fmt.Errorf("failed to process field %s: %w", key, err)
		}
	}
	return nil
}

func (p *MultipartParser) processValue(dest any, value interface{}, key string) error {
	if dest == nil {
		return fmt.Errorf("destination cannot be nil")
	}

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

func (p *MultipartParser) setFieldValue(field reflect.Value, key string, value interface{}) error {
	if !field.IsValid() {
		return fmt.Errorf("invalid field")
	}

	if !field.CanSet() {
		return fmt.Errorf("field %s cannot be set", key)
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
	default:
		return p.setPrimitive(field, value)
	}
}

func (p *MultipartParser) setPointer(field reflect.Value, key string, value interface{}) error {
	if value == nil && field.Kind() == reflect.Ptr {
		return nil
	}
	indirectType := field.Type().Elem()
	field.Set(reflect.New(indirectType).Elem().Addr().Convert(reflect.PointerTo(indirectType)))
	return p.setFieldValue(field.Elem(), key, value)
}

func (p *MultipartParser) setSlice(field reflect.Value, key string, value interface{}) error {
	if !field.IsValid() || !field.CanSet() {
		return fmt.Errorf("invalid or unsettable slice field")
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

	field.Set(newSlice)
	return nil
}

func (p *MultipartParser) setMap(field reflect.Value, key string, value interface{}) error {
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
		err := p.processValue(rv.Addr().Interface(), v, "")
		if err != nil {
			return err
		}
		field.SetMapIndex(reflect.ValueOf(k), rv)
	}
	return nil
}

func (p *MultipartParser) setStruct(field reflect.Value, key string, value interface{}) error {
	// Handle time-like structs from string form values
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
		err = p.initStruct(field.Addr().Interface(), nestedValue)
		if err != nil {
			return err
		}
		return nil
	}
	return nil
}

func (p *MultipartParser) setPrimitive(field reflect.Value, value interface{}) error {
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

func (p *MultipartParser) callBindMethodIfExists(command any, fieldName string, value any) (bool, error) {
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

	// Get the method value from the receiver
	methodValue := rvArg.MethodByName(method.Name)
	if !methodValue.IsValid() {
		return false, nil
	}

	var processedValue any
	if _, ok := value.(map[string]any); !ok {
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

	outputs := methodValue.Call([]reflect.Value{reflect.ValueOf(processedValue)})
	if len(outputs) > 0 {
		if outputs[0].IsZero() {
			return true, nil
		}

		output, ok := outputs[0].Interface().(error)
		if !ok {
			return false, fmt.Errorf("Bind method must return error or nothing")
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

func (p *MultipartParser) initDomain(field reflect.Value, fieldName string, value any) (bool, error) {
	if field.Kind() != reflect.Struct {
		return false, nil
	}

	s := reflect.New(field.Type()).Interface()
	if !util.IsGenericImplemented(s, (*core.IDomain[any])(nil)) {
		return false, nil
	}

	scheme := cache.GetDomainSchemeCache().ParseDomain(s)
	if scheme == nil {
		return false, fmt.Errorf("failed to Parse domain scheme")
	}

	primaryField := scheme.PrioritizedPrimaryField
	if primaryField == nil {
		return false, fmt.Errorf("no primary field found in domain scheme")
	}

	dest := reflect.New(field.Type()).Interface()

	id, err := util.ResolvePrimitive(primaryField.FieldType.Kind(), fmt.Sprintf("%v", value))
	if err != nil {
		return false, &error2.ValidationErrors{
			error2.ValidationError{Field: fieldName, Err: fmt.Sprintf("invalid ID value: %v", err)},
		}
	}

	neededGeneralType := core.GeneralDataTypeOf(util.IndirectType(primaryField.FieldType).Kind())
	if neededGeneralType != core.GeneralDataTypeOf(reflect.TypeOf(id).Kind()) {
		return false, &error2.ValidationErrors{
			error2.ValidationError{Field: fieldName, Err: fmt.Sprintf("Input value must be %s", neededGeneralType)},
		}
	}

	if err := db.Builder().WhereEqual(primaryField.DBName, value).Get(dest); err != nil {
		return false, fmt.Errorf("failed to fetch domain: %w", err)
	}

	domainMeta, ok := dest.(core.IDomainMeta)
	if !ok {
		return false, fmt.Errorf("destination does not implement IDomainMeta")
	}

	if !domainMeta.GetLoaded() {
		return false, &error2.ValidationErrors{
			error2.ValidationError{Field: fieldName, Err: fmt.Sprintf("Record with specified primary key (%v) was not found", value)},
		}
	}

	field.Set(reflect.ValueOf(dest).Elem())
	return true, nil
}
