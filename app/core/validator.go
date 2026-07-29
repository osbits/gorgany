package core

import (
	"github.com/go-playground/validator/v10"
)

// IValidator defines the interface for data validation based on go-playground/validator
type IValidator interface {
	// SetTagName sets the struct tag name to use for validation
	SetTagName(name string)

	// ValidateStruct validates a struct against its validation tags
	ValidateStruct(s any) error
	// ValidateMap validates a map against provided validation rules
	ValidateMap(data map[string]any, rules map[string]any) map[string]any

	// RegisterValidation registers a custom validation function for a tag
	RegisterValidation(tag string, fn validator.Func, callValidationEvenIfNull ...bool) error
	// RegisterStructValidation registers a custom validation function for a struct type
	RegisterStructValidation(fn validator.StructLevelFunc, types ...interface{})
	// RegisterCustomTypeFunc registers a custom type function for validation
	RegisterCustomTypeFunc(fn validator.CustomTypeFunc, types ...interface{})
}

// ILocalizedValidator is implemented by a validator that can render its messages in a
// specific locale. It is a separate, optional interface rather than a change to
// IValidator so an existing implementation keeps compiling; callers that have a locale
// available — the HTTP input resolver has the request's — type-assert for it and fall
// back to IValidator.ValidateStruct, which uses the default locale.
type ILocalizedValidator interface {
	// ValidateStructForLocale validates a struct and renders its messages in locale.
	ValidateStructForLocale(s any, locale string) error
}
