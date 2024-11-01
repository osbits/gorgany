package core

import (
	"github.com/go-playground/validator/v10"
)

// IValidator based on go-playground/validator
type IValidator interface {
	SetTagName(name string)

	ValidateStruct(s any) error
	ValidateMap(data map[string]any, rules map[string]any) map[string]any

	RegisterValidation(tag string, fn validator.Func, callValidationEvenIfNull ...bool) error
	RegisterStructValidation(fn validator.StructLevelFunc, types ...interface{})
}
