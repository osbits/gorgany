package multipart

import (
	"git.qix.sx/gorgany/gorgany.git/model"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/gorilla/schema"
	"reflect"
	"strings"
	"time"
)

type ValuesDecoder struct {
	formSchemaDecoder *schema.Decoder
}

func NewFormValuesDecoder() *ValuesDecoder {
	decoder := schema.NewDecoder()
	decoder.RegisterConverter(model.FormDateTimeLocal{}, func(s string) reflect.Value {
		if s == "" {
			return reflect.ValueOf(model.FormDateTimeLocal{})
		}

		t, err := time.Parse("2006-01-02T15:04", s)
		if err != nil {
			panic(err)
		}
		return reflect.ValueOf(model.FormDateTimeLocal{Time: t})
	})

	decoder.RegisterConverter(model.FormDateLocal{}, func(s string) reflect.Value {
		if s == "" {
			return reflect.ValueOf(model.FormDateLocal{})
		}

		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			panic(err)
		}
		return reflect.ValueOf(model.FormDateLocal{Time: t})
	})

	decoder.IgnoreUnknownKeys(true)

	return &ValuesDecoder{formSchemaDecoder: decoder}
}

func (thiz ValuesDecoder) Decode(dst interface{}, src map[string][]string) error {
	reflectedValue := reflect.ValueOf(dst)
	reflectedType := reflect.TypeOf(dst)

	mapValues := make(map[string]map[string]string)
	for key, values := range src {
		splitKey := strings.Split(key, ".")
		if len(splitKey) != 2 {
			continue
		}
		_, ok := mapValues[splitKey[0]]
		if !ok {
			mapValues[splitKey[0]] = make(map[string]string)
		}
		mapValues[splitKey[0]][splitKey[1]] = values[0]
		delete(src, key)
	}

	for key, values := range mapValues {
		reflectedField := reflectedValue.Elem().FieldByName(key)
		reflectedField.Set(reflect.ValueOf(values))
	}

	for key, values := range src {
		if len(values) > 1 {
			continue
		}

		reflectedField := reflectedValue.Elem().FieldByName(key)
		if !reflectedField.IsValid() {
			continue
		}

		if reflectedField.Type().Kind() != reflect.Ptr {
			continue
		}

		val := values[0]
		rv := reflect.ValueOf(val)
		if !rv.IsZero() {
			continue
		}

		reflectedFieldType, _ := util.IndirectType(reflectedType).FieldByName(key)
		reflectedField.Set(reflect.Zero(reflectedFieldType.Type))

		delete(src, key)
	}

	return thiz.formSchemaDecoder.Decode(dst, src)
}
