package multipart

import (
	"github.com/gorganyio/gorgany/app/core"
	"github.com/gorganyio/gorgany/decoder"
	"github.com/gorganyio/gorgany/model"
	"github.com/gorganyio/gorgany/util"
	"github.com/gorilla/schema"
	"reflect"
	"strings"
	"time"
	"unsafe"
)

type ValuesDecoder struct {
	formSchemaDecoder *schema.Decoder
}

func NewFormValuesDecoder() *ValuesDecoder {
	d := schema.NewDecoder()
	d.IgnoreUnknownKeys(true)
	d.RegisterConverter(model.FormDateTimeLocal{}, func(s string) reflect.Value {
		if s == "" {
			return reflect.ValueOf(model.FormDateTimeLocal{})
		}

		t, err := time.Parse("2006-01-02T15:04", s)
		if err != nil {
			panic(err)
		}
		return reflect.ValueOf(model.FormDateTimeLocal{Time: t})
	})

	d.RegisterConverter(model.FormDateLocal{}, func(s string) reflect.Value {
		if s == "" {
			return reflect.ValueOf(model.FormDateLocal{})
		}

		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			panic(err)
		}
		return reflect.ValueOf(model.FormDateLocal{Time: t})
	})

	d.RegisterConverter(model.DateLocal{}, func(s string) reflect.Value {
		if s == "" {
			return reflect.ValueOf(model.DateLocal{})
		}

		t, err := time.Parse(core.GlobalDateFormat, s)
		if err != nil {
			panic(err)
		}
		return reflect.ValueOf(model.DateLocal{FormDateLocal: model.FormDateLocal{Time: t}})
	})

	d.RegisterConverter(model.DateTimeLocal{}, func(s string) reflect.Value {
		if s == "" {
			return reflect.ValueOf(model.DateTimeLocal{})
		}

		t, err := time.Parse(core.GlobalDateTimeFormat, s)
		if err != nil {
			panic(err)
		}
		return reflect.ValueOf(model.DateTimeLocal{FormDateLocal: model.FormDateLocal{Time: t}})
	})

	return &ValuesDecoder{formSchemaDecoder: d}
}

func (thiz ValuesDecoder) Decode(dst interface{}, src map[string][]string) error {
	reflectedValue := reflect.ValueOf(dst)
	reflectedType := reflect.TypeOf(dst)

	// todo: redo this implementation on front for LocalizedString, after it can be removed
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

				if !reflectedField.IsValid() || reflectedField.Kind() != reflect.Slice {
					continue
				}

				reflectedNestedStruct := util.GetIndirectReflectElementOfSlice(reflectedField.Interface())

				if !reflectedNestedStruct.IsValid() {
					continue
				}

				mapInitiator, err := thiz.mapInitiatorImplementation(reflectedNestedStruct, el)
				if err != nil {
					return err
				}
				if mapInitiator != nil {
					reflectedField.Set(reflect.Append(reflectedField, reflect.ValueOf(mapInitiator)))
					continue
				}

				for subKey, value := range el {
					field := reflectedNestedStruct.FieldByNameFunc(func(name string) bool {
						return strings.ToLower(name) == subKey
					})
					if !field.IsValid() {
						continue
					}

					resolvedValue, err := util.ResolvePrimitive(field.Kind(), value)
					if err != nil {
						return err
					}
					field.Set(reflect.ValueOf(resolvedValue))
				}

				reflectedField.Set(reflect.Append(reflectedField, reflectedNestedStruct))
			}
		}
	}

	// todo: redo this implementation on front for LocalizedString, after it can be removed
	for key, values := range mapValues {
		reflectedField := reflectedValue.Elem().FieldByName(key)
		reflectedField.Set(reflect.ValueOf(values))
	}
	//

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

func (thiz ValuesDecoder) mapInitiatorImplementation(value reflect.Value, params map[string]string) (core.MapInitiator, error) {
	if value.Kind() != reflect.Ptr {
		value = reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr()))
	}

	if mapInitiator, ok := value.Interface().(core.MapInitiator); ok {
		err := mapInitiator.ValueOfMap(params)
		if err != nil {
			return nil, err
		}
		return mapInitiator, nil
	}

	return nil, nil
}
