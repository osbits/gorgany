package http

import (
	"fmt"
	"github.com/osbits/gorgany"
	"github.com/osbits/gorgany/app"
	"github.com/osbits/gorgany/app/core"
	error2 "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/service/dto"
	"reflect"
)

var defaultHandlers = map[string]core.ErrorHandler{
	"ValidationErrors":     processValidationErrors,
	"ValidationError":      processValidationError,
	"InputBodyParseError":  processBodyParsingError,
	"InputParamParseError": processInputParsingError,
	"Default":              processDefaultError,
	"JwtAuthError":         processJwtAuthError,
}

var customHandlers = make(map[string]core.ErrorHandler)

func SetErrorHandlers(handlers map[string]core.ErrorHandler) {
	customHandlers = handlers
}

func GetErrorHandler(errName string) core.ErrorHandler {
	if h, ok := customHandlers[errName]; ok {
		return h
	}
	if h, ok := defaultHandlers[errName]; ok {
		return h
	}
	if h, ok := customHandlers["Default"]; ok {
		return h
	}

	return processDefaultError
}

func Catch(err error, message core.HttpMessage) {
	reflectedErr := reflect.TypeOf(err)
	if reflectedErr.Kind() == reflect.Ptr {
		reflectedErr = reflectedErr.Elem()
	}

	errName := reflectedErr.Name()
	errorHandler := GetErrorHandler(errName)
	if errorHandler == nil {
		processDefaultError(err, message)
		return
	}

	errorHandler(err, message)
}

func processDefaultError(err error, message core.HttpMessage) {
	error2.PrintError(err)
	if app.GetRunMode() == gorgany.Dev {
		message.Response().Text(fmt.Sprintf("Oops... 500 error.\n %v \n%s", err, error2.GetStacktrace()), 500)
		return
	}

	message.Response().Text("Oops... Internal error.", 500)
}

func processValidationErrors(error error, message core.HttpMessage) {
	concreteError := error.(*error2.ValidationErrors)
	req := message.Request().RawRequest()
	message.RedirectWithFlash(req.Referer(), 301, map[string]any{"validation": concreteError})
}

func processValidationError(error error, message core.HttpMessage) {
	concreteError := error.(*error2.ValidationError)
	validationErrors := &error2.ValidationErrors{
		*concreteError,
	}
	processValidationErrors(validationErrors, message)
}

func processInputParsingError(error error, message core.HttpMessage) {
	error2.PrintError(error)
	message.Response().Text("", 404)
	return
}

// processBodyParsingError answers a body the framework could not parse with 400.
//
// It used to write Text("", 400) — an empty body with a text/plain content type, which
// told an API client nothing. It was also unreachable: nothing constructed
// InputBodyParseError, so a malformed body arrived as an empty ValidationErrors and
// processValidationErrors redirected to the Referer with a 301 (B3).
//
// The response never echoes the body back. InputBodyParseError carries it for the log,
// and a body that failed to parse is exactly the kind that might contain a password
// halfway through.
func processBodyParsingError(err error, message core.HttpMessage) {
	error2.PrintError(err)

	reason := "The request body could not be parsed"
	if parseError, ok := err.(*error2.InputBodyParseError); ok && parseError.RawError != nil {
		reason = parseError.RawError.Error()
	}

	if WantsJSON(message) {
		message.Response().JSON(
			dto.ReturnObject(nil, core.BadRequestHttpStatus, reason),
			core.BadRequestHttpStatus.Status)
		return
	}

	message.Response().Text(reason, core.BadRequestHttpStatus.Status)
}

func processJwtAuthError(err error, message core.HttpMessage) {
	message.Response().Text("", 401)
	return
}
