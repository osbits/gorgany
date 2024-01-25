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
	var err error

	rvBody := util.IndirectValue(reflect.ValueOf(thiz.Body))
	if rvBody.Kind() == reflect.Slice {
		body, err = thiz.buildBodySlice(rvBody)
	} else if rvBody.Kind() == reflect.Struct {
		body, err = thiz.buildBodyElement(rvBody)
	} else {
		body = thiz.Body
	}

	if err != nil {
		return nil, err
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
		var err error

		rElement := util.IndirectValue(reflectSlice.Index(i))
		if rElement.Kind() == reflect.Slice {
			sliceElement, err = thiz.buildBodySlice(rElement)
		} else if rElement.Kind() == reflect.Struct {
			sliceElement, err = thiz.buildBodyElement(rElement)
		} else {
			sliceElement = rElement.Interface()
		}

		if err != nil {
			return nil, err
		}

		slice = append(slice, sliceElement)
	}
	return slice, nil
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
		isStruct := true

		if !rtField.IsExported() {
			continue
		}

		if util.IndirectType(rtField.Type).Kind() == reflect.Struct {
			if _, ok := rvField.Interface().(core.LimitedFieldsMarshaller); ok {
				if rtField.Anonymous {
					nestedElement, err := thiz.buildBodyElement(util.IndirectValue(rvField))
					if err != nil {
						return nil, err
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
		splitJsonTag := strings.Split(jsonTag, ",")
		if len(splitJsonTag) > 0 {
			jsonFieldName = splitJsonTag[0]
		}

		if util.IndirectType(rtField.Type).Kind() == reflect.Slice {
			nestedElement, err := thiz.buildBodySlice(util.IndirectValue(rvField))
			if err != nil {
				return nil, err
			}
			body[jsonFieldName] = nestedElement
			continue
		}

		if isStruct {
			if _, ok := rvField.Interface().(core.LimitedFieldsMarshaller); ok {
				nestedElement, err := thiz.buildBodyElement(util.IndirectValue(rvField))
				if err != nil {
					return nil, err
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

		if rvField.IsZero() {
			body[jsonFieldName] = nil
			continue
		}

		body[jsonFieldName] = rvField.Interface()
	}
	return body, nil
}
