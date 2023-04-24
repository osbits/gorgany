package error

import "strings"

type ValidationError struct {
	Errors []error
}

func (thiz ValidationError) Error() string {
	errs := make([]string, 0)
	for _, err := range thiz.Errors {
		errs = append(errs, err.Error())
	}
	return strings.Join(errs, "\n")
}
