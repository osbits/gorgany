package util

import (
	"fmt"
	"reflect"
)

func InArray(value any, slice any) bool {
	sliceArr := InterfaceSlice(slice)

	for _, sliceValue := range sliceArr {
		if fmt.Sprintf("%v", value) == fmt.Sprintf("%v", sliceValue) {
			return true
		}
	}
	return false
}

func Pluck(slice any, key string) []any {
	keySlice := make([]any, 0)

	if slice == nil {
		return keySlice
	}

	sliceArr := InterfaceSlice(slice)

	for _, sliceValue := range sliceArr {
		reflectedValue := reflect.ValueOf(sliceValue)
		reflectedField := reflectedValue.FieldByName(key)
		keySlice = append(keySlice, convertValueToString(reflectedField))
	}

	return keySlice
}

func InterfaceSlice(slice interface{}) []interface{} {
	s := reflect.ValueOf(slice)
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

func UniqueSlice[T any](slice []T) []T {
	resSlice := make([]T, 0)

	for _, sliceValue := range slice {
		isExistsInResSlice := false
		for _, valueInResSlice := range resSlice {
			reflectedValue := reflect.ValueOf(sliceValue)
			reflectedValue2 := reflect.ValueOf(valueInResSlice)
			if fmt.Sprintf("%v", convertValueToString(reflectedValue2)) == fmt.Sprintf("%v", convertValueToString(reflectedValue)) {
				isExistsInResSlice = true
			}
		}
		if !isExistsInResSlice {
			resSlice = append(resSlice, sliceValue)
		}
	}

	return resSlice
}

func convertValueToString(vf reflect.Value) any {
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
