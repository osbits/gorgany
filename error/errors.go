package error

import (
	"fmt"
	"strings"
)

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
