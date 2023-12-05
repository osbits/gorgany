package http

import (
	"fmt"
	"gorgany"
	"gorgany/app"
	"gorgany/app/core"
	error2 "gorgany/err"
	"gorgany/internal"
	"gorgany/service/dto"
	"reflect"
)

var defaultErrorHandlerMap = map[string]core.ErrorHandler{
	"ValidationErrors":     processValidationErrors,
	"InputBodyParseError":  processBodyParsingError,
	"InputParamParseError": processInputParsingError,
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
		if message.GetHeader().Get("Content-Type") == core.ApplicationJson || message.IsApiNamespace() {
			message.ResponseJSON(dto.ReturnObject(nil, core.InternalErrorHttpStatus, err.Error()), 200)
			return
		}
		message.Response(fmt.Sprintf("Oops... 500 error.\n %v \n%s", err, error2.GetStacktrace()), 500)
		return
	}

	message.Response("Oops... Internal error.", 500)
}

func processValidationErrors(error error, message core.HttpMessage) {
	concreteError := error.(*error2.ValidationErrors)
	req := message.GetRequest()
	if message.GetHeader().Get("Content-Type") == core.ApplicationJson || message.IsApiNamespace() {
		message.ResponseJSON(error, 429)
		return
	}
	message.RedirectWithParams(req.Referer(), 301, map[string]any{"validation": concreteError.Errors})
}

func processInputParsingError(error error, message core.HttpMessage) {
	error2.PrintError(error)
	message.Response("", 404)
	return
}

func processBodyParsingError(error error, message core.HttpMessage) {
	error2.PrintError(error)
	message.Response("", 400)
	return
}

func processJwtAuthError(err error, message core.HttpMessage) {
	if message.GetHeader().Get("Content-Type") == core.ApplicationJson || message.IsApiNamespace() {
		message.ResponseJSON(dto.ReturnObject(nil, core.NotAuthorizedHttpStatus, "Invalid JWT"), 401)
		return
	}
	message.Response("", 401)
	return
}
