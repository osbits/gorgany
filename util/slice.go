package util

import (
	"fmt"
	"golang.org/x/exp/constraints"
	"reflect"
)

// InArray
func InArray(value any, slice any) bool {
	sliceArr := InterfaceSlice(slice)

	for _, sliceValue := range sliceArr {
		if fmt.Sprintf("%v", value) == fmt.Sprintf("%v", sliceValue) {
			return true
		}
	}
	return false
}

func InArrayFunc[T any](slice []T, callback func(element T) bool) bool {
	for i := range slice {
		el := slice[i]
		if callback(el) {
			return true
		}
	}
	return false
}

// Pluck
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

func PluckInto[T any, R any](slice []T, dest *[]R, key string) {
	if slice == nil {
		return
	}

	for _, sliceValue := range slice {
		reflectedValue := IndirectValue(reflect.ValueOf(sliceValue))
		reflectedField := reflectedValue.FieldByName(key)
		*dest = append(*dest, reflectedField.Interface().(R))
	}
}

//func UniqueSlice[T any](slice []T) []T {
//	resSlice := make([]T, 0)
//
//	for _, sliceValue := range slice {
//		isExistsInResSlice := false
//		for _, valueInResSlice := range resSlice {
//			reflectedValue := reflect.ValueOf(sliceValue)
//			reflectedValue2 := reflect.ValueOf(valueInResSlice)
//			if fmt.Sprintf("%v", ConvertReflectedValue(reflectedValue2)) == fmt.Sprintf("%v", ConvertReflectedValue(reflectedValue)) {
//				isExistsInResSlice = true
//			}
//		}
//		if !isExistsInResSlice {
//			resSlice = append(resSlice, sliceValue)
//		}
//	}
//
//	return resSlice
//}

// Add
func Prepend[T any](x []T, y T) []T {
	var empty T
	x = append(x, empty)
	copy(x[1:], x)
	x[0] = y
	return x
}

// Unique
func UniqueSlice[T comparable](s []T) []T {
	uniqueSlice := make([]T, 0, len(s))
	seen := make(map[T]bool, len(s))
	for _, v := range s {
		if !seen[v] {
			uniqueSlice = append(uniqueSlice, v)
			seen[v] = true
		}
	}
	return uniqueSlice
}

func UniqueSliceByClosure[T any, V comparable](s []T, closure func(el T) V) []T {
	uniqueSlice := make([]T, 0, len(s))
	seen := make(map[V]bool, len(s))
	for _, el := range s {
		prop := closure(el)
		if !seen[prop] {
			uniqueSlice = append(uniqueSlice, el)
			seen[prop] = true
		}
	}
	return uniqueSlice
}

// Sort
func Sort[T any, V constraints.Ordered](s []T, closure func(el T) V) {
	if len(s) <= 1 {
		return
	}

	pivotIndex := len(s) / 2
	pivot := closure(s[pivotIndex])

	left := make([]T, 0, len(s))
	right := make([]T, 0, len(s))

	for i := range s {
		if i == pivotIndex {
			continue
		}
		value := closure(s[i])
		if value < pivot {
			left = append(left, s[i])
		} else {
			right = append(right, s[i])
		}
	}

	Sort(left, closure)
	Sort(right, closure)

	copy(s, append(append(left, s[pivotIndex]), right...))
}

// Find
func FindAll[T any](slice []T, closure func(T) bool) []T {
	dest := make([]T, 0)
	for _, el := range slice {
		if closure(el) {
			dest = append(dest, el)
		}
	}
	return dest
}

func Find[T any](slice []T, dest *T, closure func(T) bool) {
	for _, el := range slice {
		if closure(el) {
			*dest = el
		}
	}
}

// Contains
func ContainsByClosure[T any](slice []T, closure func(T) bool) bool {
	for _, el := range slice {
		if closure(el) {
			return true
		}
	}
	return false
}

func Contains[T comparable](slice []T, value T) bool {
	for _, el := range slice {
		if el == value {
			return true
		}
	}
	return false
}

func ContainsAll[T comparable](slice []T, values []T) bool {
	for _, value := range values {
		if !Contains(slice, value) {
			return false
		}
	}
	return true
}

func ContainsAnyByClosure[T any, K comparable](slice []T, values []K, closure func(el T) K) bool {
	for _, el := range slice {
		value := closure(el)
		if Contains(values, value) {
			return true
		}
	}
	return false
}

func ContainsSameTypeByClosure[T any, K comparable](slice []T, values []T, closure func(el T) K) bool {
	for _, v := range values {
		value := closure(v)
		for _, el := range slice {
			elSlice := closure(el)
			if value == elSlice {
				return true
			}
		}
	}
	return false
}

// Group
func Group[T any, V comparable](s []T, closure func(T) V) map[V][]T {
	grouped := make(map[V][]T)
	for _, el := range s {
		key := closure(el)
		grouped[key] = append(grouped[key], el)
	}
	return grouped
}
