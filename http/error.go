package http

import (
	"fmt"
	"gorgany"
	error2 "gorgany/error"
	"gorgany/grg"
	"gorgany/internal"
	"gorgany/proxy"
	"reflect"
)

var defaultErrorHandlerMap = map[string]proxy.ErrorHandler{
	"ValidationErrors":     processValidationErrors,
	"InputBodyParseError":  processInputParsingError,
	"InputParamParseError": processBodyParsingError,
	"Default":              processDefaultError,
	"JwtAuthError":         processJwtAuthError,
}

func getErrorHandler(key string) proxy.ErrorHandler {
	customHandlers := internal.GetFrameworkRegistrar().GetErrorHandlers()
	if handler, ok := customHandlers[key]; ok {
		return handler
	}

	return defaultErrorHandlerMap[key]
}

func Catch(err error, message proxy.HttpMessage) {
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

func processDefaultError(err error, message proxy.HttpMessage) {
	error2.PrintError(err)
	if grg.GetRunMode() == gorgany.Dev {
		message.Response(fmt.Sprintf("Oops... 500 error.\n %v \n%s", err, error2.GetStacktrace()), 500)
	} else {
		message.Response("Oops... Internal error.", 500)
	}
}

func processValidationErrors(error error, message proxy.HttpMessage) {
	concreteError := error.(error2.ValidationErrors)
	req := message.GetRequest()
	message.RedirectWithParams(req.Referer(), 301, map[string]any{"validation": concreteError.Errors})
	return
}

func processInputParsingError(error error, message proxy.HttpMessage) {
	error2.PrintError(error)
	message.Response("", 400)
	return
}

func processBodyParsingError(error error, message proxy.HttpMessage) {
	error2.PrintError(error)
	message.Response("", 400)
	return
}

func processJwtAuthError(err error, message proxy.HttpMessage) {
	message.ResponseJSON("", 401)
}
