package model

import (
	"encoding/json"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/util"
	"github.com/iancoleman/strcase"
	"reflect"
	"strings"
)

type ApiReturnObject struct {
	Errors     []any
	HttpStatus core.HttpStatus
	Body       any
}

func (thiz *ApiReturnObject) MarshalJSON() ([]byte, error) {
	tmpStruct := struct {
		Status int    `json:"status"`
		Code   string `json:"status_code"`
		Body   any    `json:"body"`
		Errors []any  `json:"errors"`
	}{}

	tmpStruct.Status = thiz.HttpStatus.Status
	tmpStruct.Code = thiz.HttpStatus.Code

	var body any
	var e error

	rvBody := util.IndirectValue(reflect.ValueOf(thiz.Body))
	if rvBody.Kind() == reflect.Slice {
		body, e = thiz.buildBodySlice(rvBody)
	} else if rvBody.Kind() == reflect.Struct {
		body, e = thiz.buildBodyElement(rvBody)
	} else if rvBody.Kind() == reflect.Map {
		body, e = thiz.buildBodyMap(rvBody)
	} else {
		body = thiz.Body
	}

	if e != nil {
		return nil, e
	}

	tmpStruct.Body = body

	tmpStruct.Errors = thiz.Errors

	return json.Marshal(tmpStruct)
}

func (thiz *ApiReturnObject) AddValidationError(e err.ValidationError) {
	if thiz.HttpStatus != core.ValidationHttpStatus {
		thiz.HttpStatus = core.ValidationHttpStatus
	}
	thiz.Errors = append(thiz.Errors, e)
}

func (thiz *ApiReturnObject) AddError(e any, status core.HttpStatus) {
	thiz.HttpStatus = core.ValidationHttpStatus
	thiz.Errors = append(thiz.Errors, e)
}

func (thiz *ApiReturnObject) SetHttpStatus(status core.HttpStatus) {
	thiz.HttpStatus = status
}

func (thiz *ApiReturnObject) SetBody(body any) {
	thiz.Body = body
}

func (thiz *ApiReturnObject) HasErrors() bool {
	return len(thiz.Errors) > 0
}

func (thiz *ApiReturnObject) buildBodySlice(reflectSlice reflect.Value) ([]any, error) {
	slice := make([]any, 0)
	for i := 0; i < reflectSlice.Len(); i++ {

		var sliceElement any
		var e error

		rElement := util.IndirectValue(reflectSlice.Index(i))
		if rElement.Kind() == reflect.Slice {
			sliceElement, e = thiz.buildBodySlice(rElement)
		} else if rElement.Kind() == reflect.Struct {
			sliceElement, e = thiz.buildBodyElement(rElement)
		} else if rElement.Kind() == reflect.Map {
			sliceElement, e = thiz.buildBodyMap(rElement)
		} else {
			sliceElement = rElement.Interface()
		}

		if e != nil {
			return nil, e
		}

		slice = append(slice, sliceElement)
	}
	return slice, nil
}

func (thiz *ApiReturnObject) buildBodyMap(reflectMap reflect.Value) (map[string]any, error) {
	finalMap := make(map[string]any, 0)
	for _, key := range reflectMap.MapKeys() {
		value := reflectMap.MapIndex(key)

		var mapElement any
		var e error
		rElement := util.IndirectValue(value)
		if rElement.Kind() == reflect.Slice {
			mapElement, e = thiz.buildBodySlice(rElement)
		} else if rElement.Kind() == reflect.Struct {
			mapElement, e = thiz.buildBodyElement(rElement)
		} else if rElement.Kind() == reflect.Map {
			mapElement, e = thiz.buildBodyMap(rElement)
		} else {
			mapElement = rElement.Interface()
		}

		if e != nil {
			return nil, e
		}

		if key.Kind() != reflect.String {
			continue
		}
		finalMap[key.String()] = mapElement
	}
	return finalMap, nil
}

func (thiz *ApiReturnObject) buildBodyElement(element reflect.Value) (map[string]any, error) {
	body := make(map[string]any)

	allowedFields := []string{"*"}
	if limitedFields, ok := element.Interface().(core.LimitedFieldsMarshaller); ok {
		allowedFields = limitedFields.AllowedFields()
	}

	for i := 0; i < element.NumField(); i++ {
		rvField := element.Field(i)
		rtField := element.Type().Field(i)
		isStruct := false

		if !rtField.IsExported() {
			continue
		}

		if util.IndirectType(rtField.Type).Kind() == reflect.Struct {
			if _, ok := rvField.Interface().(core.LimitedFieldsMarshaller); ok {
				if rtField.Anonymous {
					nestedElement, e := thiz.buildBodyElement(util.IndirectValue(rvField))
					if e != nil {
						return nil, e
					}
					body = util.MergeMaps(body, nestedElement)
				}
				continue
			}
			isStruct = true
		}

		allowField := util.InArrayFunc(allowedFields, func(el string) bool {
			return strings.ToLower(strcase.ToLowerCamel(strings.ToLower(el))) == strings.ToLower(strcase.ToLowerCamel(rtField.Name))
		})
		if !allowField && (len(allowedFields) > 0 && allowedFields[0] != "*") {
			continue
		}

		jsonFieldName := rtField.Name
		jsonTag := rtField.Tag.Get("json")
		if jsonTag != "-" && jsonTag != "" {
			splitJsonTag := strings.Split(jsonTag, ",")
			if len(splitJsonTag) > 0 {
				jsonFieldName = splitJsonTag[0]
			}
		}

		if util.IndirectType(rtField.Type).Kind() == reflect.Slice {
			nestedElement, e := thiz.buildBodySlice(util.IndirectValue(rvField))
			if e != nil {
				return nil, e
			}
			body[jsonFieldName] = nestedElement
			continue
		}

		if rvField.IsZero() {
			if rvField.IsValid() {
				body[jsonFieldName] = rvField.Interface()
			} else {
				body[jsonFieldName] = nil
			}
			continue
		}

		if isStruct {
			if _, ok := rvField.Interface().(core.LimitedFieldsMarshaller); ok {
				nestedElement, e := thiz.buildBodyElement(util.IndirectValue(rvField))
				if e != nil {
					return nil, e
				}
				body[jsonFieldName] = nestedElement
				continue
			}

			body[jsonFieldName] = rvField.Interface()
			continue
		}

		if util.IndirectType(rtField.Type).Kind() == reflect.Bool {
			body[jsonFieldName] = rvField.Interface()
			continue
		}

		body[jsonFieldName] = rvField.Interface()
	}
	return body, nil
}
