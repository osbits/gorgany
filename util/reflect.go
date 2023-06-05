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
