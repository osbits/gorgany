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
		keySlice = append(keySlice, ConvertReflectedValue(reflectedField))
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
			if fmt.Sprintf("%v", ConvertReflectedValue(reflectedValue2)) == fmt.Sprintf("%v", ConvertReflectedValue(reflectedValue)) {
				isExistsInResSlice = true
			}
		}
		if !isExistsInResSlice {
			resSlice = append(resSlice, sliceValue)
		}
	}

	return resSlice
}

func Prepend[T any](x []T, y T) []T {
	var empty T
	x = append(x, empty)
	copy(x[1:], x)
	x[0] = y
	return x
}
