package http

import (
	"fmt"
	"gorgany"
	"gorgany/app"
	"gorgany/app/core"
	error2 "gorgany/error"
	"gorgany/internal"
	"reflect"
)

var defaultErrorHandlerMap = map[string]core.ErrorHandler{
	"ValidationErrors":     processValidationErrors,
	"InputBodyParseError":  processInputParsingError,
	"InputParamParseError": processBodyParsingError,
	"Default":              processDefaultError,
	"JwtAuthError":         processJwtAuthError,
}

func getErrorHandler(key string) core.ErrorHandler {
	customHandlers := internal.GetFrameworkRegistrar().GetErrorHandlers()

	if errorHandler, ok := customHandlers[key]; ok {
		return errorHandler
	}

	if defaultHandler, ok := defaultErrorHandlerMap[key]; ok {
		return defaultHandler
	}

	defaultHandler, ok := customHandlers["Default"]
	if !ok {
		return processDefaultError
	} else {
		return defaultHandler
	}
}

func Catch(err error, message core.HttpMessage) {
	reflectedErr := reflect.TypeOf(err)
	if reflectedErr.Kind() == reflect.Ptr {
		reflectedErr = reflectedErr.Elem()
	}

	errName := reflectedErr.Name()
	errorHandler := getErrorHandler(errName)
	if errorHandler == nil {
		processDefaultError(err, message)
		return
	}

	errorHandler(err, message)
}

func processDefaultError(err error, message core.HttpMessage) {
	error2.PrintError(err)
	if app.GetRunMode() == gorgany.Dev {
		message.Response(fmt.Sprintf("Oops... 500 error.\n %v \n%s", err, error2.GetStacktrace()), 500)
	} else {
		message.Response("Oops... Internal error.", 500)
	}
}

func processValidationErrors(error error, message core.HttpMessage) {
	concreteError := error.(error2.ValidationErrors)
	req := message.GetRequest()
	message.RedirectWithParams(req.Referer(), 301, map[string]any{"validation": concreteError.Errors})
	return
}

func processInputParsingError(error error, message core.HttpMessage) {
	error2.PrintError(error)
	message.Response("", 400)
	return
}

func processBodyParsingError(error error, message core.HttpMessage) {
	error2.PrintError(error)
	message.Response("", 400)
	return
}

func processJwtAuthError(err error, message core.HttpMessage) {
	message.ResponseJSON("", 401)
}
