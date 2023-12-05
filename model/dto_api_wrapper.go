package model

import (
	"encoding/json"
	"gorgany"
	"gorgany/err"
)

type ApiReturnObject struct {
	Errors     []any              `json:"errors"`
	HttpStatus gorgany.HttpStatus `json:"status_code"`
	Body       any                `json:"body"`
}

func (thiz *ApiReturnObject) MarshalJSON() ([]byte, error) {
	jsonMap := make(map[string]any)
	jsonMap["errors"] = thiz.Errors
	jsonMap["status"] = thiz.HttpStatus.Status
	jsonMap["code"] = thiz.HttpStatus.Code
	jsonMap["body"] = thiz.Body

	return json.Marshal(jsonMap)
}

func (thiz *ApiReturnObject) AddValidationError(e err.ValidationError) {
	if thiz.HttpStatus != gorgany.Validation {
		thiz.HttpStatus = gorgany.Validation
	}
	thiz.Errors = append(thiz.Errors, e)
}

func (thiz *ApiReturnObject) AddError(e any, status gorgany.HttpStatus) {
	thiz.HttpStatus = gorgany.Validation
	thiz.Errors = append(thiz.Errors, e)
}

func (thiz *ApiReturnObject) SetHttpStatus(status gorgany.HttpStatus) {
	thiz.HttpStatus = status
}

func (thiz *ApiReturnObject) SetBody(body any) {
	thiz.Body = body
}
