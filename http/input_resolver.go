package http

import (
	"encoding/json"
	"errors"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/decoder/multipart"
	error2 "git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/util"
	gorganyValidator "git.qix.sx/gorgany/gorgany.git/validator"
	"github.com/go-chi/chi"
	"github.com/gorilla/schema"
	url2 "net/url"
	"reflect"
)

type inputResolver struct {
	reflectedHandler reflect.Value
	message          *Message
}

func (thiz inputResolver) resolve() ([]reflect.Value, error) {
	args := make([]reflect.Value, 0)
	pathParams := thiz.collectPathParams()
	indexOfPrimitiveArguemnt := 0
	for i := 0; i < thiz.reflectedHandler.Type().NumIn(); i++ {
		in := thiz.reflectedHandler.Type().In(i)
		argTypeName := in.String()

		if in.Implements(reflect.TypeOf((*core.HttpMessage)(nil)).Elem()) {
			args = append(args, reflect.ValueOf(thiz.message))
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
				log.Log().Warnf("Argument of %s handler is not core.HttpCommand instance", thiz.reflectedHandler.Type().String())
				continue
			}
			parser := resolveBodyParser(httpCommand, thiz.message)

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

			if err := gorganyValidator.ValidateStruct(arg); err != nil {
				return nil, err
			}
		}

		args = append(args, reflect.Indirect(reflect.ValueOf(arg)))
	}

	thiz.message.inputParameters = args

	return args, nil
}

func (thiz inputResolver) collectPathParams() []string {
	routeParams := chi.RouteContext(thiz.message.request.Context()).URLParams

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

func resolveBodyParser(command core.HttpCommand, message *Message) bodyParser {
	if command.ContentType() == core.ApplicationJson {
		return jsonParser{message: message}
	} else if command.ContentType() == core.MultipartFormData {
		return multipartParser{message: message}
	} else if command.ContentType() == core.Query {
		return queryParser{message: message}
	}

	return nil
}

// json parser
type jsonParser struct {
	message *Message
}

func (thiz jsonParser) parse(arg interface{}) error {
	if len(thiz.message.GetBody()) == 0 { // todo: check it
		return nil
	}

	err := json.Unmarshal(thiz.message.GetBody(), arg)
	if err != nil {
		validationErrors := make(error2.ValidationErrors, 0)
		if errors.Is(err, &json.UnmarshalTypeError{}) {
			typeError := err.(*json.UnmarshalTypeError)
			validationErrors.AddValidationError(error2.ValidationError{
				Field: typeError.Field,
				Err:   typeError.Error(),
			})
		} else {
			checkAndAddIfValidationError(err, &validationErrors)
		}
		return &validationErrors
	}
	return nil
}

// multipart parser
type multipartParser struct {
	message *Message
}

func (thiz multipartParser) parse(arg interface{}) error {
	multipartForm := thiz.message.GetMultipartFormValues()
	decoder := multipart.NewFormValuesDecoder()
	err := decoder.Decode(arg, multipartForm.Value)
	if err != nil {
		validationErrors := make(error2.ValidationErrors, 0)
		if errors.As(err, &schema.MultiError{}) {
			multiError := err.(schema.MultiError)
			for key, err := range multiError {
				validationErrors.AddValidationError(error2.ValidationError{Field: key, Err: err.Error()})
			}
		} else {
			checkAndAddIfValidationError(err, &validationErrors)
		}
		return &validationErrors
	}
	err = multipart.DecodeFiles(multipartForm.File, arg)
	if err != nil {
		validationErrors := make(error2.ValidationErrors, 0)
		for key, _ := range multipartForm.File {
			validationErrors.AddValidationError(error2.ValidationError{Field: key, Err: "Incorrect files"})
		}
		return &validationErrors
	}
	return nil
}

// form parser
type queryParser struct {
	message *Message
}

func (thiz queryParser) parse(arg interface{}) error {
	decoder := multipart.NewFormValuesDecoder()
	values, err := url2.ParseQuery(thiz.message.GetRawQuery())
	if err != nil {
		return &error2.ValidationErrors{error2.ValidationError{
			Field: core.GeneralError,
			Err:   err.Error(),
		}}
	}
	err = decoder.Decode(arg, values)
	if err != nil {
		validationErrors := make(error2.ValidationErrors, 0)
		if errors.As(err, &schema.MultiError{}) {
			multiError := err.(schema.MultiError)
			for key, err := range multiError {
				validationErrors.AddValidationError(error2.ValidationError{Field: key, Err: err.Error()})
			}
		} else {
			checkAndAddIfValidationError(err, &validationErrors)
		}
		return &validationErrors
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
