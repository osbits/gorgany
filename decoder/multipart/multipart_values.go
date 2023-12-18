package multipart

import (
	"git.qix.sx/gorgany/gorgany.git/decoder"
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
	decoder.IgnoreUnknownKeys(true)
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

	return &ValuesDecoder{formSchemaDecoder: decoder}
}

func (thiz ValuesDecoder) Decode(dst interface{}, src map[string][]string) error {
	reflectedValue := reflect.ValueOf(dst)

	// todo: redo this implementation on front for LocalizedString after it can be removed
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
	//

	queryParams, err := decoder.ParseUrlValues(src)
	if err != nil {
		return err
	}

	for key, values := range queryParams {
		if mapArray, ok := values.([]map[string]string); ok {
			for _, el := range mapArray {
				reflectedField := util.IndirectValue(reflectedValue).FieldByNameFunc(func(name string) bool {
					return strings.ToLower(name) == key
				})

				if !reflectedField.IsValid() {
					continue
				}

				reflectedNestedStruct := util.GetReflectElementOfSlice(reflectedField.Interface())
				if !reflectedNestedStruct.IsValid() {
					continue
				}

				for subKey, value := range el {
					if reflectedField.Kind() != reflect.Slice {
						continue
					}

					field := reflectedNestedStruct.FieldByNameFunc(func(name string) bool {
						return strings.ToLower(name) == subKey
					})
					if !field.IsValid() {
						continue
					}

					field.Set(reflect.ValueOf(value))
				}
				reflectedField.Set(reflect.Append(reflectedField, reflectedNestedStruct))
			}
		}
	}

	for key, values := range mapValues {
		reflectedField := reflectedValue.Elem().FieldByName(key)
		reflectedField.Set(reflect.ValueOf(values))
	}

	return thiz.formSchemaDecoder.Decode(dst, src)
}
