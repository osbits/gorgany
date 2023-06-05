package error

import (
	"fmt"
	"strings"
)

// Validation
type ValidationErrors struct {
	Errors []ValidationError
}

func (thiz ValidationErrors) Error() string {
	errs := make([]string, 0)
	for _, err := range thiz.Errors {
		errs = append(errs, err.Error())
	}
	return strings.Join(errs, "\n")
}

type ValidationError struct {
	Field string
	Err   string
}

func (thiz ValidationError) Error() string {
	return fmt.Sprintf("Field: %s, Error: %v", thiz.Field, thiz.Err)
}

// InputParamParseError
func NewInputParamParseError(value string, kind string) *InputParamParseError {
	return &InputParamParseError{
		Value: value,
		Type:  kind,
	}
}

type InputParamParseError struct {
	Value string
	Type  string
}

func (thiz InputParamParseError) Error() string {
	return fmt.Sprintf("Unable to convert `%s` to %s", thiz.Value, thiz.Type)
}

// InputBodyParseError
func NewInputBodyParseError(body string, kind string, err error) *InputBodyParseError {
	return &InputBodyParseError{
		Body:     body,
		Type:     kind,
		RawError: err,
	}
}

type InputBodyParseError struct {
	Body     string
	Type     string
	RawError error
}

func (thiz InputBodyParseError) Error() string {
	return fmt.Sprintf("Unable to convert body from %s. Error: %v\nBody: %s", thiz.Type, thiz.RawError, thiz.Body)
}
