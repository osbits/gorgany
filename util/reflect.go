package util

import "reflect"

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
	return v
}

func IndirectType(v reflect.Type) reflect.Type {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	return v
}

func GetElementOfSlice(slice any) any {
	rtSlice := reflect.TypeOf(slice)
	if rtSlice.Kind() != reflect.Slice {
		panic("")
	}
	model := reflect.MakeSlice(rtSlice, 1, 1).Index(0).Interface()
	rType := IndirectType(reflect.TypeOf(model))
	return reflect.New(rType).Elem().Interface()
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
