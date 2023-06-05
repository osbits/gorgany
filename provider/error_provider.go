package provider

import "gorgany/http"

func NewErrorProvider() *ErrorProvider {
	return &ErrorProvider{}
}

type ErrorProvider struct{}

func (thiz ErrorProvider) InitProvider() {
	for errType, handler := range FrameworkRegistrar.customErrorHandlers {
		http.SetErrorHandler(errType, handler)
	}
}
