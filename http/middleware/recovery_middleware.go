package middleware

import (
	"fmt"

	"github.com/osbits/gorgany/app/core"
	error2 "github.com/osbits/gorgany/err"
	grghttp "github.com/osbits/gorgany/http"
	"github.com/osbits/gorgany/log"
	"github.com/osbits/gorgany/service/dto"
)

// RecoveryMiddleware recovers from a panic raised anywhere downstream of it.
//
// Nothing in the HTTP pipeline recovered before v2, which had two consequences:
// a panic dropped the connection with no response at all, and — worse — a
// registered error handler could never fire for any middleware that reports
// failure by panicking. JwtMiddleware panics with err.NewJwtAuthError() precisely
// so the registered JwtAuthError handler will run, and that handler was
// unreachable.
//
// A recovered value that is an error is therefore re-dispatched through the
// existing error-handler chain (grghttp.Catch), so registered handlers fire
// exactly as they would have if the middleware had returned the error. Anything
// else is logged with its stacktrace and answered with the framework's standard
// 500 envelope.
//
// RouteProvider registers it as the first /** filter automatically, so it wraps
// everything including the other middlewares and an app does not have to remember.
// Call RouteProvider.DisableRecoveryMiddleware() to opt out.
type RecoveryMiddleware struct{}

// NewRecoveryMiddleware creates the recovery middleware.
func NewRecoveryMiddleware() *RecoveryMiddleware {
	return &RecoveryMiddleware{}
}

var _ core.IMiddleware = (*RecoveryMiddleware)(nil)

// Handle implements the middleware handler.
func (thiz RecoveryMiddleware) Handle(next func(core.HttpMessage)) func(core.HttpMessage) {
	return func(message core.HttpMessage) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			thiz.dispatch(recovered, message)
		}()

		next(message)
	}
}

// dispatch routes a recovered value to the error-handler chain or to the standard
// 500 envelope.
func (thiz RecoveryMiddleware) dispatch(recovered any, message core.HttpMessage) {
	// The stacktrace is captured here, while the deferred call is still on the
	// panicking goroutine's stack.
	stacktrace := error2.GetStacktrace()

	if message == nil {
		log.Log().Errorf("recovered panic with no message in context: %v\n%s", recovered, stacktrace)
		return
	}

	if err, ok := recovered.(error); ok {
		// Re-dispatch through the registered handlers. This is what makes a
		// registered JwtAuthError handler reachable.
		thiz.catch(err, message, stacktrace)
		return
	}

	log.Log().Errorf("recovered panic: %v\n%s", recovered, stacktrace)
	thiz.respondInternalError(message, fmt.Sprintf("%v", recovered))
}

// catch hands err to the error-handler chain, falling back to the standard
// envelope if a handler itself panics — a handler blowing up must not take the
// connection down with it.
func (thiz RecoveryMiddleware) catch(err error, message core.HttpMessage, stacktrace string) {
	defer func() {
		if inner := recover(); inner != nil {
			log.Log().Errorf(
				"error handler panicked while handling %v: %v\n%s",
				err, inner, error2.GetStacktrace())
			thiz.respondInternalError(message, err.Error())
		}
	}()

	log.Log().Errorf("recovered panic: %v\n%s", err, stacktrace)
	grghttp.Catch(err, message)
}

// respondInternalError writes the framework's standard 500 envelope, guarding
// against a response that has already been written.
func (thiz RecoveryMiddleware) respondInternalError(message core.HttpMessage, detail string) {
	defer func() {
		// Writing to an already-committed response is not worth a second panic.
		_ = recover()
	}()

	message.Response().JSON(
		dto.ReturnObject(nil, core.InternalErrorHttpStatus, detail),
		core.InternalErrorHttpStatus.Status,
	)
}
