package err

import (
	"fmt"
	"gorgany/log"
	"runtime"
	"strings"
)

func GetStacktrace() string {
	buf := make([]byte, 1<<16)
	runtime.Stack(buf, false)
	stack := fmt.Sprintf("%s", buf)
	splitStack := strings.Split(stack, "\n")
	return strings.Join(splitStack[0:len(splitStack)-1], "\n")
}

func PrintError(err any) {
	log.Log("").Error(err)
	log.Log("").Error(GetStacktrace())
}

func HandleError(err any) {
	if err == nil {
		return
	}
	log.Log("").Error("\u001B[0;31mRuntime error: \u001B[0m")
	log.Log("").Error(err)
}

func HandleErrorWithStacktrace(err any) {
	if err == nil {
		return
	}
	log.Log("").Error("\u001B[0;31mRuntime error: \u001B[0m")
	PrintError(err)
}

// Validation
type ValidationErrors struct {
	Errors []ValidationError
}

func (thiz *ValidationErrors) Error() string {
	errs := make([]string, 0)
	for _, err := range thiz.Errors {
		errs = append(errs, err.Error())
	}
	return strings.Join(errs, "\n")
}

func (thiz *ValidationErrors) AddValidationError(validationError ValidationError) {
	thiz.Errors = append(thiz.Errors, validationError)
}

type ValidationError struct {
	Field string `json:"field"`
	Err   string `json:"err"`
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

// JwtAuthError
func NewJwtAuthError() *JwtAuthError {
	return &JwtAuthError{}
}

type JwtAuthError struct{}

func (thiz JwtAuthError) Error() string {
	return "Unauthenticated. JWT is invalid or expired"
}
