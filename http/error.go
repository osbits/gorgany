package http

import (
	"fmt"
	"git.qix.sx/gorgany/gorgany.git"
	"git.qix.sx/gorgany/gorgany.git/app"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
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
		message.Response(fmt.Sprintf("Oops... 500 error.\n %v \n%s", err, error2.GetStacktrace()), 500)
		return
	}

	message.Response("Oops... Internal error.", 500)
}

func processValidationErrors(error error, message core.HttpMessage) {
	concreteError := error.(*error2.ValidationErrors)
	req := message.GetRequest()
	message.RedirectWithParams(req.Referer(), 301, map[string]any{"validation": concreteError})
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
	message.Response("", 404)
	return
}

func processBodyParsingError(error error, message core.HttpMessage) {
	error2.PrintError(error)
	message.Response("", 400)
	return
}

func processJwtAuthError(err error, message core.HttpMessage) {
	message.Response("", 401)
	return
}
