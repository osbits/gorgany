package model

import (
	"encoding/json"
	"reflect"
	"strings"

	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/err"
	"git.qix.sx/gorgany/gorgany.git/log"
	"git.qix.sx/gorgany/gorgany.git/util"
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

	if thiz.Body != nil {
		rvBody := util.IndirectValue(reflect.ValueOf(thiz.Body))
		body, e = thiz.callBuilderFunc(rvBody)
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

func (thiz *ApiReturnObject) callBuilderFunc(reflectedValue reflect.Value) (any, error) {
	var body any
	var e error

	if !reflectedValue.IsValid() {
		log.Log().Warnf("The value passed to the ApiReturnObject is invalid")
		return nil, nil
	}

	if reflectedValue.Kind() == reflect.Slice {
		body, e = thiz.buildBodySlice(reflectedValue)
	} else if reflectedValue.Kind() == reflect.Struct {
		body, e = thiz.buildBodyElement(reflectedValue)
	} else if reflectedValue.Kind() == reflect.Map {
		body, e = thiz.buildBodyMap(reflectedValue)
	} else {
		body = reflectedValue.Interface()
	}

	return body, e
}

func (thiz *ApiReturnObject) buildBodySlice(reflectSlice reflect.Value) ([]any, error) {
	slice := make([]any, 0)
	for i := 0; i < reflectSlice.Len(); i++ {

		var sliceElement any
		var e error

		rElement := util.IndirectValue(reflectSlice.Index(i))
		sliceElement, e = thiz.callBuilderFunc(rElement)

		if e != nil {
			return nil, e
		}

		slice = append(slice, sliceElement)
	}
	return slice, nil
}

func (thiz *ApiReturnObject) buildBodyMap(reflectMap reflect.Value) (map[string]any, error) {
	finalMap := make(map[string]any)
	for _, key := range reflectMap.MapKeys() {
		if key.Kind() != reflect.String {
			continue
		}

		value := reflectMap.MapIndex(key)

		rElement := util.IndirectValue(value)
		mapElement, e := thiz.callBuilderFunc(rElement)

		if e != nil {
			return nil, e
		}

		finalMap[key.String()] = mapElement
	}
	return finalMap, nil
}

func (thiz *ApiReturnObject) buildBodyElement(element reflect.Value) (any, error) {
	body := make(map[string]any)

	if marshaller, ok := element.Interface().(json.Marshaler); ok {
		fieldContent, e := marshaller.MarshalJSON()
		if e != nil {
			return nil, e
		}
		return json.RawMessage(fieldContent), nil
	}

	allowedFields := []string{"*"}
	if limitedFields, ok := element.Interface().(core.LimitedFieldsMarshaller); ok {
		allowedFields = limitedFields.AllowedFields()
	}

	if len(allowedFields) == 0 {
		return body, nil
	}

	for i := 0; i < element.NumField(); i++ {
		rvField := element.Field(i)
		rtField := element.Type().Field(i)

		if !rtField.IsExported() {
			continue
		}

		jsonFieldName := parseJSONTag(rtField)

		if util.IndirectType(rvField.Type()).Kind() == reflect.Struct {
			if _, ok := rvField.Interface().(core.LimitedFieldsMarshaller); ok {
				if rtField.Anonymous {
					nestedElement, e := thiz.buildBodyElement(util.IndirectValue(rvField))
					if e != nil {
						return nil, e
					}
					body = util.MergeMaps(body, nestedElement.(map[string]any))
				}
				continue
			}
		}

		if !util.InArrayFunc(allowedFields, func(el string) bool {
			return strings.ToLower(el) == strings.ToLower(rtField.Name)
		}) && (allowedFields[0] != "*") {
			continue
		}

		if nestedValue, e := thiz.callBuilderFunc(rvField); e == nil {
			body[jsonFieldName] = nestedValue
		} else {
			return nil, e
		}
	}

	return body, nil
}

func parseJSONTag(rtField reflect.StructField) string {
	jsonTag := rtField.Tag.Get("json")
	if jsonTag == "-" || jsonTag == "" {
		return rtField.Name
	}

	splitTag := strings.Split(jsonTag, ",")
	if len(splitTag) > 0 && splitTag[0] != "" {
		return splitTag[0]
	}

	return rtField.Name
}
