package http

import (
	"errors"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/go-chi/chi"
	"reflect"
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
			httpCommand, ok := arg.(core.HttpCommand)
			if !ok {
				log.Log().Warnf("Argument of %s handler is not core.HttpCommand instance", thiz.ReflectedHandler.Type().String())
				continue
			}
			parser := resolveBodyParser(httpCommand, thiz.Message)

			if parser == nil {
				log.Log().Warnf("Body parser could not be resolved!")
			}

			err := parser.parse(arg)
			if err != nil {
				return nil, err
			}

			//err = service.GetContainer().Make(&arg) error: invalid structure due to arg is an interface{} but not a concrete type
			//if err != nil {
			//	return nil, err
			//}

			if err := thiz.Validator.ValidateStruct(arg); err != nil {
				return nil, err
			}
		}

		args = append(args, reflect.Indirect(reflect.ValueOf(arg)))
	}

	thiz.Message.(*Message).inputParameters = args

	return args, nil
}

func (thiz *InputResolver) collectPathParams() []string {
	routeParams := chi.RouteContext(thiz.Message.GetRequest().Context()).URLParams

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
	parse(arg interface{}) error
}

func resolveBodyParser(command core.HttpCommand, message core.HttpMessage) bodyParser {
	if command.ContentType() == core.ApplicationJson {
		return jsonParser{message: message}
	} else if command.ContentType() == core.MultipartFormData {
		return multipartParser{message: message}
	} else if command.ContentType() == core.Query {
		return queryParser{message: message}
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
