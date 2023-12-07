package util

import "reflect"

func NullSafe(fn func() any) (value any) {
	defer func() {
		if r := recover(); r != nil {
			value = nil
		}
	}()

	rvFn := reflect.ValueOf(fn)
	output := rvFn.Call(nil)
	value = output[0].Interface()
	return value
}
