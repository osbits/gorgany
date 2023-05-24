package multipart

import (
	"github.com/gorilla/schema"
	"gorgany/model"
	"reflect"
	"time"
)

func NewFormValuesDecoder() *schema.Decoder {
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

	return decoder
}
