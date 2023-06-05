package http

import (
	"fmt"
	error2 "gorgany/error"
	"reflect"
)

type ErrorHandler func(error, *Message)

var errorHandlerMap = map[string]ErrorHandler{
	"ValidationErrors":     processValidationErrors,
	"InputBodyParseError":  processInputParsingError,
	"InputParamParseError": processBodyParsingError,
	"Default":              processDefaultError,
	"JwtAuthError":         processJwtAuthError,
}

func SetErrorHandler(errType string, handlerFunc ErrorHandler) {
	errorHandlerMap[errType] = handlerFunc
}

func Catch(err error, message *Message) {
	reflectedErr := reflect.TypeOf(err)
	if reflectedErr.Kind() == reflect.Ptr {
		reflectedErr = reflectedErr.Elem()
	}

	errorHandler, ok := errorHandlerMap[reflectedErr.Name()]
	if ok {
		errorHandler(err, message)
		return
	}

	defaultHandler, ok := errorHandlerMap["Default"]
	if !ok {
		processDefaultError(err, message)
	} else {
		defaultHandler(err, message)
	}
}

func processDefaultError(err error, message *Message) {
	error2.PrintStacktrace(err)
	message.Response(fmt.Sprintf("Oops... 500 error.\n %v", err), 500)
}

func processValidationErrors(error error, message *Message) {
	concreteError := error.(error2.ValidationErrors)
	req := message.GetRequest()
	message.RedirectWithParams(req.Referer(), 301, map[string]any{"validation": concreteError.Errors})
	return
}

func processInputParsingError(error error, message *Message) {
	message.Response("", 404)
	return
}

func processBodyParsingError(error error, message *Message) {
	error2.PrintStacktrace(error)
	message.Response("", 400)
	return
}

func processJwtAuthError(err error, message *Message) {
	message.ResponseJSON("", 401)
}
