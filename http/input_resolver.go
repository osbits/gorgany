package http

import (
	"errors"
	"fmt"
	"github.com/go-chi/chi"
	"github.com/osbits/gorgany/app/core"
	error2 "github.com/osbits/gorgany/err"
	"github.com/osbits/gorgany/i18n"
	"github.com/osbits/gorgany/util"
	"reflect"
	"strings"
)

type InputResolver struct {
	ReflectedHandler reflect.Value
	Message          core.HttpMessage
	Validator        core.IValidator `container:"inject"`
}

func (thiz *InputResolver) Resolve() ([]reflect.Value, error) {
	args := make([]reflect.Value, 0)
	pathParams := thiz.collectPathParams()
	indexOfPrimitiveArguemnt := 0
	for i := 0; i < thiz.ReflectedHandler.Type().NumIn(); i++ {
		in := thiz.ReflectedHandler.Type().In(i)
		argTypeName := in.String()

		if in.Implements(reflect.TypeOf((*core.HttpMessage)(nil)).Elem()) {
			args = append(args, reflect.ValueOf(thiz.Message))
			continue
		}

		reflectedInValue := reflect.New(in)
		arg := reflectedInValue.Interface()

		switch argTypeName {
		case "string", "bool", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
			if len(pathParams) < indexOfPrimitiveArguemnt {
				continue
			}
			param := pathParams[indexOfPrimitiveArguemnt]
			var err error
			arg, err = util.ResolvePrimitive(in.Kind(), param)
			if err != nil {
				parseError := &error2.InputParamParseError{}
				if errors.As(err, &parseError) {
					return nil, &error2.ValidationErrors{{Field: core.GeneralError, Err: parseError.Error()}}
				}
				return nil, err
			}
			indexOfPrimitiveArguemnt++
		default:
			// Both of the checks below used to log a warning and carry on, and both
			// then panicked a line or two later.
			//
			// `continue` skipped the args append at the bottom of the loop, so the
			// handler was later invoked through reflect.Call with too few arguments —
			// a panic whose message points at reflection rather than at the DTO that
			// is missing ContentType().
			httpCommand, ok := arg.(core.HttpCommand)
			if !ok {
				return nil, fmt.Errorf(
					"handler %s takes parameter %d of type %s, which does not implement "+
						"core.HttpCommand; a handler parameter must be core.HttpMessage, a "+
						"primitive bound from a path parameter, or a DTO implementing "+
						"core.HttpCommand",
					thiz.ReflectedHandler.Type(), i, in)
			}

			// And this one warned about an unresolvable parser, did not return, and
			// dereferenced nil on the very next line.
			parser := resolveBodyParser(httpCommand, thiz.Message)
			if parser == nil {
				return nil, unsupportedContentTypeError(in, httpCommand.ContentType())
			}

			err := parser.Parse(arg)
			if err != nil {
				return nil, err
			}

			//err = service.GetContainer().Make(&arg) error: invalid structure due to arg is an interface{} but not a concrete type
			//if err != nil {
			//	return nil, err
			//}

			if err := thiz.validate(arg); err != nil {
				return nil, err
			}
		}

		args = append(args, reflect.Indirect(reflect.ValueOf(arg)))
	}

	//thiz.Message.(*Message).inputParameters = args

	return args, nil
}

// validate runs the DTO through the validator, asking for messages in the request's
// locale when the validator can produce them.
//
// The locale is only available here, on the request, and core.IValidator's
// ValidateStruct takes no locale — so this asks for the optional interface and falls
// back to the default locale for a validator that does not implement it.
func (thiz *InputResolver) validate(arg any) error {
	if localized, ok := thiz.Validator.(core.ILocalizedValidator); ok {
		return localized.ValidateStructForLocale(arg, thiz.requestLocale())
	}
	return thiz.Validator.ValidateStruct(arg)
}

// requestLocale is the {lang} path parameter, falling back to the configured default —
// the same resolution the view renderer uses.
func (thiz *InputResolver) requestLocale() string {
	if req := thiz.Message.Request(); req != nil {
		if lang := req.PathParam("lang"); lang != "" {
			return lang
		}
	}
	return i18n.DefaultLocale()
}

func (thiz *InputResolver) collectPathParams() []string {
	routeParams := chi.RouteContext(thiz.Message.Request().RawRequest().Context()).URLParams

	pathParams := make([]string, 0)
	for i := range routeParams.Values {
		if routeParams.Keys[i] == "namespace" || routeParams.Keys[i] == "lang" {
			continue
		}
		pathParams = append(pathParams, routeParams.Values[i])
	}
	return pathParams
}

type bodyParser interface {
	Parse(arg interface{}) error
}

func resolveBodyParser(command core.HttpCommand, message core.HttpMessage) bodyParser {
	if command.ContentType() == core.ApplicationJson {
		return &JsonParser{message: message}
	} else if command.ContentType() == core.MultipartFormData {
		return &MultipartParser{message: message}
	} else if command.ContentType() == core.Query {
		return &QueryParser{message: message}
	}

	return nil
}

func checkAndAddIfValidationError(err error, validationErrors *error2.ValidationErrors) {
	if errors.Is(err, &error2.ValidationError{}) {
		validationErrors.AddValidationError(err.(error2.ValidationError))
		return
	}
	validationErrors.AddValidationError(error2.ValidationError{
		Field: core.GeneralError,
		Err:   err.Error(),
	})
}

// unsupportedContentTypeError describes a DTO whose ContentType has no parser.
//
// resolveBodyParser returns nil for anything that is not ApplicationJson,
// MultipartFormData or Query — including the zero value, if a DTO simply forgets to
// declare ContentType(). That is a developer error in a type declaration, and it
// should never have been discoverable only as a runtime 500.
func unsupportedContentTypeError(dto reflect.Type, contentType core.ContentType) error {
	if contentType == "" {
		return fmt.Errorf(
			"DTO %s returns an empty ContentType(), so no body parser can be selected; "+
				"return one of %s", dto, strings.Join(SupportedContentTypeNames(), ", "))
	}
	return fmt.Errorf(
		"DTO %s returns ContentType %q, which has no body parser; supported: %s",
		dto, contentType, strings.Join(SupportedContentTypeNames(), ", "))
}

// SupportedContentTypes are the content types a DTO may declare, i.e. the ones
// resolveBodyParser can build a parser for.
//
// Kept beside resolveBodyParser deliberately: the two must not drift, or the
// boot-time check would pass a DTO the request path then rejects.
func SupportedContentTypes() []core.ContentType {
	return []core.ContentType{
		core.ApplicationJson,
		core.MultipartFormData,
		core.Query,
	}
}

// SupportedContentTypeNames is SupportedContentTypes as strings, for error messages.
func SupportedContentTypeNames() []string {
	types := SupportedContentTypes()
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, string(t))
	}
	return names
}

// IsSupportedContentType reports whether a parser exists for contentType.
func IsSupportedContentType(contentType core.ContentType) bool {
	for _, supported := range SupportedContentTypes() {
		if supported == contentType {
			return true
		}
	}
	return false
}

// ValidateHandlerParameters checks, at registration time, that every parameter of a
// route handler can actually be resolved.
//
// A misdeclared DTO should stop the server starting, not serve 500s. The framework
// previously had no such check, which is why e2e/fixture-app and
// MIGRATE_TO_V2_PROMPT.md both suggested an app-side
// `var _ = []core.HttpCommand{…}` compile-time list as a workaround; this replaces it
// with a framework-side check that also covers ContentType, which no compile-time
// list can.
func ValidateHandlerParameters(handler core.HandlerFunc) error {
	rt := reflect.TypeOf(handler)
	if rt == nil || rt.Kind() != reflect.Func {
		return fmt.Errorf("route handler must be a function, got %T", handler)
	}

	messageType := reflect.TypeOf((*core.HttpMessage)(nil)).Elem()

	for i := 0; i < rt.NumIn(); i++ {
		in := rt.In(i)

		if in.Implements(messageType) {
			continue
		}
		if isPrimitiveParameter(in) {
			continue
		}

		// Anything else must be a DTO with a parseable ContentType.
		instance, ok := reflect.New(in).Interface().(core.HttpCommand)
		if !ok {
			return fmt.Errorf(
				"handler %s parameter %d of type %s does not implement core.HttpCommand",
				rt, i, in)
		}
		if !IsSupportedContentType(instance.ContentType()) {
			return unsupportedContentTypeError(in, instance.ContentType())
		}
	}

	return nil
}

// isPrimitiveParameter reports whether in is bound from a path parameter.
func isPrimitiveParameter(in reflect.Type) bool {
	switch in.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
