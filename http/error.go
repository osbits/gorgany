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

// processDefaultError is the catch-all 500.
//
// It used to answer every caller with text/plain, so an API client got an HTML-shaped
// body it could not parse (C2). The dev-mode detail — the error and a stacktrace — is
// unchanged and still only in dev; prod stays generic, because an unclassified error
// message can carry anything.
func processDefaultError(err error, message core.HttpMessage) {
	error2.PrintError(err)

	reason := "Oops... Internal error."
	if app.GetRunMode() == gorgany.Dev {
		reason = fmt.Sprintf("Oops... 500 error.\n %v \n%s", err, error2.GetStacktrace())
	}

	if WantsJSON(message) {
		message.Response().JSON(
			dto.ReturnObject(nil, core.InternalErrorHttpStatus, reason),
			core.InternalErrorHttpStatus.Status)
		return
	}

	message.Response().Text(reason, core.InternalErrorHttpStatus.Status)
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

// processInputParsingError answers a path or query parameter that could not be
// converted to the handler's parameter type.
//
// It used to write Text("", 404) — an empty body, and a 404 for what is a malformed
// request rather than a missing resource. The status is unchanged to keep the break
// behaviour-only where it is observable, but the body is now negotiated and says what
// went wrong (C2).
func processInputParsingError(err error, message core.HttpMessage) {
	error2.PrintError(err)

	reason := "A request parameter could not be parsed"
	if parseError, ok := err.(*error2.InputParamParseError); ok {
		reason = parseError.Error()
	}

	if WantsJSON(message) {
		message.Response().JSON(
			dto.ReturnObject(nil, core.NotFoundHttpStatus, reason),
			core.NotFoundHttpStatus.Status)
		return
	}

	message.Response().Text(reason, core.NotFoundHttpStatus.Status)
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

// processJwtAuthError answers an invalid or expired Bearer token.
//
// It used to write Text("", 401) — an empty body, which is the one case where that was
// almost defensible, since a JWT client rarely reads it. Negotiated anyway, so an API
// client gets the same envelope shape from every error path (C2).
func processJwtAuthError(err error, message core.HttpMessage) {
	error2.PrintError(err)

	const reason = "Unauthenticated. JWT is invalid or expired"

	if WantsJSON(message) {
		message.Response().JSON(
			dto.ReturnObject(nil, core.NotAuthorizedHttpStatus, reason),
			core.NotAuthorizedHttpStatus.Status)
		return
	}

	message.Response().Text(reason, core.NotAuthorizedHttpStatus.Status)
}
