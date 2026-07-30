package middleware

import (
	"errors"
	"net/http"
	"testing"

	"github.com/osbits/gorgany/v2/app/core"
	error2 "github.com/osbits/gorgany/v2/err"
	grghttp "github.com/osbits/gorgany/v2/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withErrorHandlers installs error handlers for the duration of a test.
func withErrorHandlers(t *testing.T, handlers map[string]core.ErrorHandler) {
	t.Helper()
	grghttp.SetErrorHandlers(handlers)
	t.Cleanup(func() { grghttp.SetErrorHandlers(map[string]core.ErrorHandler{}) })
}

// TestRecoveryTurnsAPanicIntoAResponse is the T3.1 headline: nothing in the HTTP
// pipeline recovered, so a panic dropped the connection with no response at all.
func TestRecoveryTurnsAPanicIntoAResponse(t *testing.T) {
	withErrorHandlers(t, map[string]core.ErrorHandler{})

	message := newMessage(http.MethodGet, "/boom", nil)

	handler := NewRecoveryMiddleware().Handle(func(core.HttpMessage) {
		panic("kaboom")
	})

	require.NotPanics(t, func() { handler(message) })

	assert.True(t, message.recorded.Written, "a panic must still produce a response")
	assert.Equal(t, http.StatusInternalServerError, message.recorded.Status)
}

// TestRecoveryEmitsTheStandardEnvelopeForANonError
func TestRecoveryEmitsTheStandardEnvelopeForANonError(t *testing.T) {
	withErrorHandlers(t, map[string]core.ErrorHandler{})

	message := newMessage(http.MethodGet, "/boom", nil)
	handler := NewRecoveryMiddleware().Handle(func(core.HttpMessage) { panic(42) })

	handler(message)

	body, err := envelope(message.recorded.Body)
	require.NoError(t, err)
	assert.Equal(t, float64(500), body["status"])
	assert.Equal(t, "INTERNAL_ERROR", body["status_code"])
	assert.Contains(t, body["errors"], "42")
}

// TestRecoveryRedispatchesAnErrorThroughTheRegisteredHandlers is the reason this
// middleware matters beyond not dropping connections. JwtMiddleware reports failure
// by panicking with err.NewJwtAuthError() precisely so a registered JwtAuthError
// handler will run — and with nothing recovering, that handler was unreachable.
func TestRecoveryRedispatchesAnErrorThroughTheRegisteredHandlers(t *testing.T) {
	called := false
	withErrorHandlers(t, map[string]core.ErrorHandler{
		"JwtAuthError": func(err error, message core.HttpMessage) {
			called = true
			message.Response().Text("handled", http.StatusUnauthorized)
		},
	})

	message := newMessage(http.MethodGet, "/protected", nil)
	handler := NewRecoveryMiddleware().Handle(func(core.HttpMessage) {
		panic(error2.NewJwtAuthError())
	})

	require.NotPanics(t, func() { handler(message) })

	assert.True(t, called, "the registered JwtAuthError handler must fire")
	assert.Equal(t, http.StatusUnauthorized, message.recorded.Status)
	assert.Equal(t, "handled", message.recorded.Text)
}

// TestRecoveryFallsBackToTheDefaultHandlerForAnUnregisteredError
func TestRecoveryFallsBackToTheDefaultHandlerForAnUnregisteredError(t *testing.T) {
	called := false
	withErrorHandlers(t, map[string]core.ErrorHandler{
		"Default": func(err error, message core.HttpMessage) {
			called = true
			message.Response().Text(err.Error(), http.StatusInternalServerError)
		},
	})

	message := newMessage(http.MethodGet, "/boom", nil)
	handler := NewRecoveryMiddleware().Handle(func(core.HttpMessage) {
		panic(errors.New("something specific"))
	})

	handler(message)

	assert.True(t, called)
	assert.Equal(t, "something specific", message.recorded.Text)
}

// TestRecoverySurvivesAPanickingErrorHandler: a handler blowing up must not take
// the connection down with it.
func TestRecoverySurvivesAPanickingErrorHandler(t *testing.T) {
	withErrorHandlers(t, map[string]core.ErrorHandler{
		"Default": func(error, core.HttpMessage) { panic("handler is broken too") },
	})

	message := newMessage(http.MethodGet, "/boom", nil)
	handler := NewRecoveryMiddleware().Handle(func(core.HttpMessage) {
		panic(errors.New("original"))
	})

	require.NotPanics(t, func() { handler(message) })

	assert.Equal(t, http.StatusInternalServerError, message.recorded.Status)
	body, err := envelope(message.recorded.Body)
	require.NoError(t, err)
	assert.Contains(t, body["errors"], "original")
}

// TestRecoveryPassesThroughWhenNothingPanics
func TestRecoveryPassesThroughWhenNothingPanics(t *testing.T) {
	message := newMessage(http.MethodGet, "/fine", nil)

	reached := false
	handler := NewRecoveryMiddleware().Handle(func(core.HttpMessage) {
		reached = true
		message.Response().Text("ok", http.StatusOK)
	})

	handler(message)

	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, message.recorded.Status)
	assert.Equal(t, "ok", message.recorded.Text)
}

// TestRecoveryIsRegisteredByDefault is asserted from the provider side; here we
// only pin that it satisfies the middleware contract.
func TestRecoveryImplementsIMiddleware(t *testing.T) {
	var mw any = NewRecoveryMiddleware()
	_, ok := mw.(core.IMiddleware)
	assert.True(t, ok)
}
