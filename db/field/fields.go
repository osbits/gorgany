package field

import (
	"database/sql"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"reflect"
)

func NewNullField[T any](v *T) NullField[T] {
	nullField := NullField[T]{}
	if v != nil {
		nullField.SetValue(*v)
	}
	return nullField
}

type NullField[T any] struct {
	sql.Null[T]
}

func (thiz NullField[T]) GetValue() any {
	if thiz.Valid {
		return thiz.V
	}
	return nil
}

func (thiz *NullField[T]) SetValue(v any) {
	rv := reflect.ValueOf(v)

	var emptyValue T
	rtEmptyValue := reflect.TypeOf(emptyValue)

	zeroer, ok := v.(yaml.IsZeroer)
	if v == nil || ok && zeroer.IsZero() {
		thiz.V = emptyValue
		thiz.Valid = false
		return
	} else if rv.Kind() != reflect.Ptr {
		thiz.V = v.(T)
		thiz.Valid = true
		return
	} else if rv.IsNil() {
		thiz.V = emptyValue
		thiz.Valid = false
		return
	}

	if rtEmptyValue.Kind() != reflect.Ptr {
		v = rv.Elem().Interface()
	}
	thiz.V = v.(T)
	thiz.Valid = true
}

func (thiz NullField[T]) MarshalJSON() ([]byte, error) {
	if !thiz.Valid {
		return []byte("null"), nil
	}

	return json.Marshal(thiz.V)
}
