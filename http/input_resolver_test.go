package http

import (
	"reflect"
	"testing"

	"github.com/osbits/gorgany/app/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------- DTO test doubles

type jsonDto struct {
	Name string `json:"name"`
}

func (jsonDto) ContentType() core.ContentType { return core.ApplicationJson }

type queryDto struct {
	Page int `scheme:"page"`
}

func (queryDto) ContentType() core.ContentType { return core.Query }

type multipartDto struct{ File string }

func (multipartDto) ContentType() core.ContentType { return core.MultipartFormData }

// noContentTypeDto is the case the brief calls out: a DTO that forgets to declare
// ContentType, so the zero value falls through resolveBodyParser to nil.
type noContentTypeDto struct{ Name string }

func (noContentTypeDto) ContentType() core.ContentType { return "" }

// exoticContentTypeDto declares a content type that has no parser.
type exoticContentTypeDto struct{ Body string }

func (exoticContentTypeDto) ContentType() core.ContentType {
	return core.ContentType("application/xml")
}

// notACommand does not implement core.HttpCommand at all.
type notACommand struct{ Name string }

// ------------------------------------------------------- A3: boot-time validation

// TestAMisdeclaredDtoIsRejectedAtRegistration is the A3 requirement: a developer
// error in a DTO declaration must stop the server starting, not surface as a runtime
// 500 discovered in production.
func TestAMisdeclaredDtoIsRejectedAtRegistration(t *testing.T) {
	tests := []struct {
		name        string
		handler     core.HandlerFunc
		wantInError []string
	}{
		{
			name:        "DTO with an empty ContentType",
			handler:     func(msg core.HttpMessage, dto noContentTypeDto) {},
			wantInError: []string{"noContentTypeDto", "empty ContentType()"},
		},
		{
			name:        "DTO with an unparseable ContentType",
			handler:     func(msg core.HttpMessage, dto exoticContentTypeDto) {},
			wantInError: []string{"exoticContentTypeDto", "application/xml", "no body parser"},
		},
		{
			name:        "parameter that is not a HttpCommand",
			handler:     func(msg core.HttpMessage, arg notACommand) {},
			wantInError: []string{"notACommand", "core.HttpCommand"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHandlerParameters(tt.handler)

			require.Error(t, err, "a misdeclared handler must be rejected at boot")
			for _, want := range tt.wantInError {
				assert.Contains(t, err.Error(), want)
			}
			// The message must name the supported alternatives, so the fix is obvious.
			if len(tt.wantInError) > 1 && tt.wantInError[1] != "core.HttpCommand" {
				assert.Contains(t, err.Error(), string(core.ApplicationJson))
			}
		})
	}
}

// TestValidHandlersAreAccepted covers every shape a handler may legitimately take.
func TestValidHandlersAreAccepted(t *testing.T) {
	handlers := map[string]core.HandlerFunc{
		"message only":            func(msg core.HttpMessage) {},
		"message + json dto":      func(msg core.HttpMessage, dto jsonDto) {},
		"message + query dto":     func(msg core.HttpMessage, dto queryDto) {},
		"message + multipart dto": func(msg core.HttpMessage, dto multipartDto) {},
		"message + string param":  func(msg core.HttpMessage, id string) {},
		"message + int param":     func(msg core.HttpMessage, id int) {},
		"params then dto":         func(msg core.HttpMessage, id int64, dto jsonDto) {},
		"no parameters":           func() {},
	}

	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, ValidateHandlerParameters(handler))
		})
	}
}

func TestValidateHandlerParametersRejectsNonFunctions(t *testing.T) {
	require.Error(t, ValidateHandlerParameters(nil))
}

// ------------------------------------------------------ supported content types

// TestSupportedContentTypesMatchesResolveBodyParser guards the two lists against
// drift. If they diverged, the boot check would accept a DTO the request path then
// refuses — reintroducing the runtime failure this replaced.
func TestSupportedContentTypesMatchesResolveBodyParser(t *testing.T) {
	for _, contentType := range SupportedContentTypes() {
		t.Run(string(contentType), func(t *testing.T) {
			parser := resolveBodyParser(stubCommand{contentType: contentType}, nil)
			assert.NotNilf(t, parser,
				"%s is advertised as supported, so resolveBodyParser must build a parser for it",
				contentType)
		})
	}

	// And the converse: an unsupported type must yield no parser.
	assert.Nil(t, resolveBodyParser(stubCommand{contentType: "application/xml"}, nil))
	assert.Nil(t, resolveBodyParser(stubCommand{contentType: ""}, nil))
}

func TestIsSupportedContentType(t *testing.T) {
	assert.True(t, IsSupportedContentType(core.ApplicationJson))
	assert.True(t, IsSupportedContentType(core.MultipartFormData))
	assert.True(t, IsSupportedContentType(core.Query))
	assert.False(t, IsSupportedContentType(""))
	assert.False(t, IsSupportedContentType("application/xml"))
}

// TestUnsupportedContentTypeErrorNamesTheDtoAndTheFix
func TestUnsupportedContentTypeErrorNamesTheDtoAndTheFix(t *testing.T) {
	err := unsupportedContentTypeError(reflect.TypeOf(exoticContentTypeDto{}), "application/xml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exoticContentTypeDto")
	assert.Contains(t, err.Error(), "application/xml")
	assert.Contains(t, err.Error(), string(core.ApplicationJson))

	empty := unsupportedContentTypeError(reflect.TypeOf(noContentTypeDto{}), "")
	require.Error(t, empty)
	assert.Contains(t, empty.Error(), "empty ContentType()")
}

type stubCommand struct{ contentType core.ContentType }

func (s stubCommand) ContentType() core.ContentType { return s.contentType }
