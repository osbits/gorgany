package http

import (
	"encoding/json"
	"github.com/go-chi/chi"
	"gorgany"
	"gorgany/decoder/multipart"
	"gorgany/util"
	url2 "net/url"
	"reflect"
	"strconv"
)

type inputResolver struct {
	reflectedHandler reflect.Value
	message          Message
}

func (thiz inputResolver) resolve() []reflect.Value {
	args := make([]reflect.Value, 0)
	pathParams := thiz.collectPathParams()
	indexOfPrimitiveArguemnt := 0
	for i := 0; i < thiz.reflectedHandler.Type().NumIn(); i++ {
		in := thiz.reflectedHandler.Type().In(i)
		argTypeName := in.String()

		if argTypeName == "http.Message" {
			args = append(args, reflect.ValueOf(thiz.message))
			continue
		}

		reflectedInValue := reflect.New(in)
		arg := reflectedInValue.Interface()

		switch argTypeName {
		case "string", "bool", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
			param := pathParams[indexOfPrimitiveArguemnt]
			arg = resolvePrimitive(in.Kind(), param)
			indexOfPrimitiveArguemnt++
		default:
			contentType := thiz.message.GetHeader().Get("Content-Type")
			resolveBodyParser(contentType, thiz.message).parse(arg)
		}

		args = append(args, reflect.Indirect(reflect.ValueOf(arg)))
	}

	return args
}

func (thiz inputResolver) collectPathParams() []string {
	routeParams := chi.RouteContext(thiz.message.request.Context()).URLParams

	pathParams := make([]string, 0)
	for i := range routeParams.Values {
		if routeParams.Keys[i] == "namespace" {
			continue
		}
		pathParams = append(pathParams, routeParams.Values[i])
	}
	return pathParams
}

type bodyParser interface {
	parse(arg interface{})
}

func resolveBodyParser(contentType string, message Message) bodyParser {
	switch contentType {
	case gorgany.ApplicationJson:
		return jsonParser{message: message}
	case gorgany.MultipartFormData:
		return multipartParser{message: message}
	default:
		return formParser{message: message}
	}
}

// json parser
type jsonParser struct {
	message Message
}

func (thiz jsonParser) parse(arg interface{}) {
	err := json.Unmarshal(thiz.message.GetBody(), arg)
	if err != nil {
		panic(err)
	}
}

// multipart parser
type multipartParser struct {
	message Message
}

func (thiz multipartParser) parse(arg interface{}) {
	multipartForm := thiz.message.GetMultipartFormValues()
	decoder := multipart.NewFormValuesDecoder()
	err := decoder.Decode(arg, multipartForm.Value)
	if err != nil {
		panic(err)
	}
	err = multipart.DecodeFiles(multipartForm.File, arg)
	if err != nil {
		panic(err)
	}
}

// form parser
type formParser struct {
	message Message
}

func (thiz formParser) parse(arg interface{}) {
	decoder := multipart.NewFormValuesDecoder()
	values, err := url2.ParseQuery(thiz.message.GetBodyContent())
	if err != nil {
		panic(err)
	}
	err = decoder.Decode(arg, values)
	if err != nil {
		panic(err)
	}
}

// resolve primitive values for param in handler
type primitiveResolver interface {
	resolve(kind reflect.Kind, value string) any
}

// Common integer resolver
type intResolver struct {
}

func (thiz intResolver) resolve(kind reflect.Kind, value string) any {
	var arg any
	val, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		panic(err) //todo
	}
	reflectedValue := reflect.ValueOf(val)
	arg = util.ConvertReflectedValue(reflectedValue)
	return resolveIntegerValuer(kind, arg.(int64)).value()
}

// Common uinteger resolver
type uintResolver struct {
}

func (thiz uintResolver) resolve(kind reflect.Kind, value string) any {
	var arg any
	val, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		panic(err) //todo
	}
	reflectedValue := reflect.ValueOf(val)
	arg = util.ConvertReflectedValue(reflectedValue)
	return resolveUIntegerValuer(kind, arg.(uint64)).value()
}

// Common float resolver
type floatResolver struct {
}

func (thiz floatResolver) resolve(kind reflect.Kind, value string) any {
	var arg any
	val, err := strconv.ParseFloat(value, 64)
	if err != nil {
		panic(err)
	}
	reflectedValue := reflect.ValueOf(val)
	arg = util.ConvertReflectedValue(reflectedValue)
	return resolveFloatValuer(kind, arg.(float64)).value()
}

// bool resolver
type boolResolver struct {
}

func (thiz boolResolver) resolve(kind reflect.Kind, value string) any {
	arg, err := strconv.ParseBool(value)
	if err != nil {
		panic(err) //todo
	}
	return arg
}

func resolvePrimitive(kind reflect.Kind, value string) any {
	var resolver primitiveResolver
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		resolver = intResolver{}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		resolver = uintResolver{}
	case reflect.Float32, reflect.Float64:
		resolver = floatResolver{}
	case reflect.Bool:
		resolver = boolResolver{}
	default:
		return value
	}

	return resolver.resolve(kind, value)
}

// primitive value
type primitiveValuer interface {
	value() any
}

// Concrete integer resolver
func resolveIntegerValuer(kind reflect.Kind, value int64) primitiveValuer {
	switch kind {
	case reflect.Int:
		return Int{val: value}
	case reflect.Int8:
		return Int8{val: value}
	case reflect.Int16:
		return Int16{val: value}
	case reflect.Int32:
		return Int32{val: value}
	default:
		return Int64{val: value}
	}
}

type Int struct {
	val int64
}

func (thiz Int) value() any {
	return int(thiz.val)
}

type Int8 struct {
	val int64
}

func (thiz Int8) value() any {
	return int8(thiz.val)
}

type Int16 struct {
	val int64
}

func (thiz Int16) value() any {
	return int16(thiz.val)
}

type Int32 struct {
	val int64
}

func (thiz Int32) value() any {
	return int32(thiz.val)
}

type Int64 struct {
	val int64
}

func (thiz Int64) value() any {
	return thiz.val
}

// Concrete uinteger resolver
func resolveUIntegerValuer(kind reflect.Kind, value uint64) primitiveValuer {
	switch kind {
	case reflect.Uint:
		return Uint{val: value}
	case reflect.Uint8:
		return Uint8{val: value}
	case reflect.Int16:
		return Uint16{val: value}
	case reflect.Int32:
		return Uint32{val: value}
	default:
		return Uint64{val: value}
	}
}

type Uint struct {
	val uint64
}

func (thiz Uint) value() any {
	return uint(thiz.val)
}

type Uint8 struct {
	val uint64
}

func (thiz Uint8) value() any {
	return uint8(thiz.val)
}

type Uint16 struct {
	val uint64
}

func (thiz Uint16) value() any {
	return uint16(thiz.val)
}

type Uint32 struct {
	val uint64
}

func (thiz Uint32) value() any {
	return uint32(thiz.val)
}

type Uint64 struct {
	val uint64
}

func (thiz Uint64) value() any {
	return thiz.val
}

// Concrete float resolver
func resolveFloatValuer(kind reflect.Kind, value float64) primitiveValuer {
	switch kind {
	case reflect.Float32:
		return Float32{val: value}
	default:
		return Float64{val: value}
	}
}

type Float32 struct {
	val float64
}

func (thiz Float32) value() any {
	return float32(thiz.val)
}

type Float64 struct {
	val float64
}

func (thiz Float64) value() any {
	return thiz.val
}
