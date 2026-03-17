package util

import (
	err2 "github.com/osbits/gorgany/err"
	"reflect"
	"strconv"
	"strings"
)

func ConvertReflectedValue(vf reflect.Value) any {
	switch vf.Kind() {
	case reflect.String:
		return vf.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return vf.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return vf.Uint()
	case reflect.Float32, reflect.Float64:
		return vf.Float()
	case reflect.Bool:
		if vf.Bool() {
			return true
		} else {
			return false
		}
	case reflect.Array, reflect.Slice:
		panic("Slice type is not supported yet!")
	case reflect.Ptr:
		panic("Pointer type is not supported yet!")
	default:
		return ""
	}
}

func IndirectValue(v reflect.Value) reflect.Value {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() == reflect.Interface && v.IsValid() {
		originalV := v
		v = v.Elem()
		if !v.IsValid() {
			v = originalV
		}
	}
	return v
}

func IndirectType(v reflect.Type) reflect.Type {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	return v
}

func GetIndirectElementOfSlice(slice any) any {
	return GetIndirectReflectElementOfSlice(slice).Interface()
}

func GetElementOfSlice(slice any) any {
	return GetReflectedElementOfSlice(slice).Interface()
}

func GetReflectedElementOfSlice(slice any) reflect.Value {
	rtSlice := reflect.TypeOf(slice)
	if rtSlice.Kind() != reflect.Slice {
		return reflect.ValueOf(nil)
	}

	rtSliceElem := rtSlice.Elem()

	if rtSliceElem.Kind() == reflect.Interface {
		rtSliceElem = rtSliceElem.Elem()
	}

	return reflect.New(rtSliceElem).Elem()
}

func GetIndirectReflectElementOfSlice(slice any) reflect.Value {
	rtSlice := reflect.TypeOf(slice)
	if rtSlice.Kind() != reflect.Slice {
		panic("")
	}
	model := reflect.MakeSlice(rtSlice, 1, 1).Index(0).Interface()
	rType := IndirectType(reflect.TypeOf(model))
	return reflect.New(rType).Elem()
}

func GetSliceFromAny(slice any) []any {
	rvSlice := IndirectValue(reflect.ValueOf(slice))
	if rvSlice.Kind() != reflect.Slice {
		panic("Value must be slice")
	}
	dest := make([]any, rvSlice.Len())
	for i := 0; i < rvSlice.Len(); i++ {
		dest[i] = rvSlice.Index(i).Interface()
	}

	return dest
}

func InitializeStruct(t reflect.Type, v reflect.Value) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		ft := t.Field(i)
		switch ft.Type.Kind() {
		case reflect.Map:
			f.Set(reflect.MakeMap(ft.Type))
		case reflect.Slice:
			f.Set(reflect.MakeSlice(ft.Type, 0, 0))
		case reflect.Chan:
			f.Set(reflect.MakeChan(ft.Type, 0))
		case reflect.Struct:
			InitializeStruct(ft.Type, f)
		case reflect.Ptr:
			fv := reflect.New(ft.Type.Elem())
			InitializeStruct(ft.Type.Elem(), fv.Elem())
			f.Set(fv)
		default:
		}
	}
}

// Resolve primitive values for param in handler
type PrimitiveResolver interface {
	Resolve(kind reflect.Kind, value string) (any, error)
}

// Common integer resolver
type IntResolver struct {
}

func (thiz IntResolver) Resolve(kind reflect.Kind, value string) (any, error) {
	var arg any
	val, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, err2.NewInputParamParseError(value, "int64")
	}
	reflectedValue := reflect.ValueOf(val)
	arg = ConvertReflectedValue(reflectedValue)
	return ResolveIntegerValuer(kind, arg.(int64)).Value(), nil
}

// Common uinteger resolver
type UintResolver struct {
}

func (thiz UintResolver) Resolve(kind reflect.Kind, value string) (any, error) {
	var arg any
	val, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil, err2.NewInputParamParseError(value, "uint64")
	}
	reflectedValue := reflect.ValueOf(val)
	arg = ConvertReflectedValue(reflectedValue)
	return ResolveUIntegerValuer(kind, arg.(uint64)).Value(), nil
}

// Common float resolver
type FloatResolver struct {
}

func (thiz FloatResolver) Resolve(kind reflect.Kind, value string) (any, error) {
	var arg any
	val, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil, err2.NewInputParamParseError(value, "float64")
	}
	reflectedValue := reflect.ValueOf(val)
	arg = ConvertReflectedValue(reflectedValue)
	return ResolveFloatValuer(kind, arg.(float64)).Value(), nil
}

// bool resolver
type BoolResolver struct {
}

func (thiz BoolResolver) Resolve(kind reflect.Kind, value string) (any, error) {
	arg, err := strconv.ParseBool(value)
	if err != nil {
		return nil, err2.NewInputParamParseError(value, "bool")
	}
	return arg, nil
}

func ResolvePrimitive(kind reflect.Kind, value string) (any, error) {
	var resolver PrimitiveResolver
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		resolver = IntResolver{}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		resolver = UintResolver{}
	case reflect.Float32, reflect.Float64:
		resolver = FloatResolver{}
	case reflect.Bool:
		resolver = BoolResolver{}
	default:
		return value, nil
	}

	return resolver.Resolve(kind, value)
}

// primitive value
type PrimitiveValuer interface {
	Value() any
}

// Concrete integer resolver
func ResolveIntegerValuer(kind reflect.Kind, value int64) PrimitiveValuer {
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

func (thiz Int) Value() any {
	return int(thiz.val)
}

type Int8 struct {
	val int64
}

func (thiz Int8) Value() any {
	return int8(thiz.val)
}

type Int16 struct {
	val int64
}

func (thiz Int16) Value() any {
	return int16(thiz.val)
}

type Int32 struct {
	val int64
}

func (thiz Int32) Value() any {
	return int32(thiz.val)
}

type Int64 struct {
	val int64
}

func (thiz Int64) Value() any {
	return thiz.val
}

// Concrete uinteger resolver
func ResolveUIntegerValuer(kind reflect.Kind, value uint64) PrimitiveValuer {
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

func (thiz Uint) Value() any {
	return uint(thiz.val)
}

type Uint8 struct {
	val uint64
}

func (thiz Uint8) Value() any {
	return uint8(thiz.val)
}

type Uint16 struct {
	val uint64
}

func (thiz Uint16) Value() any {
	return uint16(thiz.val)
}

type Uint32 struct {
	val uint64
}

func (thiz Uint32) Value() any {
	return uint32(thiz.val)
}

type Uint64 struct {
	val uint64
}

func (thiz Uint64) Value() any {
	return thiz.val
}

// Concrete float resolver
func ResolveFloatValuer(kind reflect.Kind, value float64) PrimitiveValuer {
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

func (thiz Float32) Value() any {
	return float32(thiz.val)
}

type Float64 struct {
	val float64
}

func (thiz Float64) Value() any {
	return thiz.val
}

func InterfaceSlice(slice interface{}) []interface{} {
	s := IndirectValue(reflect.ValueOf(slice))
	if s.Kind() != reflect.Slice {
		panic("InterfaceSlice() given a non-slice type")
	}

	// Keep the distinction between nil and empty slice input
	if s.IsNil() {
		return nil
	}

	ret := make([]interface{}, s.Len())

	for i := 0; i < s.Len(); i++ {
		val := s.Index(i)
		if s.Index(i).Kind() == reflect.Ptr {
			val = reflect.Indirect(s.Index(i))
		}
		ret[i] = val.Interface()
	}

	return ret
}

func IsGenericImplemented(s any, generic any) bool {
	rtS := reflect.TypeOf(s)
	rtGeneric := IndirectType(reflect.TypeOf(generic))

	for i := 0; i < rtGeneric.NumMethod(); i++ {
		method := rtGeneric.Method(i)
		_, found := rtS.MethodByName(method.Name)
		if !found {
			return false
		}

		//if foundMethod.Type.NumIn() != method.Type.NumIn() {
		//	return false
		//}
		//
		//if foundMethod.Type.NumOut() != method.Type.NumOut() {
		//	return false
		//}
	}

	return true
}

func FindFieldByTag(s reflect.Value, tag, tagValue string, allowSearchByCamelName bool) (bool, reflect.Value, reflect.StructField) {
	s = IndirectValue(s)
	rtS := IndirectType(s.Type())

	embeddedFields := make([]reflect.Value, 0)

	for i := 0; i < rtS.NumField(); i++ {
		field := rtS.Field(i)
		if field.Anonymous && IndirectType(field.Type).Kind() == reflect.Struct {
			embeddedFields = append(embeddedFields, s.Field(i))
			continue
		}
		rawTag := field.Tag.Get(tag)
		splitTag := strings.Split(rawTag, ",")
		if splitTag[0] == tagValue {
			return true, s.Field(i), field
		}
	}

	for _, embeddedField := range embeddedFields {
		found, v, sf := FindFieldByTag(embeddedField, tag, tagValue, true)
		if found {
			return found, v, sf
		}

	}

	structField, found := rtS.FieldByNameFunc(func(name string) bool {
		return strings.ToLower(name) == strings.ToLower(tagValue)
	})

	if !found {
		return false, reflect.Value{}, reflect.StructField{}
	}

	field := s.Field(structField.Index[0])
	return found, field, structField
}
